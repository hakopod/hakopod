#!/usr/bin/env bash
set -euo pipefail

readonly UPSTREAM_REPOSITORY="https://github.com/neondatabase/neon.git"
readonly UPSTREAM_COMMIT="fa504217c61bbcaf5c512d75830564541f917f8f"
readonly PROXY_PATCH_SHA256="e9a1df309106d166adfc0982500f6500df220dbc6173761c48c7c2038563fbd6"
readonly OWNERSHIP_PATCH_SHA256="a7d464f88f374480eedde78efa4c8ea4d12928fbc11104aea710e2b9691491f4"
readonly POSTGRES_PATCH_SHA256="47f90f03bc3aa0893b952049d86da54713843abdc94b74166a33c67bfb95a3e6"
readonly CONSUMER_PATCH_SHA256="f425b69c6d20153ea3fbba2633d53adc77007f4d84468423d3544bc5fff11e60"
readonly TRANSPORT_PATCH_SHA256="3d4c6ee5a7d30a77f8ab44520b6e370829e12b41f827c99d4120f0158bf506fa"
readonly RECONFIGURE_PATCH_SHA256="96b5f4e211186ea60281128029c668ad12bb6d3cf3a552879363fe24750458c9"
readonly PLACEMENT_PATCH_SHA256="9c20c7c2491e367be87f8aae5483b7f4e1158e276c065b336f9a1040282f073a"
readonly FILESYSTEM_PATCH_SHA256="2cab4695d0d4e7029bbe2c9523d42929c4c9a19a2c25dd5e7064e830882e1256"
readonly APPLYING_DELETE_PATCH_SHA256="e86ee6ac05bbb4efe05349563b96ea520dc574a2b9ea04adfd9687829040a204"
readonly POSTGRES_COMMIT="1e01fcea2a6b38180021aa83e0051d95286d9096"
readonly POSTGRES_TREE="5aca82893a42d97c0fce65a8f63d02d9e0587a19"
readonly CONSUMER_PATCH_ID="627583b85d7c0f624245e3ac6a240e775a5eed01"
readonly COMBINED_CANDIDATE_TREE="3358ae2e6529d42fe723ed566678978665d3b7a6"

usage() {
  echo "usage: $0 OUTPUT_DIRECTORY [UPSTREAM_CHECKOUT]" >&2
  exit 2
}

[[ $# -ge 1 && $# -le 2 ]] || usage
output_directory=$(mkdir -p "$1" && cd "$1" && pwd)
upstream_checkout=${2:-}
script_directory=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "$script_directory/../.." && pwd)
proxy_patch="$repository_root/patches/neon-proxy-control-plane-timeout.patch"
ownership_patch="$repository_root/patches/neon-provider-ownership.patch"
postgres_patch="$repository_root/patches/neon-postgres-17.11.patch"
consumer_patch="$repository_root/patches/neon-pg17.11-consumers.patch"
transport_patch="$repository_root/patches/neon-storage-tls.patch"
reconfigure_patch="$repository_root/patches/neon-compute-reconfigure.patch"
placement_patch="$repository_root/patches/neon-controller-placement.patch"
filesystem_patch="$repository_root/patches/neon-layer-publish.patch"
applying_delete_patch="$repository_root/patches/neon-applying-delete.patch"

printf '%s  %s\n' "$PROXY_PATCH_SHA256" "$proxy_patch" | sha256sum --check --status
printf '%s  %s\n' "$OWNERSHIP_PATCH_SHA256" "$ownership_patch" | sha256sum --check --status
printf '%s  %s\n' "$POSTGRES_PATCH_SHA256" "$postgres_patch" | sha256sum --check --status
printf '%s  %s\n' "$CONSUMER_PATCH_SHA256" "$consumer_patch" | sha256sum --check --status
printf '%s  %s\n' "$TRANSPORT_PATCH_SHA256" "$transport_patch" | sha256sum --check --status
printf '%s  %s\n' "$RECONFIGURE_PATCH_SHA256" "$reconfigure_patch" | sha256sum --check --status
printf '%s  %s\n' "$PLACEMENT_PATCH_SHA256" "$placement_patch" | sha256sum --check --status
printf '%s  %s\n' "$FILESYSTEM_PATCH_SHA256" "$filesystem_patch" | sha256sum --check --status
printf '%s  %s\n' "$APPLYING_DELETE_PATCH_SHA256" "$applying_delete_patch" | sha256sum --check --status

work_directory=$(mktemp -d "${TMPDIR:-/tmp}/hakopod-neon-source.XXXXXXXX")
archive="${output_directory}/neon-provider-source.tar.gz"
archive_temporary="${output_directory}/.neon-provider-source.tar.gz.$$.tmp"
cleanup() {
  chmod -R u+w "$work_directory" 2>/dev/null || true
  rm -rf -- "$work_directory"
  rm -f -- "$archive_temporary"
}
trap cleanup EXIT

source_directory="$work_directory/source"
if [[ -n "$upstream_checkout" ]]; then
  git clone --quiet --no-hardlinks "$upstream_checkout" "$source_directory"
else
  git clone --quiet "$UPSTREAM_REPOSITORY" "$source_directory"
fi
git -C "$source_directory" checkout --quiet --detach "$UPSTREAM_COMMIT"
# Relative submodule URLs must resolve against the official repository even
# when the initial clone reuses a local checkout.
git -C "$source_directory" remote set-url origin "$UPSTREAM_REPOSITORY"
git -C "$source_directory" submodule update --init --recursive --depth 1 --jobs 1

[[ "$(git -C "$source_directory" rev-parse HEAD)" == "$UPSTREAM_COMMIT" ]]
git -C "$source_directory" apply --check "$proxy_patch"
git -C "$source_directory" apply "$proxy_patch"
git -C "$source_directory" apply --check "$ownership_patch"
git -C "$source_directory" apply "$ownership_patch"
git -C "$source_directory" apply --check "$consumer_patch"
git -C "$source_directory" apply "$consumer_patch"
git -C "$source_directory" apply --check "$transport_patch"
git -C "$source_directory" apply "$transport_patch"
git -C "$source_directory" apply --check "$reconfigure_patch"
git -C "$source_directory" apply "$reconfigure_patch"
git -C "$source_directory" apply --check "$placement_patch"
git -C "$source_directory" apply "$placement_patch"
git -C "$source_directory" apply --check "$filesystem_patch"
git -C "$source_directory" apply "$filesystem_patch"
git -C "$source_directory" apply --check "$applying_delete_patch"
git -C "$source_directory" apply "$applying_delete_patch"
git -C "$source_directory" add -A
[[ "$(git -C "$source_directory" write-tree)" == "$COMBINED_CANDIDATE_TREE" ]] || { echo "refusing archive: combined candidate tree mismatch" >&2; exit 1; }

postgres_directory="$source_directory/vendor/postgres-v17"
git -C "$postgres_directory" fetch --quiet --depth 1 origin "$POSTGRES_COMMIT"
git -C "$postgres_directory" checkout --quiet --detach "$POSTGRES_COMMIT"
[[ "$(git -C "$postgres_directory" rev-parse HEAD)" == "$POSTGRES_COMMIT" ]]
git -C "$postgres_directory" apply --check "$postgres_patch"
git -C "$postgres_directory" apply "$postgres_patch"
git -C "$postgres_directory" add -A
[[ "$(git -C "$postgres_directory" write-tree)" == "$POSTGRES_TREE" ]] || { echo "refusing archive: PostgreSQL candidate tree mismatch" >&2; exit 1; }

find "$source_directory" -name .git -prune -exec rm -rf -- {} +
archive_second="$work_directory/neon-provider-source-second.tar.gz"
(
  cd "$work_directory"
  find source \( -type d -printf '%p/\0' \) -o \( ! -type d -print0 \) | \
    LC_ALL=C sort -z >members.list
  LC_ALL=C tar --no-recursion --null --files-from=members.list --mtime='@0' \
    --owner=0 --group=0 --numeric-owner --pax-option=delete=atime,delete=ctime \
    --mode='u+rw,go+r-w,a+X' \
    -cf - | gzip -n >"$archive_temporary"
  LC_ALL=C tar --no-recursion --null --files-from=members.list --mtime='@0' \
    --owner=0 --group=0 --numeric-owner --pax-option=delete=atime,delete=ctime \
    --mode='u+rw,go+r-w,a+X' \
    -cf - | gzip -n >"$archive_second"
)

cmp --silent "$archive_temporary" "$archive_second"
ln "$archive_temporary" "$archive"
rm -f -- "$archive_temporary"
{
  printf '%s  %s\n' "$(sha256sum "$archive" | cut -d' ' -f1)" "$(basename "$archive")"
  printf '%s  %s\n' "$PROXY_PATCH_SHA256" "patches/neon-proxy-control-plane-timeout.patch"
  printf '%s  %s\n' "$OWNERSHIP_PATCH_SHA256" "patches/neon-provider-ownership.patch"
  printf '%s  %s\n' "$POSTGRES_PATCH_SHA256" "patches/neon-postgres-17.11.patch"
  printf '%s  %s\n' "$CONSUMER_PATCH_SHA256" "patches/neon-pg17.11-consumers.patch"
  printf '%s  %s\n' "$TRANSPORT_PATCH_SHA256" "patches/neon-storage-tls.patch"
  printf '%s  %s\n' "$RECONFIGURE_PATCH_SHA256" "patches/neon-compute-reconfigure.patch"
  printf '%s  %s\n' "$PLACEMENT_PATCH_SHA256" "patches/neon-controller-placement.patch"
  printf '%s  %s\n' "$FILESYSTEM_PATCH_SHA256" "patches/neon-layer-publish.patch"
  printf '%s  %s\n' "$APPLYING_DELETE_PATCH_SHA256" "patches/neon-applying-delete.patch"
} >"$output_directory/SHA256SUMS"
cat >"$output_directory/provenance.txt" <<EOF
upstream_repository=$UPSTREAM_REPOSITORY
upstream_commit=$UPSTREAM_COMMIT
proxy_patch_sha256=$PROXY_PATCH_SHA256
ownership_patch_sha256=$OWNERSHIP_PATCH_SHA256
postgres_patch_sha256=$POSTGRES_PATCH_SHA256
consumer_patch_sha256=$CONSUMER_PATCH_SHA256
transport_patch_sha256=$TRANSPORT_PATCH_SHA256
reconfigure_patch_sha256=$RECONFIGURE_PATCH_SHA256
placement_patch_sha256=$PLACEMENT_PATCH_SHA256
filesystem_patch_sha256=$FILESYSTEM_PATCH_SHA256
applying_delete_patch_sha256=$APPLYING_DELETE_PATCH_SHA256
postgres_commit=$POSTGRES_COMMIT
postgres_tree=$POSTGRES_TREE
consumer_patch_id=$CONSUMER_PATCH_ID
archive_sha256=$(sha256sum "$archive" | cut -d' ' -f1)
archive_recipe=GNU_tar_global_NUL_sort_no_recursion_epoch_mtime_numeric_root_normalized_mode_gzip_n
combined_candidate_tree=$COMBINED_CANDIDATE_TREE
archive_repeat_comparison=PASS
images_built=false
images_published=false
cluster_qualified=false
EOF

tar -tzf "$archive" >"$output_directory/archive-members.txt"
LC_ALL=C sort -c "$output_directory/archive-members.txt"
echo "prepared $archive"
