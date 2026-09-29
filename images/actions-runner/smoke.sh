#!/bin/sh
set -eu

test "$(id -u)" = 1001
test -f /home/runner/run.sh
for tool in cc c++ make cmake ninja pkg-config gh git jq tar zstd psql; do
    command -v "$tool"
done
gh --version

scratch=$(mktemp -d /tmp/hakopod-runner-smoke.XXXXXX)
trap 'rm -rf "$scratch"' EXIT
cd "$scratch"
# GitHub's cache action uses the runner's archive tools, without host access.
mkdir cache-source cache-restored
printf 'Managed Actions cache archive\n' > cache-source/proof
tar --use-compress-program='zstd -T1' -cf cache.tar.zst -C cache-source proof
tar --use-compress-program='zstd -d' -xf cache.tar.zst -C cache-restored
cmp cache-source/proof cache-restored/proof
cat > hello.c <<'EOF'
#include <stdio.h>
int main(void) { puts("C toolchain works"); return 0; }
EOF
cc -Wall -Werror hello.c -o hello
./hello

# Force the ARM64 failure's exact package through its source-build path on
# both architectures. Use setup-python's 3.12 interpreter mounted read-only.
"${HAKOPOD_SMOKE_PYTHON:?}" -m venv venv
export CMAKE_BUILD_PARALLEL_LEVEL=2
venv/bin/python -m pip wheel --no-cache-dir --no-binary=zxing-cpp --wheel-dir wheels zxing-cpp==2.3.0
venv/bin/python -m pip install --no-cache-dir wheels/zxing_cpp-2.3.0-*.whl numpy==2.2.6 pyzbar==0.1.9
venv/bin/python - <<'PY'
import sys
import numpy as np
import zxingcpp
from pyzbar.pyzbar import decode
assert sys.version_info[:2] == (3, 12), sys.version
payload = "Hakopod managed runner native build"
barcode = zxingcpp.write_barcode(zxingcpp.BarcodeFormat.QRCode, payload)
result = zxingcpp.read_barcode(barcode)
assert result is not None and result.text == payload
image = np.repeat(np.repeat(np.asarray(barcode), 8, axis=0), 8, axis=1)
image = np.pad(image, 32, constant_values=255)
decoded = decode(image)
assert decoded and decoded[0].data.decode() == payload
print("Python 3.12 zxing-cpp source build and barcode round trip passed")
PY
