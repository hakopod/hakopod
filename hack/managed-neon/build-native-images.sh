#!/usr/bin/env bash
set -euo pipefail

script_directory=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$script_directory/../.." && pwd)

readonly UPSTREAM_COMMIT="fa504217c61bbcaf5c512d75830564541f917f8f"
readonly PATCHED_TREE="0e9da216fbb78976bac267a8036809e12f58a1bd"
readonly POSTGRES_COMMIT="1e01fcea2a6b38180021aa83e0051d95286d9096"
readonly CONSUMER_PATCH_ID="627583b85d7c0f624245e3ac6a240e775a5eed01"
readonly SOURCE_ARCHIVE_SHA256="f91a9e1a5945dd1d2e634c2438d5c11052d4f2e993dc2d09c82f25d6eff995da"
readonly PROXY_PATCH_SHA256="e9a1df309106d166adfc0982500f6500df220dbc6173761c48c7c2038563fbd6"
readonly OWNERSHIP_PATCH_SHA256="a7d464f88f374480eedde78efa4c8ea4d12928fbc11104aea710e2b9691491f4"
readonly POSTGRES_PATCH_SHA256="47f90f03bc3aa0893b952049d86da54713843abdc94b74166a33c67bfb95a3e6"
readonly CONSUMER_PATCH_SHA256="f425b69c6d20153ea3fbba2633d53adc77007f4d84468423d3544bc5fff11e60"
readonly DEFAULT_MINIMUM_FREE_GIB=12
readonly BUILDER_MEMORY_BYTES="${HAKOPOD_NEON_BUILDER_MEMORY_BYTES:-7516192768}"
readonly BUILDER_CPU_QUOTA="${HAKOPOD_NEON_BUILDER_CPU_QUOTA:-100000}"
readonly BUILDER_MAX_PARALLELISM="${HAKOPOD_NEON_BUILDER_MAX_PARALLELISM:-1}"
readonly BUILDER_NAME="${HAKOPOD_NEON_BUILDER_NAME:-hakopod-neon-bounded}"
readonly BUILDER_CONTAINER="${HAKOPOD_NEON_BUILDER_CONTAINER:-hakopod-neon-buildkit}"
readonly BUILDER_ADDRESS="${HAKOPOD_NEON_BUILDER_ADDRESS:-tcp://127.0.0.1:12347}"
readonly BUILDKIT_IMAGE="moby/buildkit@sha256:cec9f139f45e93c5c69c60f8b07cfad9f43f4ef6b6a6cd917527fea5ff2e3dea"

usage() {
  cat >&2 <<'EOF'
usage: build-native-images.sh SOURCE_ARCHIVE OUTPUT_DIRECTORY PHASE

PHASE is one of:
  storage         Build the image containing storage_controller, pageserver,
                  safekeeper, and proxy.
  compute-tools   Build the compute-tools stage only.
  compute-runtime Build the PG17.11 core compute image without optional extensions.

Images remain in the VM-local Docker daemon. Run phases sequentially.
EOF
  exit 2
}

[[ $# == 3 ]] || usage
[[ "$BUILDER_MEMORY_BYTES" =~ ^[1-9][0-9]*$ && "$BUILDER_CPU_QUOTA" =~ ^[1-9][0-9]*$ && "$BUILDER_MAX_PARALLELISM" =~ ^[1-9][0-9]*$ ]] || {
  echo "refusing build: invalid builder resource limit" >&2
  exit 2
}
[[ "$BUILDER_NAME" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ && "$BUILDER_CONTAINER" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || {
  echo "refusing build: invalid builder identity" >&2
  exit 2
}
[[ "$BUILDER_ADDRESS" =~ ^tcp://127\.0\.0\.1:[1-9][0-9]{0,4}$ ]] || {
  echo "refusing build: builder must bind a loopback TCP address" >&2
  exit 2
}
builder_port=${BUILDER_ADDRESS##*:}
(( builder_port <= 65535 )) || { echo "refusing build: builder port is out of range" >&2; exit 2; }
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
      --network host --memory "$BUILDER_MEMORY_BYTES" --memory-swap "$BUILDER_MEMORY_BYTES" --cpu-period 100000 --cpu-quota "$BUILDER_CPU_QUOTA" \
      --volume "$buildkit_root:/var/lib/buildkit" "$BUILDKIT_IMAGE" \
      --addr "$BUILDER_ADDRESS" --oci-max-parallelism "$BUILDER_MAX_PARALLELISM" >/dev/null
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
  [[ "$memory" == "$BUILDER_MEMORY_BYTES" && "$memory_swap" == "$BUILDER_MEMORY_BYTES" && "$cpu_period" == 100000 && "$cpu_quota" == "$BUILDER_CPU_QUOTA" && \
    "$network_mode" == host && "$mounted_root" == "$buildkit_root" ]] || {
    echo "refusing build: $BUILDER_CONTAINER does not have the required resource or scratch-storage limits" >&2
    exit 1
  }
  sudo docker inspect "$BUILDER_CONTAINER" \
    | cat >"$output_directory/builder-inspect.json"
  archive="$output_directory/${phase}.oci.tar"
  base_archive="$output_directory/${phase}.base.oci.tar"
  [[ ! -e "$archive" && ! -e "$base_archive" ]] || { echo "refusing build: OCI archive already exists" >&2; exit 1; }
  sudo docker buildx build --builder "$BUILDER_NAME" --platform linux/amd64 --provenance=false --sbom=false \
    --output "type=oci,dest=$base_archive" --progress=plain \
    --metadata-file "$output_directory/${phase}-build-metadata.json" \
    --label "org.opencontainers.image.source=https://github.com/hakopod/hakopod" \
    --label "io.hakopod.neon.upstream-repository=https://github.com/neondatabase/neon" \
    --label "io.hakopod.neon.upstream-commit=$UPSTREAM_COMMIT" \
    --label "io.hakopod.neon.patched-tree=$PATCHED_TREE" \
    --label "io.hakopod.neon.postgres-commit=$POSTGRES_COMMIT" \
    --label "io.hakopod.neon.consumer-patch-id=$CONSUMER_PATCH_ID" \
    --label "io.hakopod.neon.source-archive-sha256=$SOURCE_ARCHIVE_SHA256" \
    --label "io.hakopod.neon.proxy-patch-sha256=$PROXY_PATCH_SHA256" \
    --label "io.hakopod.neon.ownership-patch-sha256=$OWNERSHIP_PATCH_SHA256" \
    --label "io.hakopod.neon.postgres-patch-sha256=$POSTGRES_PATCH_SHA256" \
    --label "io.hakopod.neon.consumer-patch-sha256=$CONSUMER_PATCH_SHA256" \
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
  python3 "$repo/release/wrap-runtime-oci.py" --input "$base_archive" --output "$archive" --user 1000:1000 \
    --label "org.opencontainers.image.source=https://github.com/hakopod/hakopod" >/dev/null
  sudo docker image load --input "$archive" >/dev/null
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
    image="hakopod/neon-compute-v17:pg17.11-${POSTGRES_COMMIT:0:8}-${CONSUMER_PATCH_ID:0:8}"
    build --file "$source_directory/compute/compute-node.Dockerfile" \
      --build-arg PG_VERSION=v17 --build-arg EXTENSIONS=none \
      --build-arg "BUILD_TAG=hakopod-pg17.11-${POSTGRES_COMMIT:0:8}" --tag "$image" "$source_directory" \
      >"$output_directory/compute-runtime-build.log" 2>&1
    ;;
  *) usage ;;
esac

sudo docker image inspect "$image" | cat >"$output_directory/${phase}-image-inspect.json"
printf '%s\n' "$image" >"$output_directory/${phase}-image.txt"
source_hash_after=$(sha256sum "$source_archive" | cut -d' ' -f1)
[[ "$source_hash_after" == "$source_hash_before" ]]
read -r image_manifest_digest image_config_digest < <(python3 - "$output_directory/${phase}-build-metadata.json" "$output_directory/${phase}.oci.tar" <<'PY'
import json
import re
import sys
import tarfile

digest_pattern = re.compile(r"sha256:[0-9a-f]{64}")
with open(sys.argv[1], encoding="utf-8") as stream:
    metadata = json.load(stream)
metadata_digest = metadata.get("containerimage.digest", "")
if not isinstance(metadata_digest, str) or not digest_pattern.fullmatch(metadata_digest):
    raise SystemExit("build metadata does not contain a SHA-256 manifest digest")
with tarfile.open(sys.argv[2]) as archive:
    index = json.load(archive.extractfile("index.json"))
    manifests = index.get("manifests", [])
    if len(manifests) != 1 or not digest_pattern.fullmatch(manifests[0].get("digest", "")):
        raise SystemExit("OCI archive does not contain one SHA-256 image manifest")
    manifest_digest = manifests[0]["digest"]
    manifest = json.load(archive.extractfile("blobs/sha256/" + manifest_digest[7:]))
    config_digest = manifest.get("config", {}).get("digest", "")
    if not isinstance(config_digest, str) or not digest_pattern.fullmatch(config_digest):
        raise SystemExit("OCI archive manifest does not contain a SHA-256 config digest")
    config = json.load(archive.extractfile("blobs/sha256/" + config_digest[7:]))
    if config.get("config", {}).get("Labels", {}).get("io.hakopod.managed-runtime.base-manifest") != metadata_digest:
        raise SystemExit("numeric-user wrapper is not bound to the BuildKit manifest")
print(manifest_digest, config_digest)
PY
)
cat >"$output_directory/${phase}-provenance.txt" <<EOF
upstream_commit=$UPSTREAM_COMMIT
patched_tree=$PATCHED_TREE
source_archive_sha256_before=$source_hash_before
source_archive_sha256_after=$source_hash_after
proxy_patch_sha256=$PROXY_PATCH_SHA256
ownership_patch_sha256=$OWNERSHIP_PATCH_SHA256
postgres_patch_sha256=$POSTGRES_PATCH_SHA256
consumer_patch_sha256=$CONSUMER_PATCH_SHA256
postgres_commit=$POSTGRES_COMMIT
consumer_patch_id=$CONSUMER_PATCH_ID
image=$image
image_manifest_digest=$image_manifest_digest
image_config_digest=$image_config_digest
published=false
cluster_qualified=false
EOF
echo "built locally: $image"
