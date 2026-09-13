#!/usr/bin/env bash
# A dedicated Linux host installer, also used by the verified release bootstrap.
set -euo pipefail
set +x
umask 077

bundle_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
helper="$bundle_root/installer/host.py"
config_path='' artifact_dir='' target_arch='' release_version='' dry_run=false assume_yes=false resume=false input_tmp='' database_input_tmp='' oauth_input_dir=''
die() { printf 'Installer: %s\n' "$*" >&2; exit 1; }
usage() {
  cat <<'EOF'
Usage: bash scripts/install.sh --artifact-dir DIR [--config FILE] [--dry-run]
       [--arch amd64|arm64] [--version VERSION] [--resume] [--yes]

Without --config, prompts for operator-owned domains, addresses and options.
--dry-run validates configuration and local release checksums without installation.
--yes accepts the printed plan for unattended installs; it never supplies identity.
--resume requires the original config, artifact bytes and ownership marker.
--version selects the already verified release and must match explicit config.
See installer/README.md for prerequisites and the prebuilt release bootstrap.
EOF
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --config|--artifact-dir|--arch|--version)
      [ "$#" -ge 2 ] || die "$1 requires a value"
      case "$1" in --config) config_path=$2;; --artifact-dir) artifact_dir=$2;; --arch) target_arch=$2;; --version) release_version=$2;; esac
      shift 2;;
    --dry-run) dry_run=true; shift;;
    --yes) assume_yes=true; shift;;
    --resume) resume=true; shift;;
    --help|-h) usage; exit 0;;
    *) die "Unknown argument: $1";;
  esac
done
case "$target_arch" in ''|amd64|arm64) ;; *) die '--arch must be amd64 or arm64';; esac
command -v python3 >/dev/null || die 'Python3.10+ is required for strict configuration and archive validation'
python3 -c 'import sys; assert sys.version_info >= (3,10), "Python3.10+ required"'
[ -f "$helper" ] || die 'Use the complete repository or extracted installer bundle, not install.sh alone'
cleanup() { if [ -n "$oauth_input_dir" ]; then rm -rf -- "$oauth_input_dir"; fi; if [ -n "$input_tmp" ]; then rm -f -- "$input_tmp"; fi; if [ -n "$database_input_tmp" ]; then rm -f -- "$database_input_tmp"; fi; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
prompt() {
  local variable=$1 label=$2 default=$3 answer
  printf '%s [%s]: ' "$label" "$default" >&2
  IFS= read -r answer || die 'Input ended; use --config for unattended installation'
  printf -v "$variable" '%s' "${answer:-$default}"
}
if [ -z "$artifact_dir" ]; then
  [ -t 0 ] || die '--artifact-dir is required: use your verified local build directory'
  prompt artifact_dir 'Directory containing both local release artifacts and SHA256SUMS' ''
fi
[ -d "$artifact_dir" ] || die 'Artifact directory does not exist'
artifact_dir=$(cd -- "$artifact_dir" && pwd)
if [ -z "$config_path" ]; then
  [ -t 0 ] || die 'Noninteractive input requires --config'
  version='' app_domain='' node_ip='' node_name='' supervisor_host='' dashboard_mode=''
  dashboard_port='' acme='' storage='' k3s_memory_mib='' api_memory_mib=''
  dashboard_memory_mib='' postgres_memory_mib='' max_pods='' deployment_mode='' public_tcp_ports=''
  database_mode='' install_docker=''
  prompt deployment_mode 'Deployment mode: self-hosted or managed-cloud' 'self-hosted'
  case "$deployment_mode" in self-hosted|managed-cloud) ;; *) die 'deployment_mode must be self-hosted or managed-cloud';; esac
  if [ -n "$release_version" ]; then version=$release_version; else prompt version 'Hakopod release version' '0.1.0-dev'; fi
  prompt app_domain 'Operator-owned application DNS domain (for example apps.example.com)' ''
  prompt node_ip 'This server IPv4 address, reachable by future workers' ''
  prompt node_name 'Kubernetes node name' 'hakopod-server'
  prompt supervisor_host 'Worker-reachable K3s DNS name or IPv4 address' "$node_ip"
  prompt dashboard_mode 'Dashboard access: ssh or https with your certificate' 'ssh'
  if [ "$dashboard_mode" = https ]; then
    prompt dashboard_port 'Dashboard HTTPS port; application HTTPS uses443' '8443'
    prompt dashboard_origin 'Dashboard origin, including its port, outside the application domain' ''
    prompt tls_cert_file 'Absolute path to the PEM certificate chain' ''
    prompt tls_key_file 'Absolute path to its private key, mode0600' ''
  else
    prompt dashboard_port 'Loopback dashboard port for the SSH tunnel' '3000'
    dashboard_origin="http://localhost:$dashboard_port"; tls_cert_file=; tls_key_file=
  fi
  prompt acme 'Application certificates: production (public DNS/port80 required), staging, or off' 'production'
  acme_email=
  if [ "$acme" != off ]; then prompt acme_email 'ACME account contact email (does not create your Hakopod account)' ''; fi
  if [ "$deployment_mode" = self-hosted ]; then
    prompt public_tcp_ports 'Public TCP ports to provision, comma-separated (up to 256); leave empty to disable' ''
  else
    printf 'Managed-cloud mode keeps TCP service ports private; public TCP is unavailable.\n' >&2
  fi
  prompt database_mode 'PostgreSQL: managed K3s pod, existing local, or external/RDS' 'managed'
  database_url_file='' database_ca_file=''
  case "$database_mode" in
    managed) ;;
    local|external)
      prompt database_url_file 'Protected PostgreSQL URL file (mode 0600), or leave empty for hidden input' ''
      if [ -z "$database_url_file" ]; then
        database_input_tmp=$(mktemp "${TMPDIR:-/tmp}/hakopod-database.XXXXXXXX")
        printf 'PostgreSQL URL (hidden): ' >&2
        IFS= read -r -s database_url || die 'Database URL input ended'
        printf '\n' >&2
        printf '%s\n' "$database_url" > "$database_input_tmp"
        unset database_url
        database_url_file=$database_input_tmp
      fi
      prompt database_ca_file 'Optional absolute PEM CA bundle path for verified TLS' '';;
    *) die 'database_mode must be managed, local or external';;
  esac
  prompt install_docker 'Also install Docker Engine (K3s already includes containerd): true or false' 'false'
  prompt storage 'Enable optional node-local application volumes: true or false' 'false'
  prompt k3s_memory_mib 'K3s hard memory cap in MiB' '2048'
  prompt api_memory_mib 'API hard memory cap in MiB' '256'
  prompt dashboard_memory_mib 'Dashboard hard memory cap in MiB' '320'
  prompt postgres_memory_mib 'PostgreSQL hard memory cap in MiB' '256'
  prompt max_pods 'Maximum pods on this node' '50'
  input_tmp=$(mktemp "${TMPDIR:-/tmp}/hakopod-input.XXXXXXXX")
  python3 - "$input_tmp" "$version" "$app_domain" "$node_ip" "$node_name" "$supervisor_host" \
    "$dashboard_mode" "$dashboard_origin" "$dashboard_port" "$tls_cert_file" "$tls_key_file" \
    "$acme" "$acme_email" "$storage" "$k3s_memory_mib" "$api_memory_mib" "$dashboard_memory_mib" \
    "$postgres_memory_mib" "$max_pods" "$public_tcp_ports" "$deployment_mode" \
    "$database_mode" "$database_url_file" "$database_ca_file" "$install_docker" <<'PY'
import json, sys
from pathlib import Path
keys='version app_domain node_ip node_name supervisor_host dashboard_mode dashboard_origin dashboard_port tls_cert_file tls_key_file acme acme_email storage k3s_memory_mib api_memory_mib dashboard_memory_mib postgres_memory_mib max_pods public_tcp_ports deployment_mode database_mode database_url_file database_ca_file install_docker'.split()
c=dict(zip(keys,sys.argv[2:]),schema_version=1)
for key in ('dashboard_port','k3s_memory_mib','api_memory_mib','dashboard_memory_mib','postgres_memory_mib','max_pods'): c[key]=int(c[key])
for key in ('storage', 'install_docker'):
    if c[key] not in ('true', 'false'): raise SystemExit(key + ' must be true or false')
    c[key] = c[key] == 'true'
c['public_tcp_ports']=[int(port.strip()) for port in c['public_tcp_ports'].split(',')] if c['public_tcp_ports'] else []
Path(sys.argv[1]).write_text(json.dumps(c)+'\n')
PY
  config_path=$input_tmp
  oauth_input_dir=$(mktemp -d "${TMPDIR:-/tmp}/hakopod-oauth.XXXXXXXX")
  python3 "$bundle_root/installer/oauth.py" --config "$config_path" --directory "$oauth_input_dir"
fi
if [ -n "$release_version" ]; then
  python3 - "$config_path" "$release_version" <<'PY'
import json, sys
from pathlib import Path
if Path(sys.argv[1]).stat().st_size > 65536: raise SystemExit('Configuration exceeds 64 KiB')
with open(sys.argv[1]) as file: config = json.load(file)
if config.get('version', '0.1.0-dev') != sys.argv[2]: raise SystemExit('Installer version does not match configuration')
PY
fi
args=(--config "$config_path" --artifact-dir "$artifact_dir")
if [ -n "$target_arch" ]; then args+=(--arch "$target_arch"); fi
if "$resume"; then args+=(--resume); fi
python3 "$helper" plan "${args[@]}"
python3 "$bundle_root/installer/prerequisites.py" plan --config "$config_path"
if "$dry_run"; then exit 0; fi
# Reject unsupported hosts before package installation; then confirm the one plan.
python3 "$helper" platform-preflight "${args[@]}"
if ! "$assume_yes"; then
  [ -t 0 ] || die 'Read the plan, then pass --yes for an unattended install'
  printf 'Type install to accept this concrete plan: ' >&2
  IFS= read -r accepted
  [ "$accepted" = install ] || die 'Installation cancelled without host changes'
fi
python3 "$bundle_root/installer/prerequisites.py" install --config "$config_path"
# Full preflight runs after missing host tools become available.
python3 "$helper" preflight "${args[@]}"
exec 9>/run/lock/hakopod-install.lock
flock -n 9 || die 'Another Hakopod installer is running'
# Recheck under the lock, so concurrent installers cannot both claim a fresh host.
python3 "$helper" preflight "${args[@]}"
installation=$(python3 "$helper" prepare "${args[@]}")
# Marker and directories are root-owned; fail if manual edits introduced symlinks.
python3 - <<'PY'
from pathlib import Path
for name in ('/opt/hakopod/tools','/opt/hakopod/releases','/var/lib/hakopod/downloads',
             '/var/lib/hakopod/install-stage','/var/lib/hakopod/postgres','/var/lib/hakopod/backups','/etc/hakopod/secrets'):
    path=Path(name)
    if path.is_symlink(): raise SystemExit('Refusing unexpected symlink: ' + name)
PY
# Populated by the validated allowlist below; these are not shell-evaluated values.
cfg_version='' cfg_dashboard_mode='' cfg_tls_cert_file='' cfg_tls_key_file='' cfg_node_name=''
cfg_database_mode='' cfg_acme='' cfg_storage='' cfg_dashboard_port='' cfg_dashboard_origin='' cfg_node_ip='' cfg_supervisor_host=''
while IFS=$'\t' read -r key value; do printf -v "cfg_$key" '%s' "$value"; done < <(python3 "$helper" config "${args[@]}")
if [ -z "$target_arch" ]; then
  case "$(uname -m)" in x86_64) target_arch=amd64;; aarch64|arm64) target_arch=arm64;; *) die 'Unsupported architecture';; esac
fi
tools_dir=/opt/hakopod/tools
cache=/var/lib/hakopod/downloads
stage=/var/lib/hakopod/install-stage
install -d -m 0700 "$cache" "$stage"
install -d -m 0755 "$tools_dir"
download() {
  local name=$1 output=$2 url expected actual
  IFS=$'\t' read -r url expected < <(python3 "$helper" pin --name "$name" --arch "$target_arch")
  if [ ! -f "$output" ]; then
    printf 'Downloading pinned %s for Linux/%s\n' "$name" "$target_arch"
    curl --fail --location --proto '=https' --tlsv1.2 --retry 3 --max-time 300 --silent --show-error \
      "$url" --output "$output.partial"
    actual=$(sha256sum "$output.partial"); actual=${actual%% *}
    [ "$actual" = "$expected" ] || die "$name download checksum mismatch"
    mv -- "$output.partial" "$output"
  fi
  [ ! -L "$output" ] || die 'Refusing a symlink in download cache'
  actual=$(sha256sum "$output"); actual=${actual%% *}
  [ "$actual" = "$expected" ] || die "$name cached checksum mismatch; remove only that corrupt cache file and resume"
}
download k3s "$cache/k3s-$target_arch"
download helm "$cache/helm-$target_arch.tar.gz"
download node "$cache/node-$target_arch.tar.gz"
extract() {
  local source=$1 destination=$2 root=$3
  # All destinations are fixed paths beneath this root-owned installer stage.
  if [ -d "$destination" ]; then rm -rf -- "$destination"; fi
  python3 "$helper" unpack --source "$source" --destination "$destination" --root "$root"
}
extract "$cache/helm-$target_arch.tar.gz" "$stage/helm" "linux-$target_arch"
node_arch=$target_arch
[ "$target_arch" != amd64 ] || node_arch=x64
extract "$cache/node-$target_arch.tar.gz" "$stage/node" "node-v24.21.0-linux-$node_arch"
install_atomic() {
  local source=$1 destination=$2
  [ ! -L "$destination" ] || die "Refusing tool symlink $destination"
  if [ -f "$destination" ] && cmp -s "$source" "$destination"; then return; fi
  install -m 0755 "$source" "$destination.new"
  mv -f -- "$destination.new" "$destination"
}
install_atomic "$cache/k3s-$target_arch" "$tools_dir/k3s"
install_atomic "$stage/helm/linux-$target_arch/helm" "$tools_dir/helm"
if [ ! -d "$tools_dir/node" ]; then
  mv "$stage/node/node-v24.21.0-linux-$node_arch" "$tools_dir/node"
elif ! cmp -s "$stage/node/node-v24.21.0-linux-$node_arch/bin/node" "$tools_dir/node/bin/node"; then
  die 'Installed Node differs from the pinned artifact'
fi
# Only expose kubectl through the pinned dedicated K3s; do not replace system tools.
ln -sfn k3s "$tools_dir/kubectl"
export PATH="$tools_dir:$PATH" GOMAXPROCS=2 GOMEMLIMIT=256MiB
release="hakopod_${cfg_version}_linux_${target_arch}"
dashboard="hakopod_${cfg_version}_dashboard"
extract "$artifact_dir/$release.tar.gz" "$stage/server" "$release"
extract "$artifact_dir/$dashboard.tar.gz" "$stage/dashboard" "$dashboard"
[ "$("$stage/server/$release/hakopod" version)" = "$cfg_version" ] || die 'CLI release version does not match input'
[ -f "$stage/dashboard/$dashboard/dist/server/server.js" ] || die 'Missing built dashboard'
[ -f "$stage/dashboard/$dashboard/serve.mjs" ] || die 'Missing dashboard runtime launcher'
destination="/opt/hakopod/releases/$cfg_version"
if [ ! -d "$destination" ]; then
  install -d -m 0755 /opt/hakopod/releases
  mv "$stage/dashboard/$dashboard" "$stage/server/$release/dashboard"
  mv "$stage/server/$release" "$destination"
fi
if [ -e /opt/hakopod/current ] && [ ! -L /opt/hakopod/current ]; then die '/opt/hakopod/current must be the installer-owned version symlink'; fi
ln -sfn "releases/$cfg_version" /opt/hakopod/current
for account in hakopod-api hakopod-dashboard; do
  if ! getent passwd "$account" >/dev/null; then useradd --system --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin "$account"; fi
  [ "$(id -u "$account")" != 0 ] || die 'A dedicated service user cannot be root'
done
install -d -m 0700 -o hakopod-api -g hakopod-api /var/lib/hakopod/backups
rendered="$stage/rendered"
python3 "$helper" render "${args[@]}" --id "$installation" --destination "$rendered"
for file in k3s.yaml api.env dashboard.env; do install -m 0600 "$rendered/$file" "/etc/hakopod/$file"; done
for unit in hakopod-k3s hakopod-api hakopod-dashboard; do
  unit_path="/etc/systemd/system/$unit.service"
  if [ -e "$unit_path" ]; then
    [ ! -L "$unit_path" ] || die "Refusing symlink $unit_path"
    IFS= read -r first_line < "$unit_path"
    [ "$first_line" = "# Hakopod installation $installation" ] || die "Refusing unrelated $unit_path"
  fi
  install -m 0644 "$rendered/$unit.service" "$unit_path"
done
chown hakopod-api:hakopod-api /etc/hakopod/secrets/setup-token /etc/hakopod/secrets/auth-encryption-key /etc/hakopod/secrets/database-url
chmod 0400 /etc/hakopod/secrets/setup-token /etc/hakopod/secrets/auth-encryption-key /etc/hakopod/secrets/database-url
for provider in google github gitlab; do
  oauth_secret="/etc/hakopod/secrets/oauth-$provider-secret"
  if [ -f "$oauth_secret" ]; then
    [ ! -L "$oauth_secret" ] || die 'Refusing OAuth secret symlink'
    chown hakopod-api:hakopod-api "$oauth_secret"
    chmod 0400 "$oauth_secret"
  fi
done
if [ "$cfg_dashboard_mode" = https ]; then
  # Resuming preserves the installed certificate, including an operator renewal.
  if ! "$resume" || [ ! -f /etc/hakopod/dashboard.crt ] || [ ! -f /etc/hakopod/dashboard.key ]; then
    install -m 0400 -o hakopod-dashboard -g hakopod-dashboard "$cfg_tls_cert_file" /etc/hakopod/dashboard.crt
    install -m 0400 -o hakopod-dashboard -g hakopod-dashboard "$cfg_tls_key_file" /etc/hakopod/dashboard.key
  fi
fi
if [ "$cfg_database_mode" = managed ]; then chown 70:70 /var/lib/hakopod/postgres; fi
systemctl daemon-reload
systemctl enable --now hakopod-k3s
export KUBECONFIG=/etc/hakopod/admin-kubeconfig
ready=false
for ((attempt=0; attempt<60; attempt++)); do
  if kubectl --request-timeout=5s get --raw /readyz >/dev/null 2>&1; then ready=true; break; fi
  sleep 2
done
"$ready" || die 'K3s is not ready. Inspect journalctl -u hakopod-k3s, fix the cause, then resume'
install -m 0400 -o hakopod-api -g hakopod-api "$KUBECONFIG" /etc/hakopod/api-kubeconfig
# API readiness can precede kubelet registration on a fresh server.
kubectl wait --for=create "node/$cfg_node_name" --timeout=180s
kubectl wait --for=condition=Ready "node/$cfg_node_name" --timeout=180s
owned() {
  local resource=$1 name=$2 namespace=${3:-}
  local query=(get "$resource" "$name" --ignore-not-found -o json)
  if [ -n "$namespace" ]; then query+=(-n "$namespace"); fi
  kubectl "${query[@]}" > "$stage/object.json"
  if [ -s "$stage/object.json" ]; then python3 "$helper" owned --source "$stage/object.json" --id "$installation"; fi
}
if [ "$cfg_database_mode" = managed ]; then
  owned namespace hakopod-system
  owned persistentvolume hakopod-postgres
  for kind_name in secret/postgres service/postgres persistentvolumeclaim/postgres deployment/postgres networkpolicy/postgres-private; do
    owned "${kind_name%/*}" "${kind_name#*/}" hakopod-system
  done
  kubectl apply --server-side --field-manager=hakopod-installer -f "$rendered/postgres.json" >/dev/null
  kubectl -n hakopod-system rollout status deployment/postgres --timeout=180s
fi
# Namespace ownership is checked before Helm; Helm also refuses foreign releases.
ensure_namespace() {
  local namespace=$1
  owned namespace "$namespace"
  kubectl create namespace "$namespace" --dry-run=client -o json > "$stage/namespace.json"
  python3 - "$stage/namespace.json" "$installation" <<'PY'
import json,sys
from pathlib import Path
p=Path(sys.argv[1]);j=json.loads(p.read_text());j['metadata']['labels']={'hakopod.com/installation':sys.argv[2]};p.write_text(json.dumps(j))
PY
  kubectl apply --server-side --field-manager=hakopod-installer -f "$stage/namespace.json" >/dev/null
}
ensure_namespace haproxy-controller
helm upgrade --install hakopod-ingress "$bundle_root/deploy/charts/hakopod-platform" \
  --namespace haproxy-controller --values "$rendered/haproxy.json" --wait --timeout 3m
if [ "$cfg_acme" != off ]; then
  owned namespace cert-manager
  owned clusterissuer hakopod-acme
  kubectl create namespace cert-manager --dry-run=client -o json > "$stage/namespace.json"
  python3 - "$stage/namespace.json" "$installation" <<'PY'
import json,sys
from pathlib import Path
p=Path(sys.argv[1]);j=json.loads(p.read_text());j['metadata']['labels']={'hakopod.com/installation':sys.argv[2]};p.write_text(json.dumps(j))
PY
  kubectl apply --server-side --field-manager=hakopod-installer -f "$stage/namespace.json" >/dev/null
  crd_owner=$(kubectl get crd certificates.cert-manager.io --ignore-not-found -o 'jsonpath={.metadata.annotations.meta\.helm\.sh/release-name}')
  if [ -n "$crd_owner" ] && [ "$crd_owner" != hakopod-cert-manager ]; then die 'Existing cert-manager belongs to another release'; fi
  (cd "$bundle_root/deploy/cert-manager" && sha256sum --check checksums.sha256)
  helm upgrade --install hakopod-cert-manager "$bundle_root/deploy/cert-manager/cert-manager-v1.21.2.tgz" \
    --namespace cert-manager --values "$bundle_root/deploy/cert-manager/values.yaml" --wait --timeout 3m
  kubectl wait --for=condition=Established crd/clusterissuers.cert-manager.io --timeout=30s
  kubectl apply --server-side --field-manager=hakopod-installer -f "$rendered/issuer.json" >/dev/null
  kubectl wait --for=condition=Ready clusterissuer/hakopod-acme --timeout=90s
fi
if [ "$cfg_storage" = true ]; then
  # Convert the pinned module to JSON, stamp every object, and relocate its owned data.
  kubectl create --dry-run=client -f "$bundle_root/deploy/storage/local-path.yaml" -o json > "$stage/storage.json"
  python3 - "$stage/storage.json" "$installation" > "$stage/storage-objects.tsv" <<'PY'
import json,sys
from pathlib import Path
p=Path(sys.argv[1]);j=json.loads(p.read_text())
for item in j['items']:
    m=item['metadata'];m.setdefault('labels',{})['hakopod.com/installation']=sys.argv[2]
    print(item['kind']+'\t'+m['name']+'\t'+m.get('namespace',''))
    if item['kind']=='ConfigMap': item['data']['config.json']=item['data']['config.json'].replace('/opt/local-path-provisioner','/var/lib/hakopod/application-volumes')
p.write_text(json.dumps(j))
PY
  while IFS=$'\t' read -r kind name namespace; do owned "$kind" "$name" "$namespace"; done < "$stage/storage-objects.tsv"
  kubectl apply --server-side --field-manager=hakopod-installer -f "$stage/storage.json" >/dev/null
  kubectl -n hakopod-storage rollout status deployment/local-path-provisioner --timeout=120s
fi
systemctl enable --now hakopod-api hakopod-dashboard
healthy=false
for ((attempt=0; attempt<30; attempt++)); do
  if curl --silent --fail --noproxy '*' --max-time 3 http://127.0.0.1:8080/healthz >/dev/null; then healthy=true; break; fi
  sleep 2
done
"$healthy" || die 'Management API did not become healthy. Inspect journalctl -u hakopod-api, then resume'
if [ "$cfg_dashboard_mode" = ssh ]; then
  curl --silent --fail --noproxy '*' --max-time 15 "http://127.0.0.1:$cfg_dashboard_port/" >/dev/null
else
  dashboard_host=$(python3 -c 'import sys,urllib.parse; print(urllib.parse.urlsplit(sys.argv[1]).hostname)' "$cfg_dashboard_origin")
  curl --silent --fail --noproxy '*' --max-time 15 --resolve "$dashboard_host:$cfg_dashboard_port:$cfg_node_ip" "$cfg_dashboard_origin/" >/dev/null
fi
python3 "$helper" complete
# Rendered environment/Secret documents are no longer needed. Retain downloads for resume.
rm -rf -- "$stage"
printf '\nHakopod is healthy. Dashboard: %s\n' "$cfg_dashboard_origin"
if [ "$cfg_dashboard_mode" = ssh ]; then
  printf 'Open an SSH tunnel: ssh -L %s:127.0.0.1:%s <your-ssh-user>@%s\n' "$cfg_dashboard_port" "$cfg_dashboard_port" "$cfg_supervisor_host"
fi
printf 'Read the setup token privately with: sudo cat /etc/hakopod/secrets/setup-token\n'
printf 'Enter that token in setup, then choose your own name, email and password. No account was created by the installer.\n'
printf 'Retain /etc/hakopod/config.json, the artifact checksums and backups. See installer/README.md for resume and recovery.\n'
