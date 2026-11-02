#!/usr/bin/env bash
# Stage, generate and publish the npm CLI packages for one release.
#
#   release/publish-npm-cli.sh <version> <directory>
#
# <directory> holds SHA256SUMS and the hakopod-cli_<version>_<platform>.tar.gz
# archives the GitHub release publishes. Used by the release job and by the
# manual npm recovery run.
set -euo pipefail
version=$1 directory=$2

# The archives are what the release publishes; re-verify before repackaging.
(cd "$directory" && grep 'hakopod-cli_' SHA256SUMS | sha256sum --check --strict)
for platform in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 windows_amd64 windows_arm64; do
  target=".local/npm-archives/$platform"
  mkdir -p "$target"
  tar -xzf "$directory/hakopod-cli_${version}_${platform}.tar.gz" -C "$target" --strip-components=1
done
node packages/cli-binary/generate.mjs "$version" .local/npm-archives .local/npm-packages

# Publishing seven packages in sequence means a failure partway leaves the
# earlier ones published. npm refuses to republish a version, so a naive
# re-run would die on those instead of resuming. Skip what is already on the
# registry at this exact version.
publish() {
  local name version
  name=$(node -p "require('./$1/package.json').name")
  version=$(node -p "require('./$1/package.json').version")
  if npm view "$name@$version" version >/dev/null 2>&1; then
    echo "skip $name@$version (already published)"
    return 0
  fi
  npm publish --provenance --access public --tag latest "$1"
}

# Platform packages first: the wrapper's optionalDependencies pin these exact
# versions, and npm resolves them at install time.
for package in .local/npm-packages/cli-*; do
  publish "$package"
done
publish .local/npm-packages/cli
