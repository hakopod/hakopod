#!/bin/sh
set -eu
umask 077
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

expected_context=k3d-hakopod-dev
: "${KUBECONFIG:?set KUBECONFIG to the dedicated Hakopod development kubeconfig}"
: "${HAKOPOD_ACCEPTANCE_SPEC:?set the path to the disposable Supabase specification JSON}"
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
for command in kubectl curl jq websocat sha256sum openssl python3; do command -v "$command" >/dev/null 2>&1 || { echo "$command is required" >&2; exit 2; }; done
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
trap 'rm -rf "$secret_dir"' EXIT HUP INT TERM
create_review=$(jq -n --slurpfile spec "$HAKOPOD_ACCEPTANCE_SPEC" --arg project "$HAKOPOD_ACCEPTANCE_PROJECT" --arg environment "$HAKOPOD_ACCEPTANCE_ENVIRONMENT" '{project:$project,environment:$environment,expected_revision:0,kind:"create",spec:$spec[0]}' | curl --max-filesize 1048576 --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/reviews" --header @"$api_headers" -H 'content-type: application/json' --data-binary @-)
printf '%s' "$create_review" | jq -e '.blocked==false and .review.id!=null' >/dev/null || { echo "Supabase capability gate remains closed; acceptance did not run" >&2; exit 3; }
HAKOPOD_ACCEPTANCE_PLATFORM_ID=$(printf '%s' "$create_review" | jq -er '.platform.id')
export HAKOPOD_ACCEPTANCE_PLATFORM_ID
namespace=managed-platform-$HAKOPOD_ACCEPTANCE_PLATFORM_ID
expected_name=$(printf '%s' "$create_review" | jq -er '.platform.spec.name')
work_dir=/tmp/hakopod-supabase-native-$HAKOPOD_ACCEPTANCE_PLATFORM_ID
[ ! -e "$work_dir" ] && [ ! -L "$work_dir" ] || { echo "acceptance work directory already exists" >&2; exit 2; }
mkdir -m 700 "$work_dir"
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
  rm -rf "$secret_dir"
  [ "$status" -ne 0 ] || rm -rf "$work_dir"
  exit "$status"
}
cleanup_armed=1
trap cleanup EXIT
trap 'exit 130' HUP INT TERM
create_operation=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/operations" --header @"$api_headers" -H "Idempotency-Key: $create_idempotency" -H 'content-type: application/json' --data-binary @"$create_request")
create_operation_id=$(printf '%s' "$create_operation" | jq -er '.id')
tries=0
while :; do create_status=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-operations/$create_operation_id" --header @"$api_headers" | jq -er '.status'); [ "$create_status" = succeeded ] && break; [ "$create_status" != failed ] && [ "$create_status" != cancelled ] || { echo "Supabase creation failed" >&2; exit 1; }; tries=$((tries+1)); [ "$tries" -lt 180 ] || { echo "Supabase creation timed out" >&2; exit 1; }; sleep 2; done
jq -e 'type=="object" and length==11 and all(.[];test("^[^@]+@sha256:[0-9a-f]{64}$"))' "$HAKOPOD_ACCEPTANCE_IMAGES" >/dev/null
platform=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --header @"$api_headers")
namespace_uid=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get namespace "$namespace" -o jsonpath='{.metadata.uid}')
[ -n "$namespace_uid" ]
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
  "$script_dir/pooler-tls.sh"

pvc_before=$work_dir/pvc-before.json
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get pvc -o json | jq -S '[.items[] | {name:.metadata.name,uid:.metadata.uid,volume:.spec.volumeName,storage_class:.spec.storageClassName,access_modes:.spec.accessModes,request:.spec.resources.requests.storage}] | sort_by(.name)' >"$pvc_before"
[ "$(jq length "$pvc_before")" -eq 5 ] && jq -e 'all(.[]; .uid!="" and .volume!="" and .storage_class!="" and (.access_modes|length)>0 and .request!="")' "$pvc_before" >/dev/null
pv_before=$work_dir/pv-before.json
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get pv $(jq -r '.[].volume' "$pvc_before") -o json | jq -S '[.items[] | {name:.metadata.name,uid:.metadata.uid,claim_namespace:.spec.claimRef.namespace,claim_name:.spec.claimRef.name,claim_uid:.spec.claimRef.uid}] | sort_by(.name)' >"$pv_before"
[ "$(jq length "$pv_before")" -eq 5 ] && jq -e --arg ns "$namespace" 'all(.[]; .uid!="" and .claim_namespace==$ns and .claim_name!="" and .claim_uid!="")' "$pv_before" >/dev/null
jq -e --slurpfile pvc "$pvc_before" 'all(.[]; . as $pv | any($pvc[0][]; .name==$pv.claim_name and .uid==$pv.claim_uid and .volume==$pv.name))' "$pv_before" >/dev/null

for pod in $(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get pods -l "hakopod.io/managed-platform-id=$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -o name); do
  component=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get "$pod" -o jsonpath='{.metadata.labels.app\.kubernetes\.io/component}')
  expected_uid=$(jq -er --arg c "$component" '.[$c].uid' "$HAKOPOD_ACCEPTANCE_IDENTITIES")
  expected_gid=$(jq -er --arg c "$component" '.[$c].gid' "$HAKOPOD_ACCEPTANCE_IDENTITIES")
  process=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec "$pod" -- /bin/sh -ceu 'printf "%s:%s\n" "$(id -u)" "$(id -g)"; awk "/^(CapInh|CapPrm|CapEff|CapBnd|CapAmb|NoNewPrivs|Seccomp):/{print}" /proc/1/status; if : > /hakopod-rootfs-write-test 2>/dev/null; then exit 41; fi')
  printf '%s\n' "$process" | grep -Fx "$expected_uid:$expected_gid" >/dev/null
  printf '%s\n' "$process" | awk '/^Cap(Inh|Prm|Eff|Bnd|Amb):/{if ($2!="0000000000000000") exit 1} /^NoNewPrivs:/{if ($2!="1") exit 1; seen=1} /^Seccomp:/{if ($2!="2") exit 1; seccomp=1} END{exit seen&&seccomp?0:1}'
done

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

row_id=hakopod-test-$(date +%s)
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec statefulset/supabase-database -- psql -U postgres -v ON_ERROR_STOP=1 -c 'create table if not exists public.hakopod_acceptance (id text primary key, owner uuid not null default auth.uid(), value text not null); alter table public.hakopod_acceptance enable row level security; drop policy if exists hakopod_acceptance_owner on public.hakopod_acceptance; create policy hakopod_acceptance_owner on public.hakopod_acceptance using (owner = auth.uid()) with check (owner = auth.uid()); grant select,insert,update,delete on public.hakopod_acceptance to anon, authenticated, service_role; do $$ begin if not exists (select 1 from pg_publication_tables where pubname='"'"'supabase_realtime'"'"' and schemaname='"'"'public'"'"' and tablename='"'"'hakopod_acceptance'"'"') then alter publication supabase_realtime add table public.hakopod_acceptance; end if; end $$;' >/dev/null
curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/rest/v1/hakopod_acceptance" --header @"$owner_headers" -H 'content-type: application/json' -H 'prefer: return=minimal' --data "{\"id\":\"$row_id\",\"value\":\"before-restart\"}"
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/rest/v1/hakopod_acceptance?id=eq.$row_id&select=id,value" --header @"$service_headers" | jq -e --arg id "$row_id" 'length==1 and .[0].id==$id and .[0].value=="before-restart"' >/dev/null
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/rest/v1/hakopod_acceptance?id=eq.$row_id&select=id" --header @"$anon_headers" | jq -e 'length==0' >/dev/null
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/rest/v1/hakopod_acceptance?id=eq.$row_id&select=id" --header @"$second_headers" | jq -e 'length==0' >/dev/null
second_update=$(curl $curl_tls --fail --silent --show-error --max-time 10 -X PATCH "$base/rest/v1/hakopod_acceptance?id=eq.$row_id" --header @"$second_headers" -H 'content-type: application/json' -H 'prefer: return=representation' --data '{"value":"forbidden-cross-user-write"}')
[ "$second_update" = '[]' ]
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/rest/v1/hakopod_acceptance?id=eq.$row_id&select=value" --header @"$owner_headers" | jq -e 'length==1 and .[0].value=="before-restart"' >/dev/null

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

bucket=hakopod-test-native
object=roundtrip.txt
control_object=control.txt
curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/storage/v1/bucket" --header @"$service_headers" -H 'content-type: application/json' --data "{\"id\":\"$bucket\",\"name\":\"$bucket\",\"public\":false}" >/dev/null
printf 'hakopod-native-storage' | curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/storage/v1/object/$bucket/$object" --header @"$service_headers" -H 'content-type: text/plain' --data-binary @- >/dev/null
printf 'hakopod-native-control' | curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/storage/v1/object/$bucket/$control_object" --header @"$service_headers" -H 'content-type: text/plain' --data-binary @- >/dev/null
[ "$(curl $curl_tls --fail --silent --show-error --max-time 10 "$base/storage/v1/object/authenticated/$bucket/$object" --header @"$service_headers")" = hakopod-native-storage ]
edge_fixture_before=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec deployment/supabase-edge-runtime -- sha256sum /home/deno/functions/hello/index.ts | awk '{print $1}')
[ -n "$edge_fixture_before" ]
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/functions/v1/hello" --header @"$owner_headers" | jq -e '. == {message:"Hello from Edge Functions!"}' >/dev/null
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/" >/dev/null
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec deployment/supabase-studio -- node -e 'fetch("http://meta:8080/tables?included_schemas=public").then(r => { if (!r.ok) process.exit(1) })'

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

platform=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --header @"$api_headers")
revision=$(printf '%s' "$platform" | jq -er '.revision')
rotation_request=$work_dir/rotation-request.json
rotation_response=$work_dir/rotation-response.json
printf '%s' "$platform" | jq --argjson revision "$revision" '{id:.id,project:.project,environment:.environment,expected_revision:$revision,kind:"update",confirm_name:.spec.name,spec:(.spec | .secrets["database-owner-password"].revision += 1)}' >"$rotation_request"
rotation_status=$(curl --max-filesize 1048576 --silent --show-error --output "$rotation_response" --write-out '%{http_code}' --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/reviews" --header @"$api_headers" -H 'content-type: application/json' --data-binary @"$rotation_request")
[ "$rotation_status" = 400 ]
jq -e '.. | strings | select(contains("secret rotation for database-owner-password is unavailable"))' "$rotation_response" >/dev/null || { echo "uncoordinated database credential rotation was not refused explicitly" >&2; exit 1; }
update_review=$(printf '%s' "$platform" | jq --argjson revision "$revision" '{id:.id,project:.project,environment:.environment,expected_revision:$revision,kind:"update",confirm_name:.spec.name,spec:.spec}' | curl --max-filesize 1048576 --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/reviews" --header @"$api_headers" -H 'content-type: application/json' --data-binary @-)
printf '%s' "$update_review" | jq -e '.blocked==false and .review.id!=null' >/dev/null
update_operation=$(printf '%s' "$update_review" | jq '{id:.platform.id,project:.platform.project,environment:.platform.environment,expected_revision:.review.expected_revision,kind:"update",confirm_name:.platform.spec.name,spec:.platform.spec,review:.review}' | curl --max-filesize 1048576 --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/operations" --header @"$api_headers" -H "Idempotency-Key: hakopod-test-update-$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -H 'content-type: application/json' --data-binary @-)
update_operation_id=$(printf '%s' "$update_operation" | jq -er '.id')
tries=0
while :; do update_status=$(curl --max-filesize 1048576 --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-operations/$update_operation_id" --header @"$api_headers" | jq -er '.status'); [ "$update_status" = succeeded ] && break; [ "$update_status" != failed ] && [ "$update_status" != cancelled ] || { echo "Supabase update failed" >&2; exit 1; }; tries=$((tries+1)); [ "$tries" -lt 180 ] || { echo "Supabase update timed out" >&2; exit 1; }; sleep 2; done
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get pvc -o json | jq -S '[.items[] | {name:.metadata.name,uid:.metadata.uid,volume:.spec.volumeName,storage_class:.spec.storageClassName,access_modes:.spec.accessModes,request:.spec.resources.requests.storage}] | sort_by(.name)' >"$pvc_after"
cmp -s "$pvc_before" "$pvc_after" || { echo "Supabase PVC or PV identity changed across no-change update" >&2; exit 1; }
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get pv $(jq -r '.[].volume' "$pvc_after") -o json | jq -S '[.items[] | {name:.metadata.name,uid:.metadata.uid,claim_namespace:.spec.claimRef.namespace,claim_name:.spec.claimRef.name,claim_uid:.spec.claimRef.uid}] | sort_by(.name)' >"$pv_after"
cmp -s "$pv_before" "$pv_after" || { echo "Supabase PV resource identity changed across no-change update" >&2; exit 1; }
[ "$(curl $curl_tls --fail --silent --show-error --max-time 10 "$base/storage/v1/object/authenticated/$bucket/$object" --header @"$service_headers")" = hakopod-native-storage ]

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
curl $target_curl_tls --fail --silent --show-error --max-time 10 "$target_base/rest/v1/hakopod_acceptance?id=eq.$row_id&select=id,value" --header @"$target_service_headers" | jq -e --arg id "$row_id" 'length==1 and .[0].id==$id and .[0].value=="realtime-observed"' >/dev/null
[ "$(curl $target_curl_tls --fail --silent --show-error --max-time 10 "$target_base/storage/v1/object/authenticated/$bucket/$object" --header @"$target_service_headers")" = hakopod-native-storage ]
curl $target_curl_tls --fail --silent --show-error --max-time 10 "$target_base/functions/v1/hello" --header @"$target_service_headers" | jq -e '. == {message:"Hello from Edge Functions!"}' >/dev/null
[ "$(curl $curl_tls --fail --silent --show-error --max-time 10 "$base/storage/v1/object/authenticated/$bucket/$object" --header @"$service_headers")" = hakopod-native-storage ]
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

curl $curl_tls --fail --silent --show-error --max-time 10 -X DELETE "$base/storage/v1/object/$bucket" --header @"$service_headers" -H 'content-type: application/json' --data "{\"prefixes\":[\"$object\"]}" | jq -e 'type=="array" and length==1' >/dev/null
removed_body=$work_dir/storage-removed.json
removed_status=$(curl $curl_tls --silent --show-error --output "$removed_body" --write-out '%{http_code}' --max-time 10 "$base/storage/v1/object/info/authenticated/$bucket/$object" --header @"$service_headers")
[ "$removed_status" = 400 ] && jq -e '.statusCode=="404" and .error=="not_found" and .message=="Object not found" and .code=="NoSuchKey"' "$removed_body" >/dev/null
[ "$(curl $curl_tls --fail --silent --show-error --max-time 10 "$base/storage/v1/object/authenticated/$bucket/$control_object" --header @"$service_headers")" = hakopod-native-control ]
curl $curl_tls --fail --silent --show-error --max-time 10 -X DELETE "$base/storage/v1/object/$bucket" --header @"$service_headers" -H 'content-type: application/json' --data "{\"prefixes\":[\"$control_object\"]}" | jq -e 'type=="array" and length==1' >/dev/null
curl $curl_tls --fail --silent --show-error --max-time 10 -X DELETE "$base/storage/v1/bucket/$bucket" --header @"$service_headers" >/dev/null
curl $curl_tls --fail --silent --show-error --max-time 10 -X DELETE "$base/rest/v1/hakopod_acceptance?id=eq.$row_id" --header @"$service_headers" >/dev/null

capture_logs after-recovery
for header_file in "$api_headers" "$anon_headers" "$owner_headers" "$second_headers" "$service_headers" "$target_service_headers"; do
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
printf '{"context":"%s","platform_id":"%s","namespace_uid":"%s","operation_id":"%s","status":"passed"}\n' "$expected_context" "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" "$namespace_uid" "$operation_id"
