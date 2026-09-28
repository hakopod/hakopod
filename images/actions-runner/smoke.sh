#!/bin/sh
set -eu

test "$(id -u)" = 1001
test -f /home/runner/run.sh
for tool in cc c++ make cmake ninja pkg-config gh git jq; do
    command -v "$tool"
done
gh --version

scratch=$(mktemp -d /tmp/hakopod-runner-smoke.XXXXXX)
trap 'rm -rf "$scratch"' EXIT
cd "$scratch"
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
venv/bin/python -m pip install --no-cache-dir wheels/zxing_cpp-2.3.0-*.whl numpy==2.2.6
venv/bin/python - <<'PY'
import sys
import zxingcpp
assert sys.version_info[:2] == (3, 12), sys.version
payload = "Hakopod managed runner native build"
barcode = zxingcpp.write_barcode(zxingcpp.BarcodeFormat.QRCode, payload)
result = zxingcpp.read_barcode(barcode)
assert result is not None and result.text == payload
print("Python 3.12 zxing-cpp source build and barcode round trip passed")
PY

