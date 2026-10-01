#!/usr/bin/env bash
set -euo pipefail

readonly UPSTREAM_REPOSITORY="https://github.com/neondatabase/neon.git"
readonly UPSTREAM_COMMIT="fa504217c61bbcaf5c512d75830564541f917f8f"
readonly PROXY_PATCH_SHA256="e9a1df309106d166adfc0982500f6500df220dbc6173761c48c7c2038563fbd6"
readonly OWNERSHIP_PATCH_SHA256="a7d464f88f374480eedde78efa4c8ea4d12928fbc11104aea710e2b9691491f4"
readonly COMBINED_CANDIDATE_TREE="8fcaa8600a9da41870d6f74478cfdfcd53fb2dd6"

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

printf '%s  %s\n' "$PROXY_PATCH_SHA256" "$proxy_patch" | sha256sum --check --status
printf '%s  %s\n' "$OWNERSHIP_PATCH_SHA256" "$ownership_patch" | sha256sum --check --status

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
git -C "$source_directory" submodule update --init --recursive --depth 1 --jobs 1

[[ "$(git -C "$source_directory" rev-parse HEAD)" == "$UPSTREAM_COMMIT" ]]
git -C "$source_directory" apply --check "$proxy_patch"
git -C "$source_directory" apply "$proxy_patch"
git -C "$source_directory" apply --check "$ownership_patch"
git -C "$source_directory" apply "$ownership_patch"
git -C "$source_directory" add -A
[[ "$(git -C "$source_directory" write-tree)" == "$COMBINED_CANDIDATE_TREE" ]] || { echo "refusing archive: combined candidate tree mismatch" >&2; exit 1; }

find "$source_directory" -name .git -prune -exec rm -rf -- {} +
archive_second="$work_directory/neon-provider-source-second.tar.gz"
(
  cd "$work_directory"
  find source \( -type d -printf '%p/\0' \) -o \( ! -type d -print0 \) | \
    LC_ALL=C sort -z >members.list
  LC_ALL=C tar --no-recursion --null --files-from=members.list --mtime='@0' \
    --owner=0 --group=0 --numeric-owner --pax-option=delete=atime,delete=ctime \
    -cf - | gzip -n >"$archive_temporary"
  LC_ALL=C tar --no-recursion --null --files-from=members.list --mtime='@0' \
    --owner=0 --group=0 --numeric-owner --pax-option=delete=atime,delete=ctime \
    -cf - | gzip -n >"$archive_second"
)

cmp --silent "$archive_temporary" "$archive_second"
ln "$archive_temporary" "$archive"
rm -f -- "$archive_temporary"
{
  printf '%s  %s\n' "$(sha256sum "$archive" | cut -d' ' -f1)" "$(basename "$archive")"
  printf '%s  %s\n' "$PROXY_PATCH_SHA256" "patches/neon-proxy-control-plane-timeout.patch"
  printf '%s  %s\n' "$OWNERSHIP_PATCH_SHA256" "patches/neon-provider-ownership.patch"
} >"$output_directory/SHA256SUMS"
cat >"$output_directory/provenance.txt" <<EOF
upstream_repository=$UPSTREAM_REPOSITORY
upstream_commit=$UPSTREAM_COMMIT
proxy_patch_sha256=$PROXY_PATCH_SHA256
ownership_patch_sha256=$OWNERSHIP_PATCH_SHA256
archive_sha256=$(sha256sum "$archive" | cut -d' ' -f1)
archive_recipe=GNU_tar_global_NUL_sort_no_recursion_epoch_mtime_numeric_root_gzip_n
combined_candidate_tree=$COMBINED_CANDIDATE_TREE
archive_repeat_comparison=PASS
images_built=false
images_published=false
cluster_qualified=false
EOF

tar -tzf "$archive" >"$output_directory/archive-members.txt"
LC_ALL=C sort -c "$output_directory/archive-members.txt"
echo "prepared $archive"
