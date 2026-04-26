#!/usr/bin/env bash
# Install a pinned scanner only under this repository; no global installation.
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
case "$(uname -s)" in Darwin) os=darwin;; Linux) os=linux;; *) echo 'Syft installer supports macOS and Linux.' >&2; exit 1;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64;; x86_64) arch=amd64;; *) echo 'Supported architectures: amd64 and arm64.' >&2; exit 1;; esac
version=1.51.1
asset="syft_${version}_${os}_${arch}.tar.gz"
mkdir -p "$ROOT/.local/downloads/syft" "$ROOT/.local/bin"
archive="$ROOT/.local/downloads/syft/$asset"
if [[ ! -f "$archive" ]]; then
  curl --fail --silent --show-error --location --retry 3 \
    "https://github.com/anchore/syft/releases/download/v$version/$asset" -o "$archive"
fi
python3 - "$ROOT" "$asset" <<'PY'
import hashlib, pathlib, sys, tarfile
root, asset = pathlib.Path(sys.argv[1]), sys.argv[2]
archive = root/'.local/downloads/syft'/asset
expected = next(line.split()[0] for line in (root/'release/syft-checksums.sha256').read_text().splitlines() if line and not line.startswith('#') and line.split()[-1] == asset)
if hashlib.sha256(archive.read_bytes()).hexdigest() != expected:
    raise SystemExit('Syft checksum mismatch; refusing to execute the download')
with tarfile.open(archive) as bundle:
    member = bundle.getmember('syft')
    if not member.isfile():
        raise SystemExit('Syft executable is not a regular archive member')
    binary = bundle.extractfile(member).read()
target = root/'.local/bin/syft'
if not target.exists() or target.read_bytes() != binary:
    target.write_bytes(binary)
target.chmod(0o755)
print(f'Checksum-verified Syft {asset}')
PY
