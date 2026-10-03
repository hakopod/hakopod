#!/bin/sh
set -eu
umask 077
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)

: "${KUBECONFIG:?set the protected development kubeconfig}"
: "${HAKOPOD_ACCEPTANCE_API_URL:?set the loopback native-acceptance API URL}"
: "${HAKOPOD_ACCEPTANCE_API_TOKEN_FILE:?set the mode-600 disposable bearer-key file}"
: "${HAKOPOD_ACCEPTANCE_PROJECT:?set the disposable project bound by the gate attestation}"
: "${HAKOPOD_ACCEPTANCE_SPEC:?set the reviewed source Neon specification}"
: "${HAKOPOD_ACCEPTANCE_TARGET_SPEC:?set the reviewed separate empty-target specification}"
: "${HAKOPOD_ACCEPTANCE_CANCELLATION_TARGET_SPEC:?set the reviewed fresh cancellation-target specification}"
: "${HAKOPOD_ACCEPTANCE_IMAGES:?set the reviewed digest-pinned image inventory}"
: "${HAKOPOD_ACCEPTANCE_IDENTITIES:?set the reviewed image UID/GID inventory}"
: "${HAKOPOD_ACCEPTANCE_DESTINATION_ID:?set the disposable recovery destination ID}"
: "${HAKOPOD_ACCEPTANCE_DESTINATION_REVISION:?set its current revision}"
: "${HAKOPOD_ACCEPTANCE_WORK_DIR:?set a fresh protected work directory}"
: "${HAKOPOD_ACCEPTANCE_GATE_ATTESTATION:?set the reviewed gate attestation}"
: "${HAKOPOD_ACCEPTANCE_MANAGED_TLS_HELPER:?set the Neon managed TLS helper}"
: "${HAKOPOD_ACCEPTANCE_RUNTIME_SPEC_DIGEST_HELPER:?set the typed runtime-spec digest helper}"
: "${HAKOPOD_ACCEPTANCE_CONTROL_PSQL_COMMAND_FILE:?set the protected control-database command argv}"
: "${HAKOPOD_ACCEPTANCE_CONTROL_PLANE_BRIDGE:?set the control-plane bridge helper}"
: "${HAKOPOD_ACCEPTANCE_OBJECT_STORE_FAULT_COMMAND_FILE:?set the protected disposable object-store fault command}"
: "${HAKOPOD_ACCEPTANCE_OPENSSL:?set the reviewed OpenSSL executable}"
: "${HAKOPOD_ACCEPTANCE_PSQL:?set the PostgreSQL client executable}"
: "${HAKOPOD_ACCEPTANCE_SOURCE_PROXY_PASSWORD_FILE:?set the protected source proxy application credential file}"
: "${HAKOPOD_ACCEPTANCE_TARGET_PROXY_PASSWORD_FILE:?set the protected target proxy application credential file}"
: "${HAKOPOD_ACCEPTANCE_CANCELLATION_PROXY_PASSWORD_FILE:?set the protected cancellation proxy application credential file}"
: "${HAKOPOD_ACCEPTANCE_LOCAL_PORT:?set a free loopback port for the Neon proxy}"
: "${HAKOPOD_ACCEPTANCE_DISPOSABLE:?set to 1 for this destructive disposable run}"

[ "$HAKOPOD_ACCEPTANCE_DISPOSABLE" = 1 ] || { echo 'native acceptance requires an explicit disposable marker' >&2; exit 2; }
[ "$(uname -s):$(uname -m)" = Linux:x86_64 ] || { echo 'native acceptance requires Linux AMD64' >&2; exit 2; }
for command in kubectl python3 stat; do command -v "$command" >/dev/null 2>&1 || { echo "$command is required" >&2; exit 2; }; done
[ "$(kubectl --request-timeout=15s --kubeconfig "$KUBECONFIG" config current-context)" = k3d-hakopod-dev ] || { echo 'refusing Kubernetes context' >&2; exit 2; }
[ ! -e "$HAKOPOD_ACCEPTANCE_WORK_DIR" ] && [ ! -L "$HAKOPOD_ACCEPTANCE_WORK_DIR" ] || { echo 'use a fresh work directory' >&2; exit 2; }
case "$HAKOPOD_ACCEPTANCE_WORK_DIR" in /tmp/hakopod-neon-native-*|/srv/hakopod-backup-scratch/hakopod-neon-native-*) ;; *) echo 'work directory is outside accepted scratch roots' >&2; exit 2;; esac
case "$HAKOPOD_ACCEPTANCE_API_URL" in http://127.0.0.1:*|http://localhost:*) ;; *) echo 'acceptance API must be loopback HTTP' >&2; exit 2;; esac
for file in "$KUBECONFIG" "$HAKOPOD_ACCEPTANCE_API_TOKEN_FILE" "$HAKOPOD_ACCEPTANCE_SOURCE_PROXY_PASSWORD_FILE" "$HAKOPOD_ACCEPTANCE_TARGET_PROXY_PASSWORD_FILE" "$HAKOPOD_ACCEPTANCE_CANCELLATION_PROXY_PASSWORD_FILE" "$HAKOPOD_ACCEPTANCE_SPEC" "$HAKOPOD_ACCEPTANCE_TARGET_SPEC" "$HAKOPOD_ACCEPTANCE_CANCELLATION_TARGET_SPEC" "$HAKOPOD_ACCEPTANCE_IMAGES" "$HAKOPOD_ACCEPTANCE_IDENTITIES" "$HAKOPOD_ACCEPTANCE_GATE_ATTESTATION" "$HAKOPOD_ACCEPTANCE_CONTROL_PLANE_BRIDGE" "$HAKOPOD_ACCEPTANCE_OBJECT_STORE_FAULT_COMMAND_FILE"; do
  [ -f "$file" ] && [ ! -L "$file" ] || { echo 'an acceptance input is missing or symbolic' >&2; exit 2; }
done
[ "$(stat -c %a "$HAKOPOD_ACCEPTANCE_API_TOKEN_FILE")" = 600 ] || { echo 'API token file must be mode 600' >&2; exit 2; }
for file in "$HAKOPOD_ACCEPTANCE_SOURCE_PROXY_PASSWORD_FILE" "$HAKOPOD_ACCEPTANCE_TARGET_PROXY_PASSWORD_FILE" "$HAKOPOD_ACCEPTANCE_CANCELLATION_PROXY_PASSWORD_FILE"; do
  [ "$(stat -c %a "$file")" = 600 ] || { echo 'proxy password files must be mode 600' >&2; exit 2; }
done

exec python3 "$script_dir/driver.py" --source "$repo_root" --kubeconfig "$KUBECONFIG" \
  --api-url "$HAKOPOD_ACCEPTANCE_API_URL" --token-file "$HAKOPOD_ACCEPTANCE_API_TOKEN_FILE" \
  --source-spec "$HAKOPOD_ACCEPTANCE_SPEC" --target-spec "$HAKOPOD_ACCEPTANCE_TARGET_SPEC" \
  --cancellation-target-spec "$HAKOPOD_ACCEPTANCE_CANCELLATION_TARGET_SPEC" \
  --images "$HAKOPOD_ACCEPTANCE_IMAGES" --identities "$HAKOPOD_ACCEPTANCE_IDENTITIES" \
  --destination-id "$HAKOPOD_ACCEPTANCE_DESTINATION_ID" --destination-revision "$HAKOPOD_ACCEPTANCE_DESTINATION_REVISION" \
  --work-dir "$HAKOPOD_ACCEPTANCE_WORK_DIR" --gate-attestation "$HAKOPOD_ACCEPTANCE_GATE_ATTESTATION" \
  --project "$HAKOPOD_ACCEPTANCE_PROJECT" --managed-tls-helper "$HAKOPOD_ACCEPTANCE_MANAGED_TLS_HELPER" \
  --runtime-spec-digest-helper "$HAKOPOD_ACCEPTANCE_RUNTIME_SPEC_DIGEST_HELPER" \
  --control-psql-command-file "$HAKOPOD_ACCEPTANCE_CONTROL_PSQL_COMMAND_FILE" \
  --control-plane-bridge "$HAKOPOD_ACCEPTANCE_CONTROL_PLANE_BRIDGE" \
  --object-store-fault-command-file "$HAKOPOD_ACCEPTANCE_OBJECT_STORE_FAULT_COMMAND_FILE" \
  --psql "$HAKOPOD_ACCEPTANCE_PSQL" --source-proxy-password-file "$HAKOPOD_ACCEPTANCE_SOURCE_PROXY_PASSWORD_FILE" \
  --target-proxy-password-file "$HAKOPOD_ACCEPTANCE_TARGET_PROXY_PASSWORD_FILE" \
  --cancellation-proxy-password-file "$HAKOPOD_ACCEPTANCE_CANCELLATION_PROXY_PASSWORD_FILE" \
  --openssl "$HAKOPOD_ACCEPTANCE_OPENSSL" --local-port "$HAKOPOD_ACCEPTANCE_LOCAL_PORT"
