#!/usr/bin/env bash
set -euo pipefail

readonly UPSTREAM_COMMIT="fa504217c61bbcaf5c512d75830564541f917f8f"
readonly PATCHED_TREE="8fcaa8600a9da41870d6f74478cfdfcd53fb2dd6"
readonly SOURCE_ARCHIVE_SHA256="0a364b86e81faefca56c7dcf56e2057767c63cb396d653775d7596fcd343e209"
readonly PROXY_PATCH_SHA256="e9a1df309106d166adfc0982500f6500df220dbc6173761c48c7c2038563fbd6"
readonly OWNERSHIP_PATCH_SHA256="a7d464f88f374480eedde78efa4c8ea4d12928fbc11104aea710e2b9691491f4"
readonly DEFAULT_MINIMUM_FREE_GIB=12
readonly BUILDER_MEMORY_BYTES=7516192768
readonly BUILDER_NAME="hakopod-neon-bounded"
readonly BUILDER_CONTAINER="hakopod-neon-buildkit"
readonly BUILDER_ADDRESS="tcp://127.0.0.1:12347"
readonly BUILDKIT_IMAGE="moby/buildkit@sha256:cec9f139f45e93c5c69c60f8b07cfad9f43f4ef6b6a6cd917527fea5ff2e3dea"

usage() {
  cat >&2 <<'EOF'
usage: build-native-images.sh SOURCE_ARCHIVE OUTPUT_DIRECTORY PHASE

PHASE is one of:
  storage         Build the image containing storage_controller, pageserver,
                  safekeeper, and proxy.
  compute-tools   Build the compute-tools stage only.
  compute-runtime Build the PG17 compute image with all upstream extensions.

Images remain in the VM-local Docker daemon. Run phases sequentially.
EOF
  exit 2
}

[[ $# == 3 ]] || usage
source_archive=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
[[ -f "$source_archive" ]] || usage
output_directory=$(mkdir -p "$2" && cd "$2" && pwd)
phase=$3
source_hash_before=$(sha256sum "$source_archive" | cut -d' ' -f1)
[[ "$source_hash_before" == "$SOURCE_ARCHIVE_SHA256" ]] || {
  echo "refusing build: source archive checksum mismatch" >&2
  exit 1
}
work_directory=$(mktemp -d "$output_directory/source.XXXXXXXX")
cleanup() {
  chmod -R u+w "$work_directory" 2>/dev/null || true
  rm -rf -- "$work_directory"
}
trap cleanup EXIT
tar -xzf "$source_archive" --strip-components=1 -C "$work_directory"
source_directory=$work_directory

require_disk_reserve() {
  local available_kib minimum_kib
  available_kib=$(df -Pk "$output_directory" | awk 'NR == 2 {print $4}')
  minimum_kib=$((DEFAULT_MINIMUM_FREE_GIB * 1024 * 1024))
  if (( available_kib < minimum_kib )); then
    echo "refusing build: less than ${DEFAULT_MINIMUM_FREE_GIB} GiB remains" >&2
    exit 1
  fi
}

build() {
  require_disk_reserve
  buildkit_root=${HAKOPOD_NEON_BUILDKIT_ROOT:-$output_directory/buildkit-root}
  buildkit_root=$(mkdir -p "$buildkit_root" && cd "$buildkit_root" && pwd)
  if ! sudo docker container inspect "$BUILDER_CONTAINER" >/dev/null 2>&1; then
    sudo docker run --detach --name "$BUILDER_CONTAINER" --privileged \
      --network host --memory "$BUILDER_MEMORY_BYTES" --memory-swap "$BUILDER_MEMORY_BYTES" --cpu-period 100000 --cpu-quota 100000 \
      --volume "$buildkit_root:/var/lib/buildkit" "$BUILDKIT_IMAGE" \
      --addr "$BUILDER_ADDRESS" >/dev/null
  fi
  if ! sudo docker buildx inspect "$BUILDER_NAME" >/dev/null 2>&1; then
    sudo docker buildx create --name "$BUILDER_NAME" --driver remote \
      "$BUILDER_ADDRESS" >/dev/null
  fi
  sudo docker buildx inspect --bootstrap "$BUILDER_NAME" >/dev/null
  read -r memory memory_swap cpu_period cpu_quota network_mode mounted_root < <(
    sudo docker inspect "$BUILDER_CONTAINER" \
      --format '{{.HostConfig.Memory}} {{.HostConfig.MemorySwap}} {{.HostConfig.CpuPeriod}} {{.HostConfig.CpuQuota}} {{.HostConfig.NetworkMode}} {{range .Mounts}}{{if eq .Destination "/var/lib/buildkit"}}{{.Source}}{{end}}{{end}}'
  )
  [[ "$memory" == "$BUILDER_MEMORY_BYTES" && "$memory_swap" == "$BUILDER_MEMORY_BYTES" && "$cpu_period" == 100000 && "$cpu_quota" == 100000 && \
    "$network_mode" == host && "$mounted_root" == "$buildkit_root" ]] || {
    echo "refusing build: $BUILDER_CONTAINER does not have the required resource or scratch-storage limits" >&2
    exit 1
  }
  sudo docker inspect "$BUILDER_CONTAINER" \
    | cat >"$output_directory/builder-inspect.json"
  sudo docker buildx build --builder "$BUILDER_NAME" --load --progress=plain \
    --metadata-file "$output_directory/${phase}-build-metadata.json" \
    --label "org.opencontainers.image.revision=$UPSTREAM_COMMIT" \
    --label "io.hakopod.neon.patched-tree=$PATCHED_TREE" \
    --label "io.hakopod.neon.source-archive-sha256=$SOURCE_ARCHIVE_SHA256" \
    --label "io.hakopod.neon.proxy-patch-sha256=$PROXY_PATCH_SHA256" \
    --label "io.hakopod.neon.ownership-patch-sha256=$OWNERSHIP_PATCH_SHA256" \
    "$@" &
  build_pid=$!
  while kill -0 "$build_pid" 2>/dev/null; do
    available_kib=$(df -Pk "$output_directory" | awk 'NR == 2 {print $4}')
    if (( available_kib < DEFAULT_MINIMUM_FREE_GIB * 1024 * 1024 )); then
      kill "$build_pid"
      wait "$build_pid" || true
      echo "cancelled build to preserve the ${DEFAULT_MINIMUM_FREE_GIB} GiB disk reserve" >&2
      exit 1
    fi
    sleep 10
  done
  wait "$build_pid"
  require_disk_reserve
}

case "$phase" in
  storage)
    image="hakopod/neon-storage:${UPSTREAM_COMMIT:0:8}-${PATCHED_TREE:0:8}"
    build --build-arg "GIT_VERSION=$UPSTREAM_COMMIT" --tag "$image" "$source_directory" \
      >"$output_directory/storage-build.log" 2>&1
    ;;
  compute-tools)
    image="hakopod/neon-compute-tools:${UPSTREAM_COMMIT:0:8}-${PATCHED_TREE:0:8}"
    build --file "$source_directory/compute/compute-node.Dockerfile" \
      --target compute-tools --build-arg PG_VERSION=v17 --tag "$image" "$source_directory" \
      >"$output_directory/compute-tools-build.log" 2>&1
    ;;
  compute-runtime)
    image="hakopod/neon-compute-v17:${UPSTREAM_COMMIT:0:8}-${PATCHED_TREE:0:8}"
    build --file "$source_directory/compute/compute-node.Dockerfile" \
      --build-arg PG_VERSION=v17 --build-arg EXTENSIONS=all --tag "$image" "$source_directory" \
      >"$output_directory/compute-runtime-build.log" 2>&1
    ;;
  *) usage ;;
esac

sudo docker image inspect "$image" | cat >"$output_directory/${phase}-image-inspect.json"
printf '%s\n' "$image" >"$output_directory/${phase}-image.txt"
source_hash_after=$(sha256sum "$source_archive" | cut -d' ' -f1)
[[ "$source_hash_after" == "$source_hash_before" ]]
image_config_digest=$(sudo docker image inspect "$image" --format '{{.Id}}')
image_manifest_digest=$(python3 - "$output_directory/${phase}-build-metadata.json" <<'PY'
import json
import sys
with open(sys.argv[1], encoding="utf-8") as stream:
    metadata = json.load(stream)
print(metadata.get("containerimage.digest", "unavailable"))
PY
)
cat >"$output_directory/${phase}-provenance.txt" <<EOF
upstream_commit=$UPSTREAM_COMMIT
patched_tree=$PATCHED_TREE
source_archive_sha256_before=$source_hash_before
source_archive_sha256_after=$source_hash_after
proxy_patch_sha256=$PROXY_PATCH_SHA256
ownership_patch_sha256=$OWNERSHIP_PATCH_SHA256
image=$image
image_manifest_digest=$image_manifest_digest
image_config_digest=$image_config_digest
published=false
cluster_qualified=false
EOF
echo "built locally: $image"
