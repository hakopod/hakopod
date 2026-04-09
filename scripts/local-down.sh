#!/usr/bin/env bash
# Stops only named development resources, preserving database and cluster data.
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
python3 - <<'PY'
import subprocess
try:
    subprocess.run(['docker','info'], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, check=True, timeout=15)
except (subprocess.TimeoutExpired, subprocess.CalledProcessError):
    raise SystemExit('Docker is not responding. Restore the Docker engine before stopping the named development resources.')
PY
if [[ -x "$ROOT/.local/bin/k3d" ]] && docker container inspect k3d-hakopod-dev-server-0 >/dev/null 2>&1; then
  "$ROOT/.local/bin/k3d" cluster stop hakopod-dev
fi
if docker container inspect hakopod-postgres >/dev/null 2>&1; then
  [[ "$(docker inspect --format '{{index .Config.Labels "com.hakopod.development"}}' hakopod-postgres)" == true ]] || { echo 'Refusing to stop an unowned database.' >&2; exit 1; }
  docker stop hakopod-postgres >/dev/null
fi
echo 'Development infrastructure stopped; data preserved. Restart with scripts/local-up.sh.'
