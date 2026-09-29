#!/bin/sh
# Build only on a bounded development VM or native CI worker.
set -eu

build_root=${1:?Pass a fresh owned build directory}
image_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
build_go=${HAKOPOD_BUILD_GO:-go}
case "$(uname -m)" in
  x86_64) build_arch=amd64 ;;
  aarch64|arm64) build_arch=arm64 ;;
  *) echo 'Unsupported native architecture' >&2; exit 1 ;;
esac
case "$("$build_go" version)" in
  'go version go1.26.8 linux/'*) ;;
  *) echo 'Use the qualified Go 1.26.8 Linux toolchain' >&2; exit 1 ;;
esac
if [ -e "$build_root" ]; then
  echo 'Build directory already exists; preserve it and use a fresh owned directory' >&2
  exit 1
fi
mkdir -p "$build_root"
build_root=$(CDPATH= cd -- "$build_root" && pwd)
git clone --depth=1 --branch=v19.4.1 --single-branch \
  https://gitlab.com/gitlab-org/gitlab-runner.git "$build_root/source"
test "$(git -C "$build_root/source" rev-parse HEAD)" = 3c39fcebf73d01d464db3dee8a5267155273a6c5
git -C "$build_root/source" apply --check "$image_root/transport.patch"
git -C "$build_root/source" apply "$image_root/transport.patch"
git -C "$build_root/source" apply --check "$image_root/cache.patch"
git -C "$build_root/source" apply "$image_root/cache.patch"
python3 - "$image_root/overlay" "$build_root/source" <<'PY'
from pathlib import Path
import sys
overlay, source = map(Path, sys.argv[1:])
for template in sorted(overlay.rglob('*.go.in')):
    target = source / template.relative_to(overlay).with_suffix('')
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(template.read_bytes())
PY
mkdir -p "$build_root/cache" "$build_root/mod" "$build_root/tmp" "$image_root/bin/$build_arch"
export GOCACHE="$build_root/cache" GOMODCACHE="$build_root/mod" GOTMPDIR="$build_root/tmp"
export GOTOOLCHAIN=local CGO_ENABLED=0 GOMAXPROCS=1
cd "$build_root/source"
"$(dirname -- "$(command -v "$build_go")")/gofmt" -w network/hakopod_transport.go network/hakopod_transport_test.go \
  network/client.go network/gitlab.go commands/verify.go commands/helpers/artifacts_uploader.go \
  cache/hakopod_s3.go cache/hakopod_s3_test.go commands/helpers/hakopod_cache.go commands/helpers/hakopod_cache_test.go \
  commands/helpers/cache_client.go commands/helpers/cache_archiver.go commands/helpers/cache_extractor.go
"$build_go" test -trimpath -p 1 ./network -run '^TestHakopod' -count=1
"$build_go" test -trimpath -p 1 ./cache ./commands/helpers -run '^TestHakopod' -count=1
build_flags='-X gitlab.com/gitlab-org/gitlab-runner/common.VERSION=19.4.1 -X gitlab.com/gitlab-org/gitlab-runner/common.REVISION=3c39fceb-hakopod -X gitlab.com/gitlab-org/gitlab-runner/common.BRANCH=hakopod-transport'
"$build_go" build -p 1 -trimpath -ldflags "$build_flags" -o "$image_root/bin/$build_arch/gitlab-runner" .
"$build_go" build -p 1 -trimpath -ldflags "$build_flags" -o "$image_root/bin/$build_arch/gitlab-runner-helper" ./apps/gitlab-runner-helper
python3 "$image_root/source-manifest.py" --go "$(command -v "$build_go")" --output "$image_root/bin/$build_arch/transport-source.json"
sha256sum "$image_root/bin/$build_arch/gitlab-runner" "$image_root/bin/$build_arch/gitlab-runner-helper"
