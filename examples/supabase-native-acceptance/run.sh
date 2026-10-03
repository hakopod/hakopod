#!/bin/sh
set -eu
umask 077
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)

expected_context=k3d-hakopod-dev
: "${KUBECONFIG:?set KUBECONFIG to the dedicated Hakopod development kubeconfig}"
: "${HAKOPOD_ACCEPTANCE_SPEC:?set the path to the disposable Supabase specification JSON}"
: "${HAKOPOD_ACCEPTANCE_ROTATION_SPEC:?set the path to the reviewed full-rotation specification JSON}"
: "${HAKOPOD_ACCEPTANCE_ROTATION_OLD_PASSWORD_FILE:?set the old database owner password file}"
: "${HAKOPOD_ACCEPTANCE_ROTATION_NEW_PASSWORD_FILE:?set the new database owner password file}"
: "${HAKOPOD_ACCEPTANCE_ROTATION_FAIL_ONCE_FILE:?set the acceptance-only rotation retry marker path}"
: "${HAKOPOD_ACCEPTANCE_ROTATION_RELEASE_FILE:?set the acceptance-only rotation release marker path}"
: "${HAKOPOD_ACCEPTANCE_SERVER_PID:?set the disposable management server PID}"
: "${HAKOPOD_ACCEPTANCE_SERVER_BINARY:?set the disposable management server binary}"
: "${HAKOPOD_ACCEPTANCE_PROJECT:?set the disposable project}"
: "${HAKOPOD_ACCEPTANCE_ENVIRONMENT:?set the disposable environment}"
: "${HAKOPOD_ACCEPTANCE_API_URL:?set the Hakopod API base URL}"
: "${HAKOPOD_ACCEPTANCE_API_TOKEN:?set a disposable Hakopod bearer key}"
: "${HAKOPOD_ACCEPTANCE_ANON_KEY:?set the disposable Supabase anon key}"
: "${HAKOPOD_ACCEPTANCE_SERVICE_KEY:?set the disposable Supabase service-role key}"
: "${HAKOPOD_ACCEPTANCE_IMAGES:?set the path to the reviewed image digest JSON}"
: "${HAKOPOD_ACCEPTANCE_IDENTITIES:?set the path to the digest-qualified component UID/GID JSON}"
: "${HAKOPOD_ACCEPTANCE_GATEWAY_CA:?set the path to the reviewed gateway CA certificate}"
: "${HAKOPOD_ACCEPTANCE_ADVERSARIAL_CERT_DIR:?set the protected database TLS fixture directory}"
: "${HAKOPOD_ACCEPTANCE_RECOVERY_DESTINATION_ID:?set the tested backup destination ID}"
: "${HAKOPOD_ACCEPTANCE_RECOVERY_DESTINATION_REVISION:?set its current revision}"
: "${HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_ID:?set a separate empty Supabase target ID}"
: "${HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_REVISION:?set its current revision}"
: "${HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_NAME:?set its exact name}"
: "${HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_SERVICE_KEY:?set its disposable service-role key}"
: "${HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_GATEWAY_CA:?set its reviewed gateway CA}"
: "${HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_DISPOSABLE:?set to 1 for the disposable restore target}"
: "${HAKOPOD_ACCEPTANCE_DISPOSABLE:?set HAKOPOD_ACCEPTANCE_DISPOSABLE=1 for an isolated disposable fixture}"
[ "$HAKOPOD_ACCEPTANCE_DISPOSABLE" = 1 ] || { echo "acceptance requires an explicit disposable fixture marker" >&2; exit 2; }
[ "$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_DISPOSABLE" = 1 ] || { echo "acceptance requires an explicitly disposable recovery target" >&2; exit 2; }
for command in kubectl curl jq websocat sha256sum openssl python3 docker go timeout; do command -v "$command" >/dev/null 2>&1 || { echo "$command is required" >&2; exit 2; }; done
kubectl_bin=$(command -v kubectl)
kubectl() { command "$kubectl_bin" --request-timeout=15s "$@"; }
context=$(kubectl --kubeconfig "$KUBECONFIG" config current-context)
[ "$context" = "$expected_context" ] || { echo "refusing Kubernetes context: $context" >&2; exit 2; }
target_namespace=managed-platform-$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_ID
target_namespace_json=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get namespace "$target_namespace" -o json)
target_namespace_uid=$(printf '%s' "$target_namespace_json" | jq -er --arg id "$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_ID" '.metadata as $m | if $m.labels["hakopod.io/managed-platform-id"]==$id then $m.uid else error("recovery target namespace ownership is invalid") end')
jq -e '.schema_version==1 and .kind=="supabase"' "$HAKOPOD_ACCEPTANCE_SPEC" >/dev/null
jq -e '.secrets["pooler-api-jwt-secret"] != null and .secrets["pooler-api-jwt-secret"] != .secrets["jwt-secret"]' "$HAKOPOD_ACCEPTANCE_SPEC" >/dev/null || { echo "pooler administrative JWT must use a separate immutable secret reference" >&2; exit 3; }
gateway_host=$(jq -er '.supabase.public_url | sub("^https://";"") | sub("/$";"")' "$HAKOPOD_ACCEPTANCE_SPEC")
[ -n "$gateway_host" ] && ! printf '%s' "$gateway_host" | grep -q '[:/]' || { echo "public_url must be an exact HTTPS origin on port 443" >&2; exit 2; }
[ -s "$HAKOPOD_ACCEPTANCE_GATEWAY_CA" ] || { echo "gateway CA is unavailable" >&2; exit 2; }
jq -e --slurpfile pins "$HAKOPOD_ACCEPTANCE_IMAGES" 'type=="object" and length==11 and all(to_entries[]; (.value.uid|type)=="number" and .value.uid>0 and (.value.gid|type)=="number" and .value.gid>0 and .value.image==$pins[0][.key])' "$HAKOPOD_ACCEPTANCE_IDENTITIES" >/dev/null || { echo "all eleven digest-qualified non-root identities are required" >&2; exit 3; }
secret_dir=$(mktemp -d /tmp/hakopod-supabase-secrets.XXXXXX)
chmod 700 "$secret_dir"
api_headers=$secret_dir/api.headers
anon_headers=$secret_dir/anon.headers
service_headers=$secret_dir/service.headers
target_service_headers=$secret_dir/target-service.headers
printf 'authorization: Bearer %s\n' "$HAKOPOD_ACCEPTANCE_API_TOKEN" >"$api_headers"
printf 'apikey: %s\n' "$HAKOPOD_ACCEPTANCE_ANON_KEY" >"$anon_headers"
printf 'apikey: %s\nauthorization: Bearer %s\n' "$HAKOPOD_ACCEPTANCE_SERVICE_KEY" "$HAKOPOD_ACCEPTANCE_SERVICE_KEY" >"$service_headers"
printf 'apikey: %s\nauthorization: Bearer %s\n' "$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_SERVICE_KEY" "$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_SERVICE_KEY" >"$target_service_headers"
chmod 600 "$api_headers" "$anon_headers" "$service_headers" "$target_service_headers"
unset HAKOPOD_ACCEPTANCE_API_TOKEN HAKOPOD_ACCEPTANCE_ANON_KEY HAKOPOD_ACCEPTANCE_SERVICE_KEY HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_SERVICE_KEY
restarted_server_pid=
cleanup_local() { [ -z "$restarted_server_pid" ] || kill "$restarted_server_pid" 2>/dev/null || true; rm -rf "$secret_dir"; }
trap cleanup_local EXIT HUP INT TERM
if [ -n "${HAKOPOD_ACCEPTANCE_CREATE_REVIEW_FILE:-}" ]; then
  create_review=$(python3 "$script_dir/review.py" --review "$HAKOPOD_ACCEPTANCE_CREATE_REVIEW_FILE" --spec "$HAKOPOD_ACCEPTANCE_SPEC" --project "$HAKOPOD_ACCEPTANCE_PROJECT" --environment "$HAKOPOD_ACCEPTANCE_ENVIRONMENT")
else
  create_review=$(jq -n --slurpfile spec "$HAKOPOD_ACCEPTANCE_SPEC" --arg project "$HAKOPOD_ACCEPTANCE_PROJECT" --arg environment "$HAKOPOD_ACCEPTANCE_ENVIRONMENT" '{project:$project,environment:$environment,expected_revision:0,kind:"create",spec:$spec[0]}' | curl --max-filesize 1048576 --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/reviews" --header @"$api_headers" -H 'content-type: application/json' --data-binary @-)
fi
printf '%s' "$create_review" | jq -e '.blocked==false and .review.id!=null' >/dev/null || { echo "Supabase capability gate remains closed; acceptance did not run" >&2; exit 3; }
HAKOPOD_ACCEPTANCE_PLATFORM_ID=$(printf '%s' "$create_review" | jq -er '.platform.id')
export HAKOPOD_ACCEPTANCE_PLATFORM_ID
namespace=managed-platform-$HAKOPOD_ACCEPTANCE_PLATFORM_ID
expected_name=$(printf '%s' "$create_review" | jq -er '.platform.spec.name')
work_dir=/tmp/hakopod-supabase-native-$HAKOPOD_ACCEPTANCE_PLATFORM_ID
[ ! -e "$work_dir" ] && [ ! -L "$work_dir" ] || { echo "acceptance work directory already exists" >&2; exit 2; }
mkdir -m 700 "$work_dir"
evidence_dir=$work_dir/evidence
mkdir -m 700 "$evidence_dir"
evidence_state=$evidence_dir/state.json
evidence_events=$evidence_dir/events.json
evidence() { python3 "$script_dir/evidence.py" "$@"; }
record_case() {
  evidence event --state "$evidence_state" --events "$evidence_events" --case "$1" --evidence "$2"
}
target_create_file=$evidence_dir/target-create.json
forward_pid=
realtime_pid=
recovery_forward_pid=
cleanup_armed=0
target_cleanup_armed=1
create_operation_id=
create_idempotency=hakopod-test-create-$HAKOPOD_ACCEPTANCE_PLATFORM_ID
create_request=$work_dir/create-request.json
printf '%s' "$create_review" | jq '{id:.platform.id,project:.platform.project,environment:.platform.environment,expected_revision:0,kind:"create",spec:.platform.spec,review:.review}' >"$create_request"
cleanup_platform() {
  if [ -z "$create_operation_id" ]; then
    replay=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/operations" --header @"$api_headers" -H "Idempotency-Key: $create_idempotency" -H 'content-type: application/json' --data-binary @"$create_request") || return 1
    create_operation_id=$(printf '%s' "$replay" | jq -er '.id') || return 1
  fi
  tries=0
  while [ "$tries" -lt 150 ]; do
    state=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-operations/$create_operation_id" --header @"$api_headers" | jq -er '.status') || return 1
    [ "$state" = queued ] || [ "$state" = running ] || break
    tries=$((tries+1)); sleep 2
  done
  [ "$tries" -lt 150 ] || { echo "cleanup waited too long for create operation" >&2; return 1; }
  current=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --header @"$api_headers") || return 1
  printf '%s' "$current" | jq -e --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --arg project "$HAKOPOD_ACCEPTANCE_PROJECT" --arg environment "$HAKOPOD_ACCEPTANCE_ENVIRONMENT" --arg name "$expected_name" '.id==$id and .project==$project and .environment==$environment and .spec.name==$name' >/dev/null || return 1
  revision=$(printf '%s' "$current" | jq -er '.revision')
  cleanup_review=$(printf '%s' "$current" | jq --argjson revision "$revision" '{id:.id,project:.project,environment:.environment,expected_revision:$revision,kind:"delete",confirm_name:.spec.name,spec:.spec}' | curl --max-filesize 1048576 --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/reviews" --header @"$api_headers" -H 'content-type: application/json' --data-binary @-) || return 1
  printf '%s' "$cleanup_review" | jq -e '.blocked==false and .review.id!=null' >/dev/null || return 1
  cleanup_operation=$(printf '%s' "$cleanup_review" | jq '{id:.platform.id,project:.platform.project,environment:.platform.environment,expected_revision:.review.expected_revision,kind:"delete",confirm_name:.platform.spec.name,spec:.platform.spec,review:.review}' | curl --max-filesize 1048576 --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/operations" --header @"$api_headers" -H "Idempotency-Key: hakopod-test-cleanup-$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -H 'content-type: application/json' --data-binary @-) || return 1
  cleanup_operation_id=$(printf '%s' "$cleanup_operation" | jq -er '.id')
  tries=0
  while [ "$tries" -lt 180 ]; do
    state=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-operations/$cleanup_operation_id" --header @"$api_headers" | jq -er '.status') || return 1
    [ "$state" = succeeded ] && break
    [ "$state" != failed ] && [ "$state" != cancelled ] || return 1
    tries=$((tries+1)); sleep 2
  done
  [ "$state" = succeeded ] || return 1
  [ -z "$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get namespace "$namespace" --ignore-not-found -o name)" ] || return 1
  kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get pv --chunk-size=128 -o json | jq -e --arg ns "$namespace" 'all(.items[]; .spec.claimRef.namespace!=$ns)' >/dev/null
}
cleanup_target() {
  current=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_ID" --header @"$api_headers") || return 1
  printf '%s' "$current" | jq -e --arg id "$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_ID" --arg project "$HAKOPOD_ACCEPTANCE_PROJECT" --arg environment "$HAKOPOD_ACCEPTANCE_ENVIRONMENT" --arg name "$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_NAME" '.id==$id and .project==$project and .environment==$environment and .spec.name==$name' >/dev/null || return 1
  target_revision=$(printf '%s' "$current" | jq -er '.revision')
  target_review=$(printf '%s' "$current" | jq --argjson revision "$target_revision" '{id:.id,project:.project,environment:.environment,expected_revision:$revision,kind:"delete",confirm_name:.spec.name,spec:.spec}' | curl --max-filesize 1048576 --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/reviews" --header @"$api_headers" -H 'content-type: application/json' --data-binary @-) || return 1
  target_operation=$(printf '%s' "$target_review" | jq '{id:.platform.id,project:.platform.project,environment:.platform.environment,expected_revision:.review.expected_revision,kind:"delete",confirm_name:.platform.spec.name,spec:.platform.spec,review:.review}' | curl --max-filesize 1048576 --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/operations" --header @"$api_headers" -H "Idempotency-Key: hakopod-test-delete-$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_ID" -H 'content-type: application/json' --data-binary @-) || return 1
  target_operation_id=$(printf '%s' "$target_operation" | jq -er '.id') || return 1
  tries=0
  while [ "$tries" -lt 180 ]; do
    state=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-operations/$target_operation_id" --header @"$api_headers" | jq -er '.status') || return 1
    [ "$state" = succeeded ] && break
    [ "$state" != failed ] && [ "$state" != cancelled ] || return 1
    tries=$((tries+1)); sleep 2
  done
  [ "$state" = succeeded ] || return 1
  [ -z "$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get namespace "$target_namespace" --ignore-not-found -o name)" ] || return 1
  kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get pv --chunk-size=128 -o json | jq -e --arg ns "$target_namespace" 'all(.items[]; .spec.claimRef.namespace!=$ns)' >/dev/null
}
cleanup() {
  status=$?
  trap - EXIT HUP INT TERM
  if [ -n "$forward_pid" ]; then kill "$forward_pid" 2>/dev/null || true; wait "$forward_pid" 2>/dev/null || true; fi
  if [ -n "$realtime_pid" ]; then kill "$realtime_pid" 2>/dev/null || true; wait "$realtime_pid" 2>/dev/null || true; fi
  if [ -n "$recovery_forward_pid" ]; then kill "$recovery_forward_pid" 2>/dev/null || true; wait "$recovery_forward_pid" 2>/dev/null || true; fi
  if [ "$cleanup_armed" -eq 1 ]; then cleanup_platform || { echo "owned Supabase cleanup failed" >&2; [ "$status" -ne 0 ] || status=1; }; fi
  if [ "$target_cleanup_armed" -eq 1 ]; then cleanup_target || { echo "owned recovery target cleanup failed" >&2; [ "$status" -ne 0 ] || status=1; }; fi
  if [ "$status" -ne 0 ]; then evidence fail --state "$evidence_state" --code native-check-or-cleanup-failed || true; fi
  rm -rf "$secret_dir"
  exit "$status"
}
cleanup_armed=1
trap cleanup EXIT
trap 'exit 130' HUP INT TERM
run_id=$(evidence begin --source "$repo_root" --images "$HAKOPOD_ACCEPTANCE_IMAGES" --state "$evidence_state" --events "$evidence_events")
curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_ID/operations" --header @"$api_headers" |
  jq -e --arg id "$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_ID" '[.items[] | select(.platform_id==$id and .kind=="create" and .status=="succeeded")]|if length==1 then .[0]|{id,platform_id,kind,status} else error("recovery target create operation is not unique") end' >"$target_create_file"
evidence bind-resource --state "$evidence_state" --role recovery_target --operation-observation "$target_create_file"
HAKOPOD_OWNERSHIP_SOURCE="$repo_root" HAKOPOD_OWNERSHIP_EVIDENCE="$evidence_dir/ownership-fencing.json" HAKOPOD_TEST_KUBECONFIG="$KUBECONFIG" "$script_dir/ownership-check.sh"
record_case ownership-fencing "$evidence_dir/ownership-fencing.json"
create_operation=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/operations" --header @"$api_headers" -H "Idempotency-Key: $create_idempotency" -H 'content-type: application/json' --data-binary @"$create_request")
create_operation_id=$(printf '%s' "$create_operation" | jq -er '.id')
tries=0
while :; do create_status=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-operations/$create_operation_id" --header @"$api_headers" | jq -er '.status'); [ "$create_status" = succeeded ] && break; [ "$create_status" != failed ] && [ "$create_status" != cancelled ] || { echo "Supabase creation failed" >&2; exit 1; }; tries=$((tries+1)); [ "$tries" -lt 180 ] || { echo "Supabase creation timed out" >&2; exit 1; }; sleep 2; done
jq -e 'type=="object" and length==11 and all(.[];test("^[^@]+@sha256:[0-9a-f]{64}$"))' "$HAKOPOD_ACCEPTANCE_IMAGES" >/dev/null
platform=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --header @"$api_headers")
namespace_uid=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get namespace "$namespace" -o jsonpath='{.metadata.uid}')
[ -n "$namespace_uid" ]
namespace_owner_operation_id=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get namespace "$namespace" -o jsonpath='{.metadata.labels.hakopod\.io/owner-operation-id}')
namespace_resource_intent_id=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get namespace "$namespace" -o jsonpath='{.metadata.labels.hakopod\.io/resource-intent-id}')
[ "$namespace_owner_operation_id" = "$create_operation_id" ] && [ -n "$namespace_resource_intent_id" ]
source_create_file=$evidence_dir/source-create.json
curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-operations/$create_operation_id" --header @"$api_headers" | jq -e '{id,platform_id,kind,status}' >"$source_create_file"
evidence bind-resource --state "$evidence_state" --role source --operation-observation "$source_create_file"
application_jwt_ref=$(jq -er '.secrets["jwt-secret"] | .name + "-r" + (.revision|tostring)' "$HAKOPOD_ACCEPTANCE_SPEC")
pooler_api_jwt_ref=$(jq -er '.secrets["pooler-api-jwt-secret"] | .name + "-r" + (.revision|tostring)' "$HAKOPOD_ACCEPTANCE_SPEC")
[ "$application_jwt_ref" != "$pooler_api_jwt_ref" ]
application_jwt_sha=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get secret "$application_jwt_ref" -o jsonpath='{.data.value}' | base64 -d | sha256sum | awk '{print $1}')
pooler_api_jwt_sha=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get secret "$pooler_api_jwt_ref" -o jsonpath='{.data.value}' | base64 -d | sha256sum | awk '{print $1}')
[ "$application_jwt_sha" != "$pooler_api_jwt_sha" ] || { echo "pooler administrative and application JWT secret bodies are shared" >&2; exit 1; }
tls_ref=$(jq -er '.secrets["gateway-tls-certificate"] | .name + "-r" + (.revision|tostring)' "$HAKOPOD_ACCEPTANCE_SPEC")
database_tls_ref=$(jq -er '.secrets["database-tls-certificate"] | .name + "-r" + (.revision|tostring)' "$HAKOPOD_ACCEPTANCE_SPEC")
runtime_ref=$(jq -er '.secrets["envoy-runtime-config"] | .name + "-r" + (.revision|tostring)' "$HAKOPOD_ACCEPTANCE_SPEC")
[ "$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get secret "$tls_ref" -o jsonpath='{.immutable}')" = true ]
tls_keys=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get secret "$tls_ref" -o go-template='{{range $key,$value := .data}}{{$key}}{{"\n"}}{{end}}' | sort)
[ "$tls_keys" = "ca.crt
tls.crt
tls.key" ] || { echo "gateway TLS snapshot contains an unexpected key inventory" >&2; exit 1; }
mounted_ca_sha=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get secret "$tls_ref" -o jsonpath='{.data.ca\.crt}' | base64 -d | sha256sum | awk '{print $1}')
reviewed_ca_sha=$(sha256sum "$HAKOPOD_ACCEPTANCE_GATEWAY_CA" | awk '{print $1}')
[ "$mounted_ca_sha" = "$reviewed_ca_sha" ] || { echo "reviewed CA does not match mounted gateway trust" >&2; exit 1; }
[ "$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get secret "$runtime_ref" -o jsonpath='{.immutable}')" = true ]
[ "$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get secret "$runtime_ref" -o go-template='{{range $key,$value := .data}}{{$key}}{{"\n"}}{{end}}')" = value ]
workloads_file=$work_dir/workloads.json
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get deployment,statefulset -o json >"$workloads_file"
jq -e --slurpfile pins "$HAKOPOD_ACCEPTANCE_IMAGES" --slurpfile ids "$HAKOPOD_ACCEPTANCE_IDENTITIES" '
  (.items | length)==11 and
  (.items | map(.metadata.labels["app.kubernetes.io/component"]) | unique | length)==11 and
  (.items | map({key:.metadata.labels["app.kubernetes.io/component"],value:.spec.template.spec.containers[0].image}) | from_entries) == $pins[0] and
  all(.items[]; .metadata.labels["app.kubernetes.io/component"] as $component |
    .spec.template.spec.automountServiceAccountToken == false and
    .spec.template.spec.enableServiceLinks == false and
    .spec.template.spec.securityContext.runAsNonRoot == true and
    .spec.template.spec.securityContext.runAsUser == $ids[0][$component].uid and
    .spec.template.spec.securityContext.runAsGroup == $ids[0][$component].gid and
    .spec.template.spec.securityContext.seccompProfile.type == "RuntimeDefault" and
    (if $component=="api-gateway" then
      (.spec.template.spec.containers[0].env // [] | all(.[]; .name != "GATEWAY_TLS_CERTIFICATE")) and
      (.spec.template.spec.containers[0].volumeMounts | any(.[]; .name=="gateway-tls" and .mountPath=="/etc/envoy/tls" and .readOnly==true))
    else true end) and
    all((.spec.template.spec.containers + (.spec.template.spec.initContainers // []))[];
      .securityContext.runAsNonRoot == true and
      .securityContext.runAsUser == $ids[0][$component].uid and
      .securityContext.runAsGroup == $ids[0][$component].gid and
      .securityContext.readOnlyRootFilesystem == true and
      .securityContext.allowPrivilegeEscalation == false and
      (.securityContext.capabilities.add // [] | length) == 0 and
      (.securityContext.capabilities.drop | index("ALL")) != null and
      .securityContext.seccompProfile.type == "RuntimeDefault"))' "$workloads_file" >/dev/null
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get service api-gw -o json | jq -e '.spec.type=="ClusterIP" and (.spec.ports|length)==1 and .spec.ports[0].port==8443 and .spec.ports[0].targetPort==8443' >/dev/null
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get networkpolicy supabase-envoy-ingress -o json | jq -e '[.spec.ingress[].ports[].port] == [8443]' >/dev/null
pooler_digest=$(jq -er '.pooler | capture("@(?<digest>sha256:[0-9a-f]{64})$").digest' "$HAKOPOD_ACCEPTANCE_IMAGES")
KUBECTL_BIN=kubectl \
  HAKOPOD_SUPABASE_NAMESPACE="$namespace" \
  HAKOPOD_SUPAVISOR_MANIFEST_DIGEST="$pooler_digest" \
  "$script_dir/pooler-tls.sh" >"$evidence_dir/pooler-tls-observation.txt"

pvc_before=$work_dir/pvc-before.json
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get pvc -o json | jq -S '[.items[] | {name:.metadata.name,uid:.metadata.uid,volume:.spec.volumeName,storage_class:.spec.storageClassName,access_modes:.spec.accessModes,request:.spec.resources.requests.storage}] | sort_by(.name)' >"$pvc_before"
[ "$(jq length "$pvc_before")" -eq 5 ] && jq -e 'all(.[]; .uid!="" and .volume!="" and .storage_class!="" and (.access_modes|length)>0 and .request!="")' "$pvc_before" >/dev/null
pv_before=$work_dir/pv-before.json
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get pv $(jq -r '.[].volume' "$pvc_before") -o json | jq -S '[.items[] | {name:.metadata.name,uid:.metadata.uid,claim_namespace:.spec.claimRef.namespace,claim_name:.spec.claimRef.name,claim_uid:.spec.claimRef.uid}] | sort_by(.name)' >"$pv_before"
[ "$(jq length "$pv_before")" -eq 5 ] && jq -e --arg ns "$namespace" 'all(.[]; .uid!="" and .claim_namespace==$ns and .claim_name!="" and .claim_uid!="")' "$pv_before" >/dev/null
jq -e --slurpfile pvc "$pvc_before" 'all(.[]; . as $pv | any($pvc[0][]; .name==$pv.claim_name and .uid==$pv.claim_uid and .volume==$pv.name))' "$pv_before" >/dev/null
record_case create-owned-resources "$workloads_file"

for pod in $(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get pods -l "hakopod.io/managed-platform-id=$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -o name); do
  component=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get "$pod" -o jsonpath='{.metadata.labels.app\.kubernetes\.io/component}')
  expected_uid=$(jq -er --arg c "$component" '.[$c].uid' "$HAKOPOD_ACCEPTANCE_IDENTITIES")
  expected_gid=$(jq -er --arg c "$component" '.[$c].gid' "$HAKOPOD_ACCEPTANCE_IDENTITIES")
  process=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec "$pod" -- /bin/sh -ceu 'printf "%s:%s\n" "$(id -u)" "$(id -g)"; awk "/^(CapInh|CapPrm|CapEff|CapBnd|CapAmb|NoNewPrivs|Seccomp):/{print}" /proc/1/status; if : > /hakopod-rootfs-write-test 2>/dev/null; then exit 41; fi')
  printf '%s\n' "$process" | grep -Fx "$expected_uid:$expected_gid" >/dev/null
  printf '%s\n' "$process" | awk '/^Cap(Inh|Prm|Eff|Bnd|Amb):/{if ($2!="0000000000000000") exit 1} /^NoNewPrivs:/{if ($2!="1") exit 1; seen=1} /^Seccomp:/{if ($2!="2") exit 1; seccomp=1} END{exit seen&&seccomp?0:1}'
  evidence observe-identity --state "$evidence_state" --role source --component "$component" --pod "${pod#pod/}"
done
record_case component-image-and-identity "$evidence_state"
record_case pooler-admin-jwt-isolation "$evidence_dir/pooler-tls-observation.txt"
HAKOPOD_ACCEPTANCE_NAMESPACE_UID="$namespace_uid" HAKOPOD_ACCEPTANCE_OWNER_OPERATION_ID="$namespace_owner_operation_id" HAKOPOD_ACCEPTANCE_RESOURCE_INTENT_ID="$namespace_resource_intent_id" "$script_dir/pooler-bounds.sh" >"$evidence_dir/pooler-connection-bounds.json"
jq -e '.status=="passed" and .client_overflow_refused and .backend_bound_observed and .released_connection_reused' "$evidence_dir/pooler-connection-bounds.json" >/dev/null
record_case pooler-connection-bounds "$evidence_dir/pooler-connection-bounds.json"

capture_logs() {
  phase=$1
  log_dir=$work_dir/logs-$phase
  mkdir -m 700 "$log_dir"
  for log_pod in $(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get pods --chunk-size=32 -l "hakopod.io/managed-platform-id=$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -o name); do
    log_name=${log_pod#pod/}
    kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" logs "$log_pod" --all-containers --tail=2000 --limit-bytes=2097152 >"$log_dir/$log_name.current.log"
  done
}
secret_in_tree() {
  python3 - "$1" "$2" <<'PY'
import pathlib, sys
needle = pathlib.Path(sys.argv[1]).read_bytes()
if not needle:
    raise SystemExit(1)
for path in pathlib.Path(sys.argv[2]).rglob("*"):
    if path.is_file() and needle in path.read_bytes():
        raise SystemExit(0)
raise SystemExit(1)
PY
}
capture_logs before-replacement

port=${HAKOPOD_ACCEPTANCE_LOCAL_PORT:-18443}
port_forward_log=$work_dir/port-forward.log
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" port-forward service/api-gw "$port:8443" >"$port_forward_log" 2>&1 &
forward_pid=$!
base=https://$gateway_host:$port
curl_tls="--noproxy $gateway_host --resolve $gateway_host:$port:127.0.0.1 --cacert $HAKOPOD_ACCEPTANCE_GATEWAY_CA"
tries=0
until openssl s_client -connect "127.0.0.1:$port" -servername "$gateway_host" -CAfile "$HAKOPOD_ACCEPTANCE_GATEWAY_CA" -verify_return_error </dev/null 2>/dev/null | grep -F 'Verify return code: 0 (ok)' >/dev/null; do tries=$((tries+1)); [ "$tries" -lt 30 ] || { echo "Supabase gateway TLS identity did not become verifiable" >&2; exit 1; }; sleep 1; done
if curl --silent --show-error --max-time 3 "http://127.0.0.1:$port/auth/v1/health" >/dev/null 2>&1; then echo "Supabase gateway served plaintext on its TLS listener" >&2; exit 1; fi
tries=0
until curl $curl_tls --fail --silent --max-time 2 "$base/auth/v1/health" >/dev/null; do tries=$((tries+1)); [ "$tries" -lt 30 ] || { echo "Supabase gateway did not become reachable" >&2; exit 1; }; sleep 1; done
gateway_observation=$evidence_dir/gateway-tls.json
jq -n --arg host "$gateway_host" --arg ca_sha256 "$reviewed_ca_sha" --arg uid "$namespace_uid" '{hostname:$host,ca_sha256:$ca_sha256,namespace_uid:$uid,tls_verified:true,plaintext_refused:true}' >"$gateway_observation"
record_case gateway-tls "$gateway_observation"

signup=$(curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/auth/v1/signup" --header @"$anon_headers" -H 'content-type: application/json' --data '{"data":{"acceptance":"hakopod-test-native-owner"}}')
access_token=$(printf '%s' "$signup" | jq -er '.access_token')
user_id=$(printf '%s' "$signup" | jq -er '.user.id')
second_signup=$(curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/auth/v1/signup" --header @"$anon_headers" -H 'content-type: application/json' --data '{"data":{"acceptance":"hakopod-test-native-other"}}')
second_access_token=$(printf '%s' "$second_signup" | jq -er '.access_token')
second_user_id=$(printf '%s' "$second_signup" | jq -er '.user.id')
[ -n "$access_token" ] && [ -n "$second_access_token" ] && [ "$user_id" != "$second_user_id" ]
owner_headers=$secret_dir/owner.headers
second_headers=$secret_dir/second.headers
cat "$anon_headers" >"$owner_headers"
printf 'authorization: Bearer %s\n' "$access_token" >>"$owner_headers"
cat "$anon_headers" >"$second_headers"
printf 'authorization: Bearer %s\n' "$second_access_token" >>"$second_headers"
chmod 600 "$owner_headers" "$second_headers"
studio_headers=$secret_dir/studio.headers
dashboard_username_ref=$(jq -er '.secrets["dashboard-username"] | .name + "-r" + (.revision|tostring)' "$HAKOPOD_ACCEPTANCE_SPEC")
dashboard_password_ref=$(jq -er '.secrets["dashboard-password"] | .name + "-r" + (.revision|tostring)' "$HAKOPOD_ACCEPTANCE_SPEC")
{
  kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get secret "$dashboard_username_ref" -o jsonpath='{.data.value}' | base64 -d
  printf ':'
  kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get secret "$dashboard_password_ref" -o jsonpath='{.data.value}' | base64 -d
} >"$secret_dir/studio-basic-value"
{ printf 'authorization: Basic '; base64 <"$secret_dir/studio-basic-value" | tr -d '\n'; printf '\n'; } >"$studio_headers"
chmod 600 "$studio_headers"
export HAKOPOD_BEHAVIOR_BASE_URL="$base" HAKOPOD_BEHAVIOR_HOST="$gateway_host" HAKOPOD_BEHAVIOR_PORT="$port" HAKOPOD_BEHAVIOR_CA="$HAKOPOD_ACCEPTANCE_GATEWAY_CA"
export HAKOPOD_BEHAVIOR_ANON_HEADERS="$anon_headers" HAKOPOD_BEHAVIOR_OWNER_HEADERS="$owner_headers" HAKOPOD_BEHAVIOR_SERVICE_HEADERS="$service_headers" HAKOPOD_BEHAVIOR_EVIDENCE_DIR="$evidence_dir"
export HAKOPOD_BEHAVIOR_STUDIO_HEADERS="$studio_headers"
"$script_dir/behavior-checks.sh" auth
record_case auth-redirect-and-signup "$evidence_dir/auth-redirect-and-signup.json"

row_id=hakopod-test-$(date +%s)
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec statefulset/supabase-database -- psql -U postgres -v ON_ERROR_STOP=1 -c 'create table if not exists public.hakopod_acceptance (id text primary key, owner uuid not null default auth.uid(), value text not null); alter table public.hakopod_acceptance enable row level security; drop policy if exists hakopod_acceptance_owner on public.hakopod_acceptance; create policy hakopod_acceptance_owner on public.hakopod_acceptance using (owner = auth.uid()) with check (owner = auth.uid()); grant select,insert,update,delete on public.hakopod_acceptance to anon, authenticated, service_role; do $$ begin if not exists (select 1 from pg_publication_tables where pubname='"'"'supabase_realtime'"'"' and schemaname='"'"'public'"'"' and tablename='"'"'hakopod_acceptance'"'"') then alter publication supabase_realtime add table public.hakopod_acceptance; end if; end $$;' >/dev/null
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec -i statefulset/supabase-database -- psql -U postgres -v ON_ERROR_STOP=1 >/dev/null <<'SQL'
DO $hakopod$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_roles
    WHERE rolname = 'hakopod_realtime' AND rolcanlogin AND NOT rolsuper
      AND NOT rolcreatedb AND NOT rolcreaterole AND rolreplication AND NOT rolbypassrls
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_roles
    WHERE rolname = 'hakopod_meta' AND rolcanlogin AND NOT rolsuper
      AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolreplication AND NOT rolbypassrls
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_roles
    WHERE rolname = 'supabase_realtime_admin' AND NOT rolcanlogin AND NOT rolsuper
      AND NOT rolinherit AND NOT rolcreatedb AND NOT rolcreaterole
      AND NOT rolreplication AND NOT rolbypassrls
  ) THEN
    RAISE EXCEPTION 'custom service role attributes differ';
  END IF;
  IF NOT EXISTS (
       SELECT 1 FROM pg_auth_members membership
       JOIN pg_roles member_role ON member_role.oid = membership.member
       JOIN pg_roles granted_role ON granted_role.oid = membership.roleid
       WHERE member_role.rolname = 'hakopod_realtime'
         AND granted_role.rolname = 'supabase_realtime_admin'
         AND membership.admin_option
     ) OR 2 <> (
       SELECT count(*) FROM pg_auth_members membership
       JOIN pg_roles granted_role ON granted_role.oid = membership.roleid
       WHERE granted_role.rolname = 'supabase_realtime_admin'
     ) OR EXISTS (
       SELECT 1 FROM pg_auth_members membership
       JOIN pg_roles member_role ON member_role.oid = membership.member
       JOIN pg_roles granted_role ON granted_role.oid = membership.roleid
       WHERE member_role.rolname IN ('supabase_realtime_admin', 'hakopod_meta')
          OR (member_role.rolname = 'hakopod_realtime'
              AND granted_role.rolname <> 'supabase_realtime_admin')
          OR granted_role.rolname IN ('hakopod_realtime', 'hakopod_meta')
          OR (granted_role.rolname = 'supabase_realtime_admin'
              AND member_role.rolname NOT IN ('postgres', 'hakopod_realtime'))
     ) THEN
    RAISE EXCEPTION 'custom service role membership differs';
  END IF;
  IF NOT has_database_privilege('hakopod_realtime', 'postgres', 'CONNECT')
     OR NOT has_database_privilege('hakopod_realtime', 'postgres', 'CREATE')
     OR NOT has_database_privilege('hakopod_meta', 'postgres', 'CONNECT')
     OR has_database_privilege('hakopod_meta', 'postgres', 'CREATE')
     OR NOT has_schema_privilege('hakopod_meta', 'public', 'USAGE')
     OR NOT has_schema_privilege('hakopod_meta', 'public', 'CREATE')
     OR NOT has_table_privilege('hakopod_meta', 'public.hakopod_acceptance', 'SELECT')
     OR has_table_privilege('hakopod_meta', 'public.hakopod_acceptance', 'INSERT')
     OR has_table_privilege('hakopod_meta', 'public.hakopod_acceptance', 'UPDATE')
     OR has_table_privilege('hakopod_meta', 'public.hakopod_acceptance', 'DELETE') THEN
    RAISE EXCEPTION 'custom service role privileges differ';
  END IF;
  IF (SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname = 'realtime') <> 'supabase_realtime_admin'
     OR (SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname = '_realtime') <> 'supabase_realtime_admin' THEN
    RAISE EXCEPTION 'realtime schema ownership differs';
  END IF;
  IF NOT EXISTS (
    SELECT 1
    FROM _realtime.tenants
    WHERE external_id = 'realtime-dev' AND jwt_secret LIKE 'g1:%'
  ) OR NOT EXISTS (
    SELECT 1
    FROM _realtime.extensions
    WHERE tenant_external_id = 'realtime-dev'
      AND type = 'postgres_cdc_rls'
      AND settings->>'ssl_enforced' = 'true'
      AND settings->>'db_host' LIKE 'g1:%'
      AND settings->>'db_password' LIKE 'g1:%'
  ) THEN
    RAISE EXCEPTION 'realtime secure encryption or tenant TLS policy differs';
  END IF;
END
$hakopod$;
SQL
curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/rest/v1/hakopod_acceptance" --header @"$owner_headers" -H 'content-type: application/json' -H 'prefer: return=minimal' --data "{\"id\":\"$row_id\",\"value\":\"before-restart\"}"
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/rest/v1/hakopod_acceptance?id=eq.$row_id&select=id,value" --header @"$service_headers" | jq -e --arg id "$row_id" 'length==1 and .[0].id==$id and .[0].value=="before-restart"' >/dev/null
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/rest/v1/hakopod_acceptance?id=eq.$row_id&select=id" --header @"$anon_headers" | jq -e 'length==0' >/dev/null
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/rest/v1/hakopod_acceptance?id=eq.$row_id&select=id" --header @"$second_headers" | jq -e 'length==0' >/dev/null
second_update=$(curl $curl_tls --fail --silent --show-error --max-time 10 -X PATCH "$base/rest/v1/hakopod_acceptance?id=eq.$row_id" --header @"$second_headers" -H 'content-type: application/json' -H 'prefer: return=representation' --data '{"value":"forbidden-cross-user-write"}')
[ "$second_update" = '[]' ]
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/rest/v1/hakopod_acceptance?id=eq.$row_id&select=value" --header @"$owner_headers" | jq -e 'length==1 and .[0].value=="before-restart"' >/dev/null
row_observation=$evidence_dir/row-isolation.json
jq -n --arg id "$row_id" --arg owner "$user_id" --arg other "$second_user_id" '{row_id:$id,owner_id:$owner,other_user_id:$other,owner_read:true,service_read:true,anonymous_read_refused:true,other_user_read_refused:true,other_user_write_refused:true}' >"$row_observation"
record_case jwt-role-isolation "$row_observation"
record_case row-level-security "$row_observation"

printf '{"topic":"realtime:public:hakopod_acceptance","event":"phx_join","payload":{"config":{"broadcast":{"ack":false,"self":false},"presence":{"enabled":false},"postgres_changes":[]},"access_token":"%s"},"ref":"1"}\n' "$access_token" | SSL_CERT_FILE="$HAKOPOD_ACCEPTANCE_GATEWAY_CA" timeout 10 websocat -1 -t --tls-domain="$gateway_host" "wss://127.0.0.1:$port/realtime/v1/websocket?vsn=1.0.0" | jq -e 'select(.event=="phx_reply" and .ref=="1" and .payload.status=="ok")' >/dev/null

realtime_output=$work_dir/realtime.jsonl
realtime_input=$work_dir/realtime.input
mkfifo "$realtime_input"
SSL_CERT_FILE="$HAKOPOD_ACCEPTANCE_GATEWAY_CA" timeout 30 websocat -t --tls-domain="$gateway_host" "wss://127.0.0.1:$port/realtime/v1/websocket?vsn=1.0.0" <"$realtime_input" >"$realtime_output" &
realtime_pid=$!
exec 9>"$realtime_input"
printf '{"topic":"realtime:public:hakopod_acceptance","event":"phx_join","payload":{"config":{"broadcast":{"ack":false,"self":false},"presence":{"enabled":false},"postgres_changes":[{"event":"UPDATE","schema":"public","table":"hakopod_acceptance"}]},"access_token":"%s"},"ref":"2"}\n' "$access_token" >&9
tries=0
until jq -e 'select(.event=="phx_reply" and .ref=="2" and .payload.status=="ok")' "$realtime_output" >/dev/null 2>&1; do
  kill -0 "$realtime_pid" 2>/dev/null || { echo "Realtime exited before subscription acknowledgement" >&2; exit 1; }
  tries=$((tries+1)); [ "$tries" -lt 30 ] || { echo "Realtime subscription acknowledgement timed out" >&2; exit 1; }; sleep 1
done
curl $curl_tls --fail --silent --show-error --max-time 10 -X PATCH "$base/rest/v1/hakopod_acceptance?id=eq.$row_id" --header @"$owner_headers" -H 'content-type: application/json' -H 'prefer: return=minimal' --data '{"value":"realtime-observed"}'
tries=0
until jq -e --arg id "$row_id" 'select(.event=="postgres_changes" and .payload.data.record.id==$id and .payload.data.record.value=="realtime-observed")' "$realtime_output" >/dev/null 2>&1; do
  kill -0 "$realtime_pid" 2>/dev/null || { echo "Realtime exited before committed change acknowledgement" >&2; exit 1; }
  tries=$((tries+1)); [ "$tries" -lt 30 ] || { echo "Realtime committed change acknowledgement timed out" >&2; exit 1; }; sleep 1
done
exec 9>&-
kill "$realtime_pid" 2>/dev/null || true
wait "$realtime_pid" 2>/dev/null || true
realtime_pid=
rm -f "$realtime_input" "$realtime_output"
realtime_observation=$evidence_dir/realtime.json
jq -n --arg id "$row_id" '{row_id:$id,authenticated_join:true,postgres_update_observed:true}' >"$realtime_observation"
record_case realtime-authorization "$realtime_observation"

bucket=hakopod-test-native
object=roundtrip.txt
control_object=control.txt
curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/storage/v1/bucket" --header @"$service_headers" -H 'content-type: application/json' --data "{\"id\":\"$bucket\",\"name\":\"$bucket\",\"public\":false}" >/dev/null
printf 'hakopod-native-storage' | curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/storage/v1/object/$bucket/$object" --header @"$service_headers" -H 'content-type: text/plain' --data-binary @- >/dev/null
printf 'hakopod-native-control' | curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/storage/v1/object/$bucket/$control_object" --header @"$service_headers" -H 'content-type: text/plain' --data-binary @- >/dev/null
[ "$(curl $curl_tls --fail --silent --show-error --max-time 10 "$base/storage/v1/object/authenticated/$bucket/$object" --header @"$service_headers")" = hakopod-native-storage ]
storage_observation=$evidence_dir/storage.json
jq -n --arg bucket "$bucket" --arg object "$object" '{bucket:$bucket,object:$object,private_bucket:true,authenticated_roundtrip:true}' >"$storage_observation"
record_case storage-roundtrip "$storage_observation"
HAKOPOD_BEHAVIOR_BUCKET="$bucket" "$script_dir/behavior-checks.sh" image
record_case image-transformation "$evidence_dir/image-transformation.json"
edge_fixture_before=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec deployment/supabase-edge-runtime -- sha256sum /home/deno/functions/hello/index.ts | awk '{print $1}')
[ -n "$edge_fixture_before" ]
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/functions/v1/hello" --header @"$owner_headers" | jq -e '. == {message:"Hello from Edge Functions!"}' >/dev/null
"$script_dir/behavior-checks.sh" edge
record_case edge-runtime-isolation "$evidence_dir/edge-runtime-isolation.json"
"$script_dir/behavior-checks.sh" studio
record_case studio-admin-isolation "$evidence_dir/studio-admin-isolation.json"
HAKOPOD_ACCEPTANCE_NAMESPACE_UID="$namespace_uid" HAKOPOD_ACCEPTANCE_OWNER_OPERATION_ID="$namespace_owner_operation_id" HAKOPOD_ACCEPTANCE_RESOURCE_INTENT_ID="$namespace_resource_intent_id" "$script_dir/network-isolation.sh" >"$evidence_dir/network-policy-isolation.json"
record_case network-policy-isolation "$evidence_dir/network-policy-isolation.json"
meta_tables=$work_dir/postgres-meta-tables.json
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec -i deployment/supabase-studio -- node - >"$meta_tables" <<'JS'
const endpoint = 'http://meta:8080/tables'
const connection = new URL('postgresql://db:5432/' + encodeURIComponent(process.env.POSTGRES_DB))
connection.username = process.env.POSTGRES_USER_READ_WRITE
connection.password = process.env.POSTGRES_PASSWORD
connection.searchParams.set('sslmode', 'verify-full')
const request = async (path = '', options = {}) => {
  const response = await fetch(endpoint + path, {
    ...options,
    headers: { pg: connection.toString(), 'content-type': 'application/json', ...(options.headers || {}) },
  })
  const text = await response.text()
  let body
  try { body = JSON.parse(text) } catch { body = { raw: text } }
  return { response, body }
}
const fail = message => { throw new Error(message) }
;(async () => {
  const initial = await request('?included_schemas=public')
  if (!initial.response.ok || !Array.isArray(initial.body)) fail('public table listing failed')
  const acceptance = initial.body.find(table => table?.schema === 'public' && table?.name === 'hakopod_acceptance')
  if (!acceptance || !Number.isInteger(acceptance.id)) fail('acceptance table is absent from public catalog')

  const created = await request('', {
    method: 'POST',
    body: JSON.stringify({ schema: 'public', name: 'hakopod_meta_managed', comment: 'created by native acceptance' }),
  })
  if (!created.response.ok || created.body?.schema !== 'public' || created.body?.name !== 'hakopod_meta_managed' || !Number.isInteger(created.body?.id)) fail('public table creation failed')
  const managedId = created.body.id

  const retrieved = await request(`/${managedId}`)
  if (!retrieved.response.ok || retrieved.body?.id !== managedId || retrieved.body?.name !== 'hakopod_meta_managed') fail('created table retrieval failed')
  const updated = await request(`/${managedId}`, {
    method: 'PATCH',
    body: JSON.stringify({ comment: 'updated by native acceptance', rls_enabled: true }),
  })
  if (!updated.response.ok || updated.body?.id !== managedId || updated.body?.comment !== 'updated by native acceptance' || updated.body?.rls_enabled !== true) fail('owned public table update failed')

  const protectedCreate = await request('', {
    method: 'POST',
    body: JSON.stringify({ schema: 'auth', name: 'hakopod_meta_forbidden' }),
  })
  if (protectedCreate.response.ok) fail('protected schema creation unexpectedly succeeded')
  const unownedUpdate = await request(`/${acceptance.id}`, {
    method: 'PATCH',
    body: JSON.stringify({ comment: 'forbidden unowned update' }),
  })
  if (unownedUpdate.response.ok) fail('unowned public table update unexpectedly succeeded')

  const removed = await request(`/${managedId}`, { method: 'DELETE' })
  if (!removed.response.ok || removed.body?.id !== managedId) fail('owned public table deletion failed')
  const final = await request('?included_schemas=public')
  if (!final.response.ok || !Array.isArray(final.body) || final.body.some(table => table?.name === 'hakopod_meta_managed')) fail('deleted table remains in public catalog')

  process.stdout.write(JSON.stringify({
    requested_schema: 'public',
    acceptance_table: 'hakopod_acceptance',
    acceptance_table_visible: true,
    managed_table_create_read_update_drop: true,
    protected_schema_create_denied: true,
    unowned_public_table_update_denied: true,
  }))
})().catch(error => { console.error(error.message); process.exit(1) })
JS
meta_observation=$evidence_dir/postgres-meta.json
jq -e '.requested_schema == "public" and .acceptance_table == "hakopod_acceptance" and .acceptance_table_visible == true and .managed_table_create_read_update_drop == true and .protected_schema_create_denied == true and .unowned_public_table_update_denied == true' "$meta_tables" >/dev/null
jq '.' "$meta_tables" >"$meta_observation"
record_case postgres-meta-public-management "$meta_observation"

# Tie each direct database client to a live TLS session by its pod IP. The
# database rejects plaintext independently, but this proves every expected
# client actually reached the native PostgreSQL TLS listener in this run.
database_sessions=/tmp/hakopod-supabase-database-sessions-$HAKOPOD_ACCEPTANCE_PLATFORM_ID.txt
for component in auth pooler postgres-meta realtime rest storage; do
  client_ip=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get pod -l "app.kubernetes.io/component=$component" -o jsonpath='{.items[0].status.podIP}')
  [ -n "$client_ip" ]
  tries=0
  while :; do
    kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec statefulset/supabase-database -- \
      psql -U postgres -v ON_ERROR_STOP=1 -Atc "select coalesce(a.client_addr::text,''),s.ssl,coalesce(s.version,''),coalesce(s.cipher,'') from pg_stat_activity a join pg_stat_ssl s using(pid) where a.client_addr is not null" >"$database_sessions"
    awk -F '|' -v ip="$client_ip" '$1==ip && $2=="t" && ($3=="TLSv1.2" || $3=="TLSv1.3") && $4!="" {found=1} END{exit found?0:1}' "$database_sessions" && break
    tries=$((tries+1))
    [ "$tries" -lt 15 ] || { echo "$component has no attributable verified database TLS session" >&2; exit 1; }
    sleep 2
  done
done
rm -f "$database_sessions"
mounted_database_ca_sha=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get secret "$database_tls_ref" -o jsonpath='{.data.ca\.crt}' | base64 -d | sha256sum | awk '{print $1}')
fixture_database_ca_sha=$(sha256sum "$HAKOPOD_ACCEPTANCE_ADVERSARIAL_CERT_DIR/trusted-ca.crt" | awk '{print $1}')
mounted_database_cert_sha=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get secret "$database_tls_ref" -o jsonpath='{.data.tls\.crt}' | base64 -d | sha256sum | awk '{print $1}')
fixture_database_cert_sha=$(sha256sum "$HAKOPOD_ACCEPTANCE_ADVERSARIAL_CERT_DIR/good-db.crt" | awk '{print $1}')
[ "$mounted_database_ca_sha" = "$fixture_database_ca_sha" ] && [ "$mounted_database_cert_sha" = "$fixture_database_cert_sha" ] || { echo "running database certificate does not match the reviewed adversarial matrix reference" >&2; exit 1; }
HAKOPOD_SUPABASE_NAMESPACE="$namespace" \
  HAKOPOD_ACCEPTANCE_PLATFORM_ID="$HAKOPOD_ACCEPTANCE_PLATFORM_ID" \
  HAKOPOD_ACCEPTANCE_NAMESPACE_UID="$namespace_uid" \
  HAKOPOD_ACCEPTANCE_DISPOSABLE=1 \
  "$script_dir/adversarial-database-tls.sh"
database_tls_observation=$evidence_dir/database-tls.json
jq -n --arg ca_sha256 "$mounted_database_ca_sha" --arg cert_sha256 "$mounted_database_cert_sha" '{ca_sha256:$ca_sha256,certificate_sha256:$cert_sha256,attributed_clients:["auth","pooler","postgres-meta","realtime","rest","storage"],hostname_verified:true,wrong_ca_refused:true,wrong_hostname_refused:true,plaintext_refused:true}' >"$database_tls_observation"
record_case database-tls-hostname "$database_tls_observation"
record_case database-plaintext-refusal "$database_tls_observation"

HAKOPOD_SUPABASE_NAMESPACE="$namespace" HAKOPOD_ACCEPTANCE_NAMESPACE_UID="$namespace_uid" "$script_dir/restart-owned-pods.sh"
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" rollout status statefulset/supabase-database --timeout=5m >/dev/null
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" rollout status deployment --timeout=5m >/dev/null
kill "$forward_pid" 2>/dev/null || true
wait "$forward_pid" 2>/dev/null || true
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" port-forward service/api-gw "$port:8443" >"$port_forward_log" 2>&1 &
forward_pid=$!
tries=0
until curl $curl_tls --fail --silent --max-time 2 "$base/auth/v1/health" >/dev/null; do tries=$((tries+1)); [ "$tries" -lt 30 ] || { echo "Supabase gateway did not return after restart" >&2; exit 1; }; sleep 1; done
edge_fixture_after=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec deployment/supabase-edge-runtime -- sha256sum /home/deno/functions/hello/index.ts | awk '{print $1}')
[ "$edge_fixture_after" = "$edge_fixture_before" ] || { echo "Edge hello fixture changed across PVC-backed restart" >&2; exit 1; }
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/functions/v1/hello" --header @"$owner_headers" | jq -e '. == {message:"Hello from Edge Functions!"}' >/dev/null
pvc_after=$work_dir/pvc-after.json
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get pvc -o json | jq -S '[.items[] | {name:.metadata.name,uid:.metadata.uid,volume:.spec.volumeName,storage_class:.spec.storageClassName,access_modes:.spec.accessModes,request:.spec.resources.requests.storage}] | sort_by(.name)' >"$pvc_after"
cmp -s "$pvc_before" "$pvc_after" || { echo "Supabase PVC or PV identity changed across pod replacement" >&2; exit 1; }
pv_after=$work_dir/pv-after.json
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get pv $(jq -r '.[].volume' "$pvc_after") -o json | jq -S '[.items[] | {name:.metadata.name,uid:.metadata.uid,claim_namespace:.spec.claimRef.namespace,claim_name:.spec.claimRef.name,claim_uid:.spec.claimRef.uid}] | sort_by(.name)' >"$pv_after"
cmp -s "$pv_before" "$pv_after" || { echo "Supabase PV resource identity changed across pod replacement" >&2; exit 1; }
[ "$(curl $curl_tls --fail --silent --show-error --max-time 10 "$base/storage/v1/object/authenticated/$bucket/$object" --header @"$service_headers")" = hakopod-native-storage ]
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/rest/v1/hakopod_acceptance?id=eq.$row_id&select=id,value" --header @"$service_headers" | jq -e --arg id "$row_id" 'length==1 and .[0].id==$id' >/dev/null
record_case restart-persistence "$pvc_after"

platform=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --header @"$api_headers")
revision=$(printf '%s' "$platform" | jq -er '.revision')
rotation_request=$work_dir/rotation-request.json
rotation_response=$work_dir/rotation-response.json
printf '%s' "$platform" | jq --argjson revision "$revision" '{id:.id,project:.project,environment:.environment,expected_revision:$revision,kind:"update",confirm_name:.spec.name,spec:(.spec | .secrets["database-owner-password"].revision += 1)}' >"$rotation_request"
rotation_status=$(curl --max-filesize 1048576 --silent --show-error --output "$rotation_response" --write-out '%{http_code}' --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/reviews" --header @"$api_headers" -H 'content-type: application/json' --data-binary @"$rotation_request")
[ "$rotation_status" = 400 ]
jq -e '.. | strings | select(contains("secret rotation for database-owner-password is unavailable"))' "$rotation_response" >/dev/null || { echo "uncoordinated database credential rotation was not refused explicitly" >&2; exit 1; }
record_case unsafe-rotation-refusal "$rotation_response"
database_keys='["auth-database-url","database-owner-password","database-role-bootstrap","postgres-meta-database-password","realtime-database-password","rest-database-url","storage-database-url","supavisor-database-url"]'
jq -e --slurpfile before "$HAKOPOD_ACCEPTANCE_SPEC" --argjson keys "$database_keys" '
  .schema_version==1 and .kind=="supabase" and .supabase.jwt_expiry_seconds==7200 and
  (. as $new | $before[0] as $old | ($new|.secrets={}|.supabase.jwt_expiry_seconds=0)==($old|.secrets={}|.supabase.jwt_expiry_seconds=0)) and
  (.secrets as $new | $before[0].secrets as $old | all($new|keys[] as $key; (($keys|index($key))!=null) or $new[$key]==$old[$key])) and
  (.secrets as $new | $before[0].secrets as $old | all($keys[] as $key; $new[$key] != $old[$key]))
' "$HAKOPOD_ACCEPTANCE_ROTATION_SPEC" >/dev/null || { echo "full rotation specification changed unsupported identities or omitted a database credential" >&2; exit 1; }
database_name=$(jq -er '.supabase.database_name' "$HAKOPOD_ACCEPTANCE_SPEC")
check_database_password() {
  password_file=$1
  { cat "$password_file"; printf '\n'; } | kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec -i statefulset/supabase-database -- sh -c 'IFS= read -r PGPASSWORD; export PGPASSWORD; exec psql -X -h 127.0.0.1 -U postgres -d "$1" -v ON_ERROR_STOP=1 -Atc "select 1"' sh "$database_name" >/dev/null 2>&1
}
check_database_password "$HAKOPOD_ACCEPTANCE_ROTATION_OLD_PASSWORD_FILE" || { echo "old database credential was not valid before rotation" >&2; exit 1; }
old_secret_refs=$work_dir/pre-rotation-secret-refs.json
printf '%s' "$platform" | jq -S '.spec.secrets' >"$old_secret_refs"
update_review=$(printf '%s' "$platform" | jq --slurpfile spec "$HAKOPOD_ACCEPTANCE_ROTATION_SPEC" --argjson revision "$revision" '{id:.id,project:.project,environment:.environment,expected_revision:$revision,kind:"update",confirm_name:.spec.name,spec:$spec[0]}' | curl --max-filesize 1048576 --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/reviews" --header @"$api_headers" -H 'content-type: application/json' --data-binary @-)
printf '%s' "$update_review" | jq -e '.blocked==false and .review.id!=null' >/dev/null
update_operation=$(printf '%s' "$update_review" | jq '{id:.platform.id,project:.platform.project,environment:.platform.environment,expected_revision:.review.expected_revision,kind:"update",confirm_name:.platform.spec.name,spec:.platform.spec,review:.review}' | curl --max-filesize 1048576 --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/operations" --header @"$api_headers" -H "Idempotency-Key: hakopod-test-update-$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -H 'content-type: application/json' --data-binary @-)
update_operation_id=$(printf '%s' "$update_operation" | jq -er '.id')
tries=0
while [ ! -f "$HAKOPOD_ACCEPTANCE_ROTATION_FAIL_ONCE_FILE" ]; do tries=$((tries+1)); [ "$tries" -lt 60 ] || { echo "database rotation interruption boundary was not reached" >&2; exit 1; }; sleep 1; done
kill "$HAKOPOD_ACCEPTANCE_SERVER_PID"
tries=0
while curl --silent --max-time 1 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/me" >/dev/null 2>&1; do tries=$((tries+1)); [ "$tries" -lt 30 ] || { echo "management server did not stop at the rotation boundary" >&2; exit 1; }; sleep 1; done
! kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get configmap "supabase-database-credentials-r$((revision+1))" >/dev/null 2>&1 || { echo "rotation completion marker existed before management restart" >&2; exit 1; }
"$HAKOPOD_ACCEPTANCE_SERVER_BINARY" >>"$work_dir/server-rotation-restart.log" 2>&1 &
restarted_server_pid=$!
tries=0
until curl --silent --fail --max-time 1 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/me" --header @"$api_headers" >/dev/null 2>&1; do tries=$((tries+1)); [ "$tries" -lt 60 ] || { echo "management server did not restart for rotation replay" >&2; exit 1; }; sleep 1; done
: >"$HAKOPOD_ACCEPTANCE_ROTATION_RELEASE_FILE"; chmod 600 "$HAKOPOD_ACCEPTANCE_ROTATION_RELEASE_FILE"
tries=0
while :; do update_status=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-operations/$update_operation_id" --header @"$api_headers" | jq -er '.status'); [ "$update_status" = succeeded ] && break; [ "$update_status" != failed ] && [ "$update_status" != cancelled ] || { echo "Supabase update failed" >&2; exit 1; }; tries=$((tries+1)); [ "$tries" -lt 180 ] || { echo "Supabase update timed out" >&2; exit 1; }; sleep 2; done
updated_platform=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --header @"$api_headers")
[ -f "$HAKOPOD_ACCEPTANCE_ROTATION_FAIL_ONCE_FILE" ] && [ ! -L "$HAKOPOD_ACCEPTANCE_ROTATION_FAIL_ONCE_FILE" ] || { echo "database rotation retry boundary did not run" >&2; exit 1; }
rotation_retry_evidence=$evidence_dir/database-credential-rotation-retry.json
jq -n '{status:"passed",sql_committed_before_process_restart:true,completion_marker_absent_before_process_restart:true,management_process_restarted:true,durable_operation_resumed:true,retry_replayed_transaction:true}' >"$rotation_retry_evidence"
record_case database-credential-rotation-retry "$rotation_retry_evidence"
printf '%s' "$updated_platform" | jq -e --argjson revision "$((revision+1))" --slurpfile expected "$HAKOPOD_ACCEPTANCE_ROTATION_SPEC" '.revision==$revision and .spec==$expected[0]' >/dev/null
check_database_password "$HAKOPOD_ACCEPTANCE_ROTATION_NEW_PASSWORD_FILE" || { echo "new database credential was rejected after rotation" >&2; exit 1; }
if check_database_password "$HAKOPOD_ACCEPTANCE_ROTATION_OLD_PASSWORD_FILE"; then echo "old database credential remained valid after rotation" >&2; exit 1; fi
jwt_setting=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec statefulset/supabase-database -- psql -X -U postgres -d "$database_name" -Atc 'show app.settings.jwt_exp')
[ "$jwt_setting" = 7200 ] || { echo "database JWT expiry setting was not rotated" >&2; exit 1; }
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec -i statefulset/supabase-database -- psql -X -U postgres -d "$database_name" -v ON_ERROR_STOP=1 >/dev/null <<'SQL'
create or replace function public.hakopod_jwt_expiry() returns text language sql stable security definer set search_path = '' as $$ select current_setting('app.settings.jwt_exp', true) $$;
revoke all on function public.hakopod_jwt_expiry() from public;
grant execute on function public.hakopod_jwt_expiry() to service_role;
SQL
[ "$(curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/rest/v1/rpc/hakopod_jwt_expiry" --header @"$service_headers" -H 'content-type: application/json' --data '{}')" = '"7200"' ] || { echo "PostgREST did not observe the rotated JWT setting" >&2; exit 1; }
rotation_marker=supabase-database-credentials-r$((revision+1))
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get configmap "$rotation_marker" -o json | jq -e '.immutable==true and (.data.reference_fingerprint|test("^[a-f0-9]{64}$"))' >/dev/null
new_signup=$(curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/auth/v1/signup" --header @"$anon_headers" -H 'content-type: application/json' --data '{"data":{"acceptance":"hakopod-test-native-rotated"}}')
new_access_token=$(printf '%s' "$new_signup" | jq -er '.access_token')
token_lifetime=$(TOKEN="$new_access_token" python3 - <<'PY'
import base64,json,os
part=os.environ['TOKEN'].split('.')[1]; part += '='*((4-len(part)%4)%4)
payload=json.loads(base64.urlsafe_b64decode(part)); print(payload['exp']-payload['iat'])
PY
)
[ "$token_lifetime" = 7200 ] || { echo "GoTrue did not issue the reviewed JWT lifetime" >&2; exit 1; }
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" delete pod -l 'app.kubernetes.io/component in (database,auth,rest)' --wait=false >/dev/null
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" rollout status statefulset/supabase-database --timeout=180s >/dev/null
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" rollout status deployment/supabase-auth --timeout=180s >/dev/null
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" rollout status deployment/supabase-rest --timeout=180s >/dev/null
check_database_password "$HAKOPOD_ACCEPTANCE_ROTATION_NEW_PASSWORD_FILE" || { echo "rotated credential did not survive restart" >&2; exit 1; }
[ "$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec statefulset/supabase-database -- psql -X -U postgres -d "$database_name" -Atc 'show app.settings.jwt_exp')" = 7200 ] || { echo "JWT expiry did not survive restart" >&2; exit 1; }
[ "$(curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/rest/v1/rpc/hakopod_jwt_expiry" --header @"$service_headers" -H 'content-type: application/json' --data '{}')" = '"7200"' ] || { echo "PostgREST JWT setting did not survive restart" >&2; exit 1; }
printf '%s' "$updated_platform" | jq -r --argjson keys "$database_keys" '.spec.secrets|to_entries[]|.key as $key|select($keys|index($key))|.value.name+"-r"+(.value.revision|tostring)' | while read -r secret_name; do kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get secret "$secret_name" >/dev/null; done
jq -r --argjson keys "$database_keys" 'to_entries[]|.key as $key|select($keys|index($key))|.value.name+"-r"+(.value.revision|tostring)' "$old_secret_refs" | while read -r secret_name; do ! kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get secret "$secret_name" >/dev/null 2>&1 || { echo "old database credential snapshot was not pruned after readiness" >&2; exit 1; }; done
rotation_evidence=$evidence_dir/database-credential-and-jwt-rotation.json
jq -n --arg marker "$rotation_marker" --argjson revision "$((revision+1))" '{status:"passed",revision:$revision,all_database_credentials_rotated:true,old_database_credential_rejected:true,new_database_credential_accepted:true,jwt_expiry_seconds:7200,gotrue_token_lifetime_seconds:7200,postgrest_setting_observed:true,restart_persistence:true,old_snapshots_pruned_after_ready:true,completion_marker:$marker}' >"$rotation_evidence"
record_case database-credential-and-jwt-rotation "$rotation_evidence"
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get pvc -o json | jq -S '[.items[] | {name:.metadata.name,uid:.metadata.uid,volume:.spec.volumeName,storage_class:.spec.storageClassName,access_modes:.spec.accessModes,request:.spec.resources.requests.storage}] | sort_by(.name)' >"$pvc_after"
cmp -s "$pvc_before" "$pvc_after" || { echo "Supabase PVC or PV identity changed across credential rotation" >&2; exit 1; }
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get pv $(jq -r '.[].volume' "$pvc_after") -o json | jq -S '[.items[] | {name:.metadata.name,uid:.metadata.uid,claim_namespace:.spec.claimRef.namespace,claim_name:.spec.claimRef.name,claim_uid:.spec.claimRef.uid}] | sort_by(.name)' >"$pv_after"
cmp -s "$pv_before" "$pv_after" || { echo "Supabase PV resource identity changed across no-change update" >&2; exit 1; }
[ "$(curl $curl_tls --fail --silent --show-error --max-time 10 "$base/storage/v1/object/authenticated/$bucket/$object" --header @"$service_headers")" = hakopod-native-storage ]

HAKOPOD_ACCEPTANCE_API_HEADERS="$api_headers" HAKOPOD_ACCEPTANCE_NAMESPACE_UID="$namespace_uid" HAKOPOD_ACCEPTANCE_OWNER_OPERATION_ID="$namespace_owner_operation_id" HAKOPOD_ACCEPTANCE_RESOURCE_INTENT_ID="$namespace_resource_intent_id" "$script_dir/cancel-recovery.sh" >"$evidence_dir/cancellation.json"
jq -e '.status=="passed" and .artifact_absent and .source_ready_unchanged and .ownership_unchanged' "$evidence_dir/cancellation.json" >/dev/null
[ "$(curl $curl_tls --fail --silent --show-error --max-time 10 "$base/storage/v1/object/authenticated/$bucket/$object" --header @"$service_headers")" = hakopod-native-storage ]
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/rest/v1/hakopod_acceptance?id=eq.$row_id&select=id,value" --header @"$service_headers" | jq -e --arg id "$row_id" 'length==1 and .[0].id==$id and .[0].value=="realtime-observed"' >/dev/null
record_case cancellation-recovery "$evidence_dir/cancellation.json"

HAKOPOD_ACCEPTANCE_API_HEADERS="$api_headers" "$script_dir/recovery.sh" >"$work_dir/recovery-result.json"
jq -e '.status=="passed" and (.artifact_id|test("^[0-9a-f]{32}$"))' "$work_dir/recovery-result.json" >/dev/null
target_namespace_json=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get namespace "$target_namespace" -o json)
printf '%s' "$target_namespace_json" | jq -e --arg id "$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_ID" --arg uid "$target_namespace_uid" '.metadata.labels["hakopod.io/managed-platform-id"]==$id and .metadata.uid==$uid' >/dev/null
target_platform=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_ID" --header @"$api_headers")
target_host=$(printf '%s' "$target_platform" | jq -er '.spec.supabase.public_url | sub("^https://";"") | sub("/$";"")')
target_port=$((port+1))
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$target_namespace" port-forward service/api-gw "$target_port:8443" >"$work_dir/recovery-port-forward.log" 2>&1 &
recovery_forward_pid=$!
target_base=https://$target_host:$target_port
target_curl_tls="--noproxy $target_host --resolve $target_host:$target_port:127.0.0.1 --cacert $HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_GATEWAY_CA"
tries=0
until curl $target_curl_tls --fail --silent --max-time 2 "$target_base/auth/v1/health" >/dev/null; do tries=$((tries+1)); [ "$tries" -lt 30 ] || { echo "restored Supabase target did not become reachable" >&2; exit 1; }; sleep 1; done
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$target_namespace" exec statefulset/supabase-database -- \
  psql -U postgres -v ON_ERROR_STOP=1 -Atc "select exists(select 1 from _realtime.tenants where external_id='realtime-dev' and jwt_secret like 'g1:%') and exists(select 1 from _realtime.extensions where tenant_external_id='realtime-dev' and type='postgres_cdc_rls' and settings->>'ssl_enforced'='true' and settings->>'db_host' like 'g1:%' and settings->>'db_password' like 'g1:%')" | grep -qx t || { echo "restored Realtime encryption or tenant TLS policy differs" >&2; exit 1; }
curl $target_curl_tls --fail --silent --show-error --max-time 10 "$target_base/rest/v1/hakopod_acceptance?id=eq.$row_id&select=id,value" --header @"$target_service_headers" | jq -e --arg id "$row_id" 'length==1 and .[0].id==$id and .[0].value=="realtime-observed"' >/dev/null
[ "$(curl $target_curl_tls --fail --silent --show-error --max-time 10 "$target_base/storage/v1/object/authenticated/$bucket/$object" --header @"$target_service_headers")" = hakopod-native-storage ]
curl $target_curl_tls --fail --silent --show-error --max-time 10 "$target_base/functions/v1/hello" --header @"$target_service_headers" | jq -e '. == {message:"Hello from Edge Functions!"}' >/dev/null
[ "$(curl $curl_tls --fail --silent --show-error --max-time 10 "$base/storage/v1/object/authenticated/$bucket/$object" --header @"$service_headers")" = hakopod-native-storage ]
record_case backup-separate-resource-restore "$work_dir/recovery-result.json"
target_log_dir=$work_dir/logs-recovery-target
mkdir -m 700 "$target_log_dir"
for target_pod in $(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$target_namespace" get pods --chunk-size=32 -l "hakopod.io/managed-platform-id=$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_ID" -o name); do
  target_log_name=${target_pod#pod/}
  kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$target_namespace" logs "$target_pod" --all-containers --tail=2000 --limit-bytes=2097152 >"$target_log_dir/$target_log_name.current.log"
done
printf '%s' "$target_platform" | jq -r '.spec.secrets|to_entries[]|[.key,(.value.name+"-r"+(.value.revision|tostring))]|@tsv' | while IFS="$(printf '\t')" read -r logical_name secret_name; do
  target_secret=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$target_namespace" get secret "$secret_name" -o json)
  printf '%s' "$target_secret" | jq -r '.data|to_entries[]|[.key,.value]|@tsv' | while IFS="$(printf '\t')" read -r data_key encoded; do
    printf '%s' "$encoded" | base64 -d >"$secret_dir/target-value"
    [ -s "$secret_dir/target-value" ] || continue
    ! secret_in_tree "$secret_dir/target-value" "$work_dir" || { echo "target secret material appeared in acceptance artifacts: $logical_name/$data_key" >&2; exit 1; }
  done
done
kill "$recovery_forward_pid" 2>/dev/null || true
wait "$recovery_forward_pid" 2>/dev/null || true
recovery_forward_pid=
cleanup_target
target_cleanup_armed=0
target_delete_file=$evidence_dir/target-delete.json
curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-operations/$(printf '%s' "$target_operation" | jq -er '.id')" --header @"$api_headers" | jq -e '{id,platform_id,kind,status}' >"$target_delete_file"

curl $curl_tls --fail --silent --show-error --max-time 10 -X DELETE "$base/storage/v1/object/$bucket" --header @"$service_headers" -H 'content-type: application/json' --data "{\"prefixes\":[\"$object\"]}" | jq -e 'type=="array" and length==1' >/dev/null
removed_body=$work_dir/storage-removed.json
removed_status=$(curl $curl_tls --silent --show-error --output "$removed_body" --write-out '%{http_code}' --max-time 10 "$base/storage/v1/object/info/authenticated/$bucket/$object" --header @"$service_headers")
[ "$removed_status" = 400 ] && jq -e '.statusCode=="404" and .error=="not_found" and .message=="Object not found" and .code=="NoSuchKey"' "$removed_body" >/dev/null
[ "$(curl $curl_tls --fail --silent --show-error --max-time 10 "$base/storage/v1/object/authenticated/$bucket/$control_object" --header @"$service_headers")" = hakopod-native-control ]
curl $curl_tls --fail --silent --show-error --max-time 10 -X DELETE "$base/storage/v1/object/$bucket" --header @"$service_headers" -H 'content-type: application/json' --data "{\"prefixes\":[\"$control_object\"]}" | jq -e 'type=="array" and length==1' >/dev/null
curl $curl_tls --fail --silent --show-error --max-time 10 -X DELETE "$base/storage/v1/bucket/$bucket" --header @"$service_headers" >/dev/null
curl $curl_tls --fail --silent --show-error --max-time 10 -X DELETE "$base/rest/v1/hakopod_acceptance?id=eq.$row_id" --header @"$service_headers" >/dev/null

capture_logs after-recovery
for header_file in "$api_headers" "$anon_headers" "$owner_headers" "$second_headers" "$service_headers" "$target_service_headers" "$studio_headers"; do
  while IFS= read -r header; do
    credential=${header#*: }
    credential=${credential#Bearer }
    [ -n "$credential" ] || continue
    printf '%s' "$credential" >"$secret_dir/header-value"
    ! secret_in_tree "$secret_dir/header-value" "$work_dir" || { echo "credential appeared in acceptance artifacts or captured workload logs" >&2; exit 1; }
    ! ps -eo args= | CREDENTIAL="$credential" awk 'index($0,ENVIRON["CREDENTIAL"]){found=1} END{exit found?0:1}' || { echo "credential appeared in a process argument" >&2; exit 1; }
  done <"$header_file"
done
secret_inventory=$secret_dir/secret-inventory.tsv
jq -r '.secrets|to_entries[]|[.key,(.value.name+"-r"+(.value.revision|tostring))]|@tsv' "$HAKOPOD_ACCEPTANCE_SPEC" >"$secret_inventory"
while IFS="$(printf '\t')" read -r logical_name secret_name; do
  secret_json=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get secret "$secret_name" -o json)
  printf '%s' "$secret_json" | jq -r '.data|to_entries[]|[.key,.value]|@tsv' | while IFS="$(printf '\t')" read -r data_key encoded; do
    value_file=$secret_dir/secret-value
    printf '%s' "$encoded" | base64 -d >"$value_file"
    [ -s "$value_file" ] || continue
    ! secret_in_tree "$value_file" "$work_dir" || { echo "fixture secret material appeared in acceptance artifacts or captured workload logs: $logical_name/$data_key" >&2; exit 1; }
  done
done <"$secret_inventory"

kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec statefulset/supabase-database -- psql -U postgres -v ON_ERROR_STOP=1 -c 'drop table public.hakopod_acceptance;' >/dev/null

platform=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --header @"$api_headers")
revision=$(printf '%s' "$platform" | jq -er '.revision')
name=$(printf '%s' "$platform" | jq -er '.spec.name')
review=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/reviews" --header @"$api_headers" -H 'content-type: application/json' --data "$(printf '%s' "$platform" | jq --argjson revision "$revision" '{id:.id,project:.project,environment:.environment,expected_revision:$revision,kind:"delete",confirm_name:.spec.name,spec:.spec}')")
printf '%s' "$review" | jq -e '.blocked==false and .review.id!=null' >/dev/null
operation=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/operations" --header @"$api_headers" -H "Idempotency-Key: hakopod-test-delete-$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -H 'content-type: application/json' --data "$(printf '%s' "$review" | jq '{id:.platform.id,project:.platform.project,environment:.platform.environment,expected_revision:(.review.expected_revision),kind:"delete",confirm_name:.platform.spec.name,spec:.platform.spec,review:.review}')")
operation_id=$(printf '%s' "$operation" | jq -er '.id')
tries=0
while kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get namespace "$namespace" >/dev/null 2>&1; do tries=$((tries+1)); [ "$tries" -lt 180 ] || { echo "namespace deletion timed out" >&2; exit 1; }; sleep 2; done
status=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-operations/$operation_id" --header @"$api_headers" | jq -er '.status')
[ "$status" = succeeded ]
[ -z "$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get namespace "$namespace" --ignore-not-found -o name)" ] || { echo "Supabase namespace remains after deletion" >&2; exit 1; }
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get pv --chunk-size=128 -o json | jq -e --arg ns "$namespace" 'all(.items[]; .spec.claimRef.namespace!=$ns)' >/dev/null || { echo "Supabase persistent volumes remain after deletion" >&2; exit 1; }
cleanup_armed=0
source_delete_file=$evidence_dir/source-delete.json
curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-operations/$operation_id" --header @"$api_headers" | jq -e '{id,platform_id,kind,status}' >"$source_delete_file"
record_case owned-cleanup "$source_delete_file"
# Structural events are the safe log. Raw component logs stay protected and are
# never copied into a qualification record.
cp "$evidence_events" "$evidence_dir/sanitized-log.json"
evidence finalize --source "$repo_root" --state "$evidence_state" --events "$evidence_events" --sanitized-log "$evidence_dir/sanitized-log.json" --source-delete-operation "$source_delete_file" --recovery-target-delete-operation "$target_delete_file" --report "$evidence_dir/native-result.json" --cleanup "$evidence_dir/cleanup-receipt.json"
printf '{"context":"%s","platform_id":"%s","namespace_uid":"%s","operation_id":"%s","status":"passed","evidence_directory":"%s"}\n' "$expected_context" "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" "$namespace_uid" "$operation_id" "$evidence_dir"
