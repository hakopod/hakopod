#!/usr/bin/env bash
# Run only on an explicitly reserved Linux VM with a separate Docker daemon.
set -euo pipefail
[[ "$(uname -s)" == Linux && "${HAKOPOD_XEM_VM_ACCEPTANCE:-}" == 1 ]] || {
  echo 'Xem acceptance requires an explicitly reserved Linux VM.' >&2
  exit 1
}
: "${HAKOPOD_XEM_VM_ROOT:?Set the reserved scratch directory}"
: "${HAKOPOD_XEM_DOCKER_ROOT:?Set the isolated Docker data directory}"
: "${HAKOPOD_XEM_DOCKER_PID_FILE:?Set the isolated Docker daemon PID file}"
: "${DOCKER_HOST:?Set the isolated Docker socket}"
[[ -z "${DOCKER_CONTEXT:-}" ]] || { echo 'Unset DOCKER_CONTEXT before selecting the isolated socket.' >&2; exit 1; }
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GOMAXPROCS=2 GOMEMLIMIT=512MiB
umask 077

# Reject the host daemon and keep all generated files inside reserved scratch.
python3 - "$ROOT" <<'PY'
import os, pathlib, socket, sys
scratch = pathlib.Path(os.environ['HAKOPOD_XEM_VM_ROOT']).resolve(strict=True)
data = pathlib.Path(os.environ['HAKOPOD_XEM_DOCKER_ROOT']).resolve(strict=True)
pid_file = pathlib.Path(os.environ['HAKOPOD_XEM_DOCKER_PID_FILE']).resolve(strict=True)
endpoint = os.environ['DOCKER_HOST']
if scratch == pathlib.Path('/') or not endpoint.startswith('unix://'):
    raise SystemExit('A dedicated scratch directory and Unix Docker socket are required')
for path in (data, pid_file, pathlib.Path(sys.argv[1]).resolve(), pathlib.Path(endpoint[7:]).resolve(strict=True)):
    if not path.is_relative_to(scratch) or path == scratch:
        raise SystemExit('Docker data, socket and checkout must be inside the reserved scratch directory')
if endpoint in ('unix:///var/run/docker.sock', 'unix:///run/docker.sock'):
    raise SystemExit('The host Docker socket is forbidden')
pid = pid_file.read_text().strip()
if not pid.isdecimal() or int(pid) <= 1:
    raise SystemExit('The isolated Docker PID file is invalid')
netns = os.readlink('/proc/self/ns/net')
if netns == os.readlink('/proc/1/ns/net') or netns != os.readlink(f'/proc/{pid}/ns/net'):
    raise SystemExit('Run the harness and its Docker daemon in the same isolated network namespace')
available = int(next(line.split()[1] for line in pathlib.Path('/proc/meminfo').read_text().splitlines() if line.startswith('MemAvailable:'))) * 1024
if available < 7 * 1024**3:
    raise SystemExit('Reserve at least 7 GiB of available VM memory before acceptance')
print(f'VM available memory: {available // 1024**2} MiB')
with socket.socket() as probe:
    probe.bind(('127.0.0.1', 16443))
PY
export GOCACHE="$HAKOPOD_XEM_VM_ROOT/cache/go-build"
export GOMODCACHE="$HAKOPOD_XEM_VM_ROOT/cache/go-mod"
export GOPATH="$HAKOPOD_XEM_VM_ROOT/go"
export GOTMPDIR="$HAKOPOD_XEM_VM_ROOT/tmp" TMPDIR="$HAKOPOD_XEM_VM_ROOT/tmp"
export DOCKER_CONFIG="$HAKOPOD_XEM_VM_ROOT/docker-client"
export XDG_CONFIG_HOME="$HAKOPOD_XEM_VM_ROOT/k3d-config"
export GOOS=linux GOFLAGS='' GOENV=off GOTOOLCHAIN=auto
case "$(uname -m)" in
  x86_64) export GOARCH=amd64 ;;
  aarch64|arm64) export GOARCH=arm64 ;;
  *) echo 'Native Xem acceptance supports only AMD64 and ARM64.' >&2; exit 1 ;;
esac
mkdir -p "$GOCACHE" "$GOMODCACHE" "$GOPATH" "$GOTMPDIR" "$DOCKER_CONFIG" "$XDG_CONFIG_HOME"
[[ ! -e "$DOCKER_CONFIG/config.json" ]] || {
  echo 'The acceptance Docker client must not have registry credentials.' >&2
  exit 1
}
for tool in docker go curl python3; do
  command -v "$tool" >/dev/null || { echo "Required VM tool missing: $tool" >&2; exit 1; }
done
actual_root=$(docker info --format '{{.DockerRootDir}}')
[[ "$(realpath "$actual_root")" == "$(realpath "$HAKOPOD_XEM_DOCKER_ROOT")" ]] || {
  echo 'The Docker daemon does not use the reserved data directory.' >&2
  exit 1
}
[[ -z "$(docker ps -aq)" ]] || { echo 'The isolated Docker daemon already has containers; refusing reuse.' >&2; exit 1; }
if docker network inspect k3d-hakopod-dev >/dev/null 2>&1; then
  echo 'The isolated daemon retains a development network; inspect its previous cleanup first.' >&2
  exit 1
fi
if docker volume inspect k3d-hakopod-dev-images >/dev/null 2>&1; then
  echo 'The isolated daemon retains a development image volume; inspect its previous cleanup first.' >&2
  exit 1
fi
[[ -z "$(docker volume ls -q)" ]] || {
  echo 'The isolated daemon retains Docker volumes; inspect their ownership and previous cleanup first.' >&2
  exit 1
}
check_disk() {
python3 - "$actual_root" <<'PY'
import shutil, sys
free = shutil.disk_usage(sys.argv[1]).free
print(f'Isolated Docker filesystem free space: {free // 1024**3} GiB')
if free < 35 * 1024**3:
    raise SystemExit('Keep at least 35 GiB of free shared disk before acceptance startup')
PY
}
check_disk
source "$ROOT/deploy/local/versions.env"
"$ROOT/scripts/local-tools.sh"
K3D="$ROOT/.local/bin/k3d"
case "$GOARCH" in
  amd64) kubectl_sha=874d5e72dbb819f43cff16bcd1e4f8bac5b7f2361fe1e55049b0a6c676fb0cbf ;;
  arm64) kubectl_sha=cc749967b62f4422260bc9c0aa7a7c55f45175ae38cb8d95767b5d2b7e04c1fd ;;
esac
curl --fail --silent --show-error --location --retry 3 \
  "https://dl.k8s.io/release/v1.35.8/bin/linux/$GOARCH/kubectl" \
  -o "$ROOT/.local/downloads/kubectl"
python3 - "$ROOT" "$kubectl_sha" <<'PY'
import hashlib, pathlib, sys
root = pathlib.Path(sys.argv[1])
source = root / '.local/downloads/kubectl'
if hashlib.sha256(source.read_bytes()).hexdigest() != sys.argv[2]:
    raise SystemExit('kubectl checksum mismatch; refusing to install')
target = root / '.local/bin/kubectl'
source.replace(target)
target.chmod(0o755)
PY
export PATH="$ROOT/.local/bin:$PATH"
run_id="xem-acceptance-$(date +%s)-$$"
results="$ROOT/.local/xem-runs/$run_id"
mkdir -p "$results"
export HAKOPOD_TEST_KUBECONFIG="$results/kubeconfig"
# k3d cluster deletion edits its default kubeconfig even when creation was
# configured not to merge it. Isolate both of k3d's configuration locations.
export KUBECONFIG="$HAKOPOD_TEST_KUBECONFIG"
[[ "$("$K3D" cluster list -o json)" == '[]' ]] || {
  echo 'The isolated Docker daemon already has a k3d cluster; refusing reuse.' >&2
  exit 1
}
export HAKOPOD_XEM_TEST=1 HAKOPOD_XEM_TEST_POOL="$run_id"
export HAKOPOD_XEM_TEST_NODE="k3d-$run_id-0"
case "${HAKOPOD_XEM_TEST_CASES:-all}" in
  all) test_filter='^TestLiveXemTemplate$' ;;
  external-dependencies) test_filter='^TestLiveXemTemplate$/^external-postgres_external-redis_' ;;
  *) echo 'HAKOPOD_XEM_TEST_CASES must be all or external-dependencies.' >&2; exit 1 ;;
esac
printf 'cases=%s\nfilter=%s\n' "${HAKOPOD_XEM_TEST_CASES:-all}" "$test_filter" | tee "$results/test-selection.txt"
unset HAKOPOD_XEM_BACKEND_IMAGE HAKOPOD_XEM_FRONTEND_IMAGE HAKOPOD_XEM_BROWSER_EVIDENCE_FILE
git -C "$ROOT" rev-parse HEAD | tee "$results/product-commit.txt"
git -C "$ROOT/templates" rev-parse HEAD | tee "$results/catalog-commit.txt"
git -C "$ROOT" status --short > "$results/source-changes.txt"
git -C "$ROOT/templates" status --short > "$results/catalog-changes.txt"
uname -m | tee "$results/architecture.txt"
go env GOOS GOARCH | tee "$results/go-target.txt"
# Hash actual tracked and untracked source bytes, including dirty files, without
# printing their contents. This identifies a copied working tree precisely.
python3 - "$ROOT" "$results/source-manifest.json" <<'PY'
import hashlib, json, os, pathlib, subprocess, sys
root, output = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
manifest = {}
for prefix in ('', 'templates'):
    checkout = root / prefix
    names = subprocess.check_output(['git', '-C', str(checkout), 'ls-files', '-z', '--cached', '--others', '--exclude-standard']).split(b'\0')
    for raw in sorted(set(names)):
        if not raw:
            continue
        name = os.fsdecode(raw)
        path = checkout / name
        if path.is_dir():
            continue
        key = str(path.relative_to(root))
        if path.is_symlink():
            manifest[key] = {'symlink': os.readlink(path)}
        elif path.exists():
            manifest[key] = {'sha256': hashlib.sha256(path.read_bytes()).hexdigest(), 'mode': oct(path.stat().st_mode & 0o777)}
        else:
            manifest[key] = {'deleted': True}
output.write_text(json.dumps(manifest, sort_keys=True, indent=2) + '\n')
print('Source manifest SHA256: ' + hashlib.sha256(output.read_bytes()).hexdigest())
PY

server_id=''
worker_id=''
tools_id=''
image_volume_created=''
cluster_started=0
child_pid=''
child_reaped=0
child_output=''
log_pid=''
launching_child=0
signal_status=0
interrupt_run() {
  signal_status=$1
  # Finish recording newly launched PIDs before the EXIT trap can reap them.
  [[ "$launching_child" == 1 ]] || exit "$signal_status"
}
stop_child() {
  [[ -n "$child_pid" ]] || return 0
  # Each launcher becomes its child's session leader. Signal the group to
  # stop compiler workers or port forwards too, and the PID before setsid.
  kill -TERM -- "-$child_pid" 2>/dev/null || true
  if [[ "$child_reaped" == 0 ]]; then
    kill -TERM -- "$child_pid" 2>/dev/null || true
    for ((attempt = 0; attempt < 10; attempt++)); do
      kill -0 -- "$child_pid" 2>/dev/null || break
      sleep 1
    done
    kill -KILL -- "$child_pid" 2>/dev/null || true
  fi
  kill -KILL -- "-$child_pid" 2>/dev/null || true
  [[ "$child_reaped" == 1 ]] || wait "$child_pid" 2>/dev/null || true
  child_pid=''
}
run_child() {
  local workdir=$1
  shift
  local child_status=0
  local -a launcher=(python3 -c 'import os, sys; os.setsid(); os.chdir(sys.argv[1]); os.execvp(sys.argv[2], sys.argv[2:])' "$workdir" "$@")
  launching_child=1
  if [[ -n "$child_output" ]]; then
    "${launcher[@]}" > "$child_output" 2>&1 &
  else
    "${launcher[@]}" &
  fi
  child_pid=$!
  child_reaped=0
  launching_child=0
  [[ "$signal_status" == 0 ]] || exit "$signal_status"
  # Builtin wait lets INT/TERM interrupt setup and tests immediately.
  wait "$child_pid" || child_status=$?
  child_reaped=1
  stop_child
  return "$child_status"
}
kube() {
  [[ "$(kubectl --kubeconfig "$HAKOPOD_TEST_KUBECONFIG" config current-context)" == k3d-hakopod-dev ]] || return 1
  run_child "$ROOT" kubectl --kubeconfig "$HAKOPOD_TEST_KUBECONFIG" --context k3d-hakopod-dev --request-timeout=30s "$@"
}
cleanup() {
  status=$?
  trap - EXIT
  # systemd should use KillMode=mixed and TimeoutStopSec=300. Keep its Docker
  # daemon in a separate unit so ownership cleanup still has a working API.
  trap '' INT TERM
  stop_child
  child_output=''
  if [[ -n "$log_pid" ]]; then
    kill -TERM -- "$log_pid" 2>/dev/null || true
    wait "$log_pid" 2>/dev/null || true
    log_pid=''
  fi
  if [[ "$signal_status" != 0 ]]; then
    printf 'Interrupted with status %s; child processes stopped before ownership cleanup.\n' "$signal_status" > "$results/interruption.txt"
  fi
  signal_status=0
  rm -f "$results/runtime.pipe"
  if [[ "$cluster_started" == 1 ]]; then
    cleanup_ok=1
    # Every container is in a newly created, otherwise empty daemon. Still
    # require this invocation's ownership labels and recorded immutable IDs.
    for container_id in $(docker ps -aq --no-trunc --filter label=k3d.cluster=hakopod-dev); do
      if [[ -n "$tools_id" && "$container_id" == "$tools_id" ]]; then
        [[ "$(docker inspect -f '{{.Name}}/{{index .Config.Labels "app"}}/{{index .Config.Labels "k3d.cluster"}}' "$tools_id")" == /k3d-hakopod-dev-tools/k3d/hakopod-dev ]] || cleanup_ok=0
        continue
      fi
      if [[ "$(docker inspect -f '{{index .Config.Labels "com.hakopod.acceptance-id"}}' "$container_id")" != "$run_id" ]]; then
        echo 'Cleanup refused: development container ownership changed.' >&2
        cleanup_ok=0
      fi
    done
    for container_id in "$server_id" "$worker_id"; do
      [[ -z "$container_id" ]] && continue
      if [[ "$(docker inspect -f '{{index .Config.Labels "com.hakopod.acceptance-id"}}' "$container_id" 2>/dev/null)" != "$run_id" ]]; then
        echo 'Cleanup refused: a recorded container no longer matches.' >&2
        cleanup_ok=0
      fi
    done
    if [[ -n "${HAKOPOD_XEM_TEST_NODE_UID:-}" ]]; then
      observed_uid=$(kube get node "$HAKOPOD_XEM_TEST_NODE" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)
      if [[ -n "$observed_uid" && "$observed_uid" != "$HAKOPOD_XEM_TEST_NODE_UID" ]]; then
        echo 'Cleanup refused: reserved node UID changed.' >&2
        cleanup_ok=0
      fi
    fi
    if [[ -n "$image_volume_created" ]]; then
      if [[ "$(docker volume inspect -f '{{.CreatedAt}}/{{index .Labels "k3d.cluster"}}' k3d-hakopod-dev-images 2>/dev/null)" != "$image_volume_created/hakopod-dev" ]]; then
        echo 'Cleanup refused: development image-volume identity changed.' >&2
        cleanup_ok=0
      fi
    fi
    if [[ "$cleanup_ok" == 1 ]]; then
      "$K3D" cluster delete hakopod-dev || cleanup_ok=0
      [[ -z "$(docker ps -aq --filter label=k3d.cluster=hakopod-dev)" ]] || cleanup_ok=0
      # k3d can leave an unlabeled image-inspection helper when node creation
      # fails. Retain it for an exact-ID review and report cleanup honestly.
      [[ -z "$(docker ps -aq)" ]] || cleanup_ok=0
      if docker network inspect k3d-hakopod-dev >/dev/null 2>&1; then
        cleanup_ok=0
      fi
      if docker volume inspect k3d-hakopod-dev-images >/dev/null 2>&1; then
        cleanup_ok=0
      fi
      [[ -z "$(docker volume ls -q)" ]] || cleanup_ok=0
    fi
    if [[ "$cleanup_ok" == 1 ]]; then
      echo 'Owned disposable development cluster and workers removed.' | tee "$results/cleanup.txt"
      rm -f "$HAKOPOD_TEST_KUBECONFIG"
    else
      echo 'Cleanup incomplete; retain the isolated daemon for ownership review.' | tee "$results/cleanup.txt" >&2
      status=1
    fi
  fi
  rm -f "$results/cluster.test"
  echo "Acceptance evidence: $results"
  exit "$status"
}
trap cleanup EXIT
trap 'interrupt_run 130' INT
trap 'interrupt_run 143' TERM
# Compile before reserving cluster memory. This binary runs from the package
# directory so the suite reads the exact catalog checked out beside the source.
run_child "$ROOT" go test -p=2 -c ./internal/cluster -o "$results/cluster.test"
check_disk
cluster_started=1
run_child "$ROOT" "$K3D" cluster create hakopod-dev --servers 1 --agents 0 \
  --image "$K3S_IMAGE" --servers-memory 1536m --no-lb \
  --api-port 127.0.0.1:16443 \
  --runtime-label "com.hakopod.acceptance-id=$run_id@server:*" \
  --k3s-arg '--disable=traefik,servicelb,local-storage,metrics-server@server:*' \
  --k3s-arg '--secrets-encryption@server:*' \
  --k3s-arg '--kubelet-arg=max-pods=20@server:*' \
  --kubeconfig-update-default=false --kubeconfig-switch-context=false \
  --wait --timeout 240s
server_id=$(docker inspect -f '{{.Id}}' k3d-hakopod-dev-server-0)
tools_id=$(docker inspect -f '{{.Id}}' k3d-hakopod-dev-tools 2>/dev/null || true)
if [[ -n "$tools_id" ]]; then
  [[ "$(docker inspect -f '{{.Name}}/{{index .Config.Labels "app"}}/{{index .Config.Labels "k3d.cluster"}}' "$tools_id")" == /k3d-hakopod-dev-tools/k3d/hakopod-dev ]]
  docker update --memory 128m --memory-swap 128m --cpus 0.25 "$tools_id" >/dev/null
fi
[[ "$(docker volume inspect -f '{{index .Labels "k3d.cluster"}}' k3d-hakopod-dev-images)" == hakopod-dev ]]
image_volume_created=$(docker volume inspect -f '{{.CreatedAt}}' k3d-hakopod-dev-images)
docker update --memory 1536m --memory-swap 1536m --cpus 1 "$server_id" >/dev/null
"$K3D" kubeconfig get hakopod-dev > "$HAKOPOD_TEST_KUBECONFIG"
run_child "$ROOT" "$K3D" node create "$run_id" --cluster hakopod-dev --role agent --memory 4096m \
  --runtime-label com.hakopod.acceptance=xem \
  --runtime-label "com.hakopod.acceptance-id=$run_id" \
  --k3s-node-label "hakopod.io/xem-acceptance=$run_id" \
  --k3s-arg "--node-taint=hakopod.io/xem-staging=$run_id:NoSchedule" \
  --k3s-arg '--kubelet-arg=max-pods=30' --wait --timeout 180s
worker_id=$(docker inspect -f '{{.Id}}' "$HAKOPOD_XEM_TEST_NODE")
docker update --memory 4096m --memory-swap 4096m --cpus 2 "$worker_id" >/dev/null
kube wait --for=condition=Ready "node/$HAKOPOD_XEM_TEST_NODE" --timeout=180s
export HAKOPOD_XEM_TEST_NODE_UID
HAKOPOD_XEM_TEST_NODE_UID=$(kube get node "$HAKOPOD_XEM_TEST_NODE" -o jsonpath='{.metadata.uid}')
[[ "$(kube get node "$HAKOPOD_XEM_TEST_NODE" -o jsonpath='{.status.nodeInfo.architecture}')" == "$GOARCH" ]] || {
  echo 'The reserved worker does not match the native Go target.' >&2
  exit 1
}
[[ "$(kube get node "$HAKOPOD_XEM_TEST_NODE" -o jsonpath='{.metadata.labels.hakopod\.io/xem-acceptance}')" == "$run_id" ]]
kube patch node "$HAKOPOD_XEM_TEST_NODE" --type=merge -p \
  "{\"metadata\":{\"labels\":{\"hakopod.com/pool\":\"$run_id\"}},\"spec\":{\"taints\":[{\"key\":\"hakopod.com/pool\",\"value\":\"$run_id\",\"effect\":\"NoSchedule\"}]}}"
printf '%s\n' "$run_id" "$server_id" "$worker_id" "$HAKOPOD_XEM_TEST_NODE_UID" "$tools_id" k3d-hakopod-dev-images "$image_volume_created" > "$results/ownership.txt"
kube apply -f "$ROOT/deploy/storage/local-path.yaml"
# The storage helper is restricted to the exact pool reserved for this run.
kube -n hakopod-storage get configmap local-path-config -o json > "$results/storage-config.json"
python3 - "$results/storage-config.json" "$run_id" > "$results/storage-patch.json" <<'PY'
import json, pathlib, sys
config = json.loads(pathlib.Path(sys.argv[1]).read_text())
helper = config['data']['helperPod.yaml']
helper = helper.replace('  tolerations:\n', '  tolerations:\n    - key: hakopod.com/pool\n      operator: Equal\n      value: '+sys.argv[2]+'\n      effect: NoSchedule\n', 1)
print(json.dumps({'data': {'helperPod.yaml': helper}}))
PY
kube -n hakopod-storage patch configmap local-path-config --type=merge --patch-file "$results/storage-patch.json"
kube -n hakopod-storage rollout status deployment/local-path-provisioner --timeout=120s
kube -n kube-system rollout status deployment/coredns --timeout=120s
kube get nodes -o wide
launching_child=1
mkfifo "$results/runtime.pipe"
tee "$results/runtime.log" < "$results/runtime.pipe" &
log_pid=$!
launching_child=0
[[ "$signal_status" == 0 ]] || exit "$signal_status"
test_status=0
child_output="$results/runtime.pipe"
run_child "$ROOT/internal/cluster" "$results/cluster.test" \
  -test.run "$test_filter" -test.count=1 -test.timeout=42m -test.v || test_status=$?
child_output=''
wait "$log_pid" || test_status=1
log_pid=''
rm -f "$results/runtime.pipe"
remaining_namespaces=$(kube get namespaces -l hakopod.io/xem-acceptance -o name)
remaining_volumes=$(kube get pv -o name)
if [[ -n "$remaining_namespaces$remaining_volumes" ]]; then
  echo 'The suite retained application namespaces or persistent volumes.' >&2
  printf '%s\n' "$remaining_namespaces" "$remaining_volumes"
  test_status=1
fi
exit "$test_status"
