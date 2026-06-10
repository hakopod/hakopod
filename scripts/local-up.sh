#!/usr/bin/env bash
# Development environment only. Does not change ~/.kube/config.
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
source "$ROOT/deploy/local/versions.env"
for tool in docker kubectl helm python3 curl; do
  command -v "$tool" >/dev/null || { echo "Required tool missing: $tool" >&2; exit 1; }
done
python3 - <<'PY'
import subprocess
try:
    subprocess.run(['docker','info'], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, check=True, timeout=15)
except subprocess.TimeoutExpired:
    raise SystemExit('Docker did not respond within 15 seconds. Restore the Docker engine, then retry local-up.sh.')
except subprocess.CalledProcessError:
    raise SystemExit('Docker is unavailable. Start the Docker engine, then retry local-up.sh.')
PY
"$ROOT/scripts/local-tools.sh"
K3D="$ROOT/.local/bin/k3d"
mkdir -p "$ROOT/.local"
chmod 700 "$ROOT/.local"
umask 077
if ! "$K3D" cluster list -o json | python3 -c 'import json,sys; sys.exit(not any(c["name"]=="hakopod-dev" for c in json.load(sys.stdin)))'; then
  python3 - <<'PY'
import socket
for port in (16443, 18080, 18443):
    with socket.socket() as s:
        try: s.bind(('127.0.0.1', port))
        except OSError: raise SystemExit(f'Port {port} is busy; refusing to disturb another service.')
PY
  "$K3D" cluster create hakopod-dev --servers 1 --agents 0 \
    --image "$K3S_IMAGE" --servers-memory 2304m \
    --api-port 127.0.0.1:16443 \
    --port 127.0.0.1:18080:30080@server:0 \
    --port 127.0.0.1:18443:30443@server:0 \
    --k3s-arg '--disable=traefik,servicelb,local-storage@server:*' \
    --k3s-arg '--secrets-encryption@server:*' \
    --k3s-arg '--kubelet-arg=max-pods=50@server:*' \
    --kubeconfig-update-default=false --kubeconfig-switch-context=false \
    --wait --timeout 240s
else
  "$K3D" cluster start hakopod-dev --wait --timeout 180s
fi
"$K3D" kubeconfig get hakopod-dev > "$ROOT/.local/kubeconfig"
# k3d's local TCP forwarding helper is small but otherwise inherits the VM limit.
docker update --memory 64m --memory-swap 64m --cpus 0.25 k3d-hakopod-dev-serverlb >/dev/null
export KUBECONFIG="$ROOT/.local/kubeconfig"
[[ "$(kubectl config current-context)" == k3d-hakopod-dev ]] || { echo 'Unexpected kubeconfig context.' >&2; exit 1; }
kubectl wait --for=condition=Ready nodes --all --timeout=180s
dns_change=$(kubectl apply -f "$ROOT/deploy/local/coredns-custom.yaml")
if [[ "$dns_change" != *unchanged* ]]; then
  kubectl -n kube-system rollout restart deployment/coredns
fi
kubectl -n kube-system rollout status deployment/coredns --timeout=120s
export HELM_REPOSITORY_CONFIG="$ROOT/.local/helm/repositories.yaml"
export HELM_REPOSITORY_CACHE="$ROOT/.local/helm/cache"
mkdir -p "$HELM_REPOSITORY_CACHE"
helm repo add haproxytech https://haproxytech.github.io/helm-charts >/dev/null
helm dependency build "$ROOT/deploy/charts/hakopod-platform" --skip-refresh
python3 - "$ROOT" <<'PY'
import hashlib, pathlib, sys
root = pathlib.Path(sys.argv[1])
name = 'kubernetes-ingress-1.54.0.tgz'
expected = next(line.split()[0] for line in (root/'deploy/local/checksums.sha256').read_text().splitlines() if line and not line.startswith('#') and line.split()[-1] == name)
actual = hashlib.sha256((root/'deploy/charts/hakopod-platform/charts'/name).read_bytes()).hexdigest()
if actual != expected:
    raise SystemExit('HAProxy chart checksum mismatch; refusing to install')
PY
helm upgrade --install hakopod-ingress "$ROOT/deploy/charts/hakopod-platform" \
  --namespace haproxy-controller --create-namespace --wait --timeout 240s

if ! [[ -f "$ROOT/.local/postgres.env" ]]; then
  python3 - "$ROOT" <<'PY'
import pathlib, secrets, sys
root = pathlib.Path(sys.argv[1])
password = secrets.token_hex(24)
(root / '.local/postgres.env').write_text(f'POSTGRES_USER=hakopod\nPOSTGRES_DB=hakopod\nPOSTGRES_PASSWORD={password}\n')
PY
fi
if docker container inspect hakopod-postgres >/dev/null 2>&1; then
  [[ "$(docker inspect --format '{{index .Config.Labels "com.hakopod.development"}}' hakopod-postgres)" == true ]] || { echo 'Container hakopod-postgres is not owned by this environment.' >&2; exit 1; }
  docker start hakopod-postgres >/dev/null
else
  python3 - <<'PY'
import socket
with socket.socket() as s:
    try: s.bind(('127.0.0.1', 55432))
    except OSError: raise SystemExit('Port 55432 is busy; refusing to disturb another database.')
PY
  docker volume create --label com.hakopod.development=true hakopod-postgres-data >/dev/null
  docker run --detach --name hakopod-postgres --label com.hakopod.development=true \
    --restart unless-stopped --memory 256m --cpus 0.5 \
    --publish 127.0.0.1:55432:5432 --env-file "$ROOT/.local/postgres.env" \
    --volume hakopod-postgres-data:/var/lib/postgresql/data \
    --health-cmd 'pg_isready -U hakopod -d hakopod' --health-interval 5s --health-timeout 3s --health-retries 12 \
    "$POSTGRES_IMAGE" -c shared_buffers=32MB -c max_connections=30 -c work_mem=2MB \
    -c maintenance_work_mem=32MB -c wal_buffers=4MB >/dev/null
fi
for attempt in $(seq 1 30); do
  if docker exec hakopod-postgres pg_isready -U hakopod -d hakopod >/dev/null 2>&1; then break; fi
  sleep 1
done
docker exec hakopod-postgres pg_isready -U hakopod -d hakopod
python3 - "$ROOT" <<'PY'
import pathlib, shlex, sys
root = pathlib.Path(sys.argv[1])
values = dict(line.split('=',1) for line in (root / '.local/postgres.env').read_text().splitlines())
env = {
    'HAKOPOD_DATABASE_URL': f'postgres://hakopod:{values["POSTGRES_PASSWORD"]}@127.0.0.1:55432/hakopod?sslmode=disable',
    'HAKOPOD_KUBECONFIG': str(root / '.local/kubeconfig'),
    'KUBECONFIG': str(root / '.local/kubeconfig'),
    'HAKOPOD_APP_DOMAIN': '127.0.0.1.sslip.io',
    'HAKOPOD_INGRESS_CLASS': 'haproxy',
    'HAKOPOD_PUBLIC_PORT': '18080',
    'HAKOPOD_ROLLOUT_TIMEOUT': '120s',
    'GOMEMLIMIT': '192MiB',
    'GOMAXPROCS': '2',
}
(root / '.local/env').write_text(''.join(f'export {key}={shlex.quote(value)}\n' for key,value in env.items()))
PY
echo 'Hakopod development infrastructure ready. Source .local/env before running the API.'
python3 "$ROOT/scripts/local-auth.py"
echo 'Application HTTP: 127.0.0.1:18080; PostgreSQL: 127.0.0.1:55432; kubeconfig: .local/kubeconfig'
