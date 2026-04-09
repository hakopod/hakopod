#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
source "$ROOT/deploy/local/versions.env"
mkdir -p "$ROOT/.local/bin" "$ROOT/.local/downloads"
case "$(uname -s)" in Darwin) os=darwin;; Linux) os=linux;; *) echo 'Unsupported OS; use Linux or macOS.' >&2; exit 1;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64;; x86_64) arch=amd64;; *) echo 'Supported architectures: amd64 and arm64.' >&2; exit 1;; esac
asset="k3d-$os-$arch"
if [[ -x "$ROOT/.local/bin/k3d" ]] && python3 - "$ROOT" "$asset" <<'PY'
import hashlib, pathlib, sys
root, asset = pathlib.Path(sys.argv[1]), sys.argv[2]
expected = next(line.split()[0] for line in (root/'deploy/local/checksums.sha256').read_text().splitlines() if line and not line.startswith('#') and line.split()[-1] == asset)
raise SystemExit(hashlib.sha256((root/'.local/bin/k3d').read_bytes()).hexdigest() != expected)
PY
then
  exit 0
fi
curl --fail --silent --show-error --location --retry 3 "https://github.com/k3d-io/k3d/releases/download/$K3D_VERSION/$asset" -o "$ROOT/.local/downloads/$asset"
curl --fail --silent --show-error --location --retry 3 "https://github.com/k3d-io/k3d/releases/download/$K3D_VERSION/checksums.txt" -o "$ROOT/.local/downloads/checksums.txt"
python3 - "$ROOT" "$asset" <<'PY'
import hashlib, pathlib, sys
root, asset = pathlib.Path(sys.argv[1]), sys.argv[2]
download = root / '.local/downloads' / asset
expected = next(line.split()[0] for line in (root / '.local/downloads/checksums.txt').read_text().splitlines() if pathlib.Path(line.split()[-1]).name == asset)
pinned = next(line.split()[0] for line in (root / 'deploy/local/checksums.sha256').read_text().splitlines() if line and not line.startswith('#') and line.split()[-1] == asset)
actual = hashlib.sha256(download.read_bytes()).hexdigest()
if actual != expected or actual != pinned:
    raise SystemExit('k3d checksum mismatch; refusing to install')
target = root / '.local/bin/k3d'
target.write_bytes(download.read_bytes())
target.chmod(0o755)
print(f'Installed checksum-verified {asset}: {actual}')
PY
