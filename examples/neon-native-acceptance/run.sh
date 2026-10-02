#!/bin/sh
set -eu
umask 077

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
expected_context=k3d-hakopod-dev

: "${KUBECONFIG:?set the dedicated Hakopod development kubeconfig}"
: "${HAKOPOD_ACCEPTANCE_IMAGES:?set the reviewed digest-pinned Neon image inventory}"
: "${HAKOPOD_ACCEPTANCE_IDENTITIES:?set the reviewed Neon image UID/GID inventory}"
: "${HAKOPOD_ACCEPTANCE_WORK_DIR:?set a fresh protected work directory}"
: "${HAKOPOD_ACCEPTANCE_SHIPPING_TREE:?set the exact unmodified shipping source tree hash}"
: "${HAKOPOD_ACCEPTANCE_SERVER_TREE:?set the exact acceptance server source tree hash}"
: "${HAKOPOD_ACCEPTANCE_GATE_ATTESTATION:?set the reviewed in-process development gate attestation path}"

for command in kubectl python3 sha256sum jq; do
  command -v "$command" >/dev/null 2>&1 || { echo "$command is required" >&2; exit 2; }
done
[ "$(kubectl --kubeconfig "$KUBECONFIG" config current-context)" = "$expected_context" ] || { echo "refusing Kubernetes context" >&2; exit 2; }
[ ! -e "$HAKOPOD_ACCEPTANCE_WORK_DIR" ] && [ ! -L "$HAKOPOD_ACCEPTANCE_WORK_DIR" ] || { echo "use a fresh acceptance work directory" >&2; exit 2; }
case "$HAKOPOD_ACCEPTANCE_SHIPPING_TREE:$HAKOPOD_ACCEPTANCE_SERVER_TREE" in
  [0-9a-f][0-9a-f]*:[0-9a-f][0-9a-f]*) ;;
  *) echo "source tree identities are malformed" >&2; exit 2 ;;
esac
[ -f "$HAKOPOD_ACCEPTANCE_GATE_ATTESTATION" ] && [ ! -L "$HAKOPOD_ACCEPTANCE_GATE_ATTESTATION" ] || { echo "reviewed development gate attestation is unavailable" >&2; exit 2; }

# The shipping runtime intentionally keeps Neon unavailable. Native execution
# must use the reviewed in-process test-server injection described by the
# attestation. It may only replace the capability decision; runtime packages,
# API handlers, renderers, manifests and recovery code must match the shipping
# tree. Until that injection exposes a source-bound driver contract, this
# runner refuses to accept caller-authored pass events or observation files.
echo "Neon native acceptance is blocked until the reviewed shipping-binary development gate exposes the committed driver contract" >&2
exit 3
