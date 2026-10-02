#!/bin/sh
set -eu
umask 077

: "${KUBECONFIG:?set the dedicated Hakopod development kubeconfig}"
: "${HAKOPOD_ACCEPTANCE_API_URL:?set the API base URL}"
: "${HAKOPOD_ACCEPTANCE_API_HEADERS:?set a protected curl header file}"
: "${HAKOPOD_ACCEPTANCE_PROJECT:?set the project}"
: "${HAKOPOD_ACCEPTANCE_ENVIRONMENT:?set the environment}"
: "${HAKOPOD_ACCEPTANCE_PLATFORM_ID:?set the source platform ID}"
: "${HAKOPOD_ACCEPTANCE_NAMESPACE_UID:?set the observed source namespace UID}"
: "${HAKOPOD_ACCEPTANCE_OWNER_OPERATION_ID:?set the source namespace owner operation ID}"
: "${HAKOPOD_ACCEPTANCE_RESOURCE_INTENT_ID:?set the source namespace resource intent ID}"
: "${HAKOPOD_ACCEPTANCE_RECOVERY_DESTINATION_ID:?set the tested backup destination ID}"
: "${HAKOPOD_ACCEPTANCE_RECOVERY_DESTINATION_REVISION:?set its current revision}"

expected_context=k3d-hakopod-dev
namespace=managed-platform-$HAKOPOD_ACCEPTANCE_PLATFORM_ID
deadline=$(($(date +%s) + 240))

[ -f "$HAKOPOD_ACCEPTANCE_API_HEADERS" ] && [ ! -L "$HAKOPOD_ACCEPTANCE_API_HEADERS" ] || { echo "protected API header file is invalid" >&2; exit 2; }
header_metadata=$(stat -c '%u:%a:%s' "$HAKOPOD_ACCEPTANCE_API_HEADERS")
header_owner=${header_metadata%%:*}; header_rest=${header_metadata#*:}; header_mode=${header_rest%%:*}; header_size=${header_metadata##*:}
[ "$header_owner" = "$(id -u)" ] && [ "$header_mode" = 600 ] && [ "$header_size" -gt 0 ] && [ "$header_size" -le 65536 ] || { echo "protected API header file ownership, mode or size is invalid" >&2; exit 2; }
[ "$(kubectl --kubeconfig "$KUBECONFIG" config current-context)" = "$expected_context" ] || { echo "refusing another Kubernetes context" >&2; exit 2; }

api() {
  [ "$(date +%s)" -lt "$deadline" ] || { echo "cancellation acceptance exceeded its deadline" >&2; return 1; }
  curl --max-filesize 1048576 --fail --silent --show-error --connect-timeout 5 --max-time 10 \
    --header @"$HAKOPOD_ACCEPTANCE_API_HEADERS" "$@"
}

namespace_identity() {
  kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" --request-timeout=10s \
    get namespace "$namespace" -o json |
    jq -cer --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --arg uid "$HAKOPOD_ACCEPTANCE_NAMESPACE_UID" --arg owner "$HAKOPOD_ACCEPTANCE_OWNER_OPERATION_ID" --arg intent "$HAKOPOD_ACCEPTANCE_RESOURCE_INTENT_ID" '
      .metadata as $m |
      select($m.uid==$uid and $m.deletionTimestamp==null and
        $m.labels["app.kubernetes.io/managed-by"]=="hakopod" and
        $m.labels["hakopod.io/managed-platform-id"]==$id) |
      {uid:$m.uid,managed_by:$m.labels["app.kubernetes.io/managed-by"],platform_id:$m.labels["hakopod.io/managed-platform-id"],owner_operation_id:$m.labels["hakopod.io/owner-operation-id"],resource_intent_id:$m.labels["hakopod.io/resource-intent-id"]} |
      select(.owner_operation_id==$owner and .resource_intent_id==$intent)'
}

source_before=$(api "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_PLATFORM_ID")
source_revision=$(printf '%s' "$source_before" | jq -er --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --arg project "$HAKOPOD_ACCEPTANCE_PROJECT" --arg environment "$HAKOPOD_ACCEPTANCE_ENVIRONMENT" '
  select(.id==$id and .project==$project and .environment==$environment and .spec.kind=="supabase" and .status=="ready" and .deleted_at==null) | .revision')
source_fingerprint=$(printf '%s' "$source_before" | jq -cS '{id,project,environment,revision,spec,status,observation,reserved_cpu_milli,reserved_memory_bytes,reserved_storage_gib}')
namespace_before=$(namespace_identity)

intent=$(jq -cn --arg project "$HAKOPOD_ACCEPTANCE_PROJECT" --arg environment "$HAKOPOD_ACCEPTANCE_ENVIRONMENT" \
  --arg source "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --arg destination "$HAKOPOD_ACCEPTANCE_RECOVERY_DESTINATION_ID" \
  --argjson source_revision "$source_revision" --argjson destination_revision "$HAKOPOD_ACCEPTANCE_RECOVERY_DESTINATION_REVISION" \
  '{kind:"backup",project:$project,environment:$environment,source_platform_id:$source,destination_id:$destination,destination_revision:$destination_revision,expected_source_revision:$source_revision}')
review=$(printf '%s' "$intent" | api -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-recovery/reviews" -H 'content-type: application/json' --data-binary @-)
request=$(jq -cn --argjson intent "$intent" --argjson review "$review" '$intent+{review:$review}')
idempotency=hakopod-supabase-cancel-$(date +%s)-$$
operation=$(printf '%s' "$request" | api -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-recovery/operations" \
  -H "Idempotency-Key: $idempotency" -H 'content-type: application/json' --data-binary @-)
operation_id=$(printf '%s' "$operation" | jq -er --arg source "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" '
  select(.kind=="backup" and .source_platform_id==$source and (.status=="queued" or .status=="running")) | .id | select(test("^[0-9a-f]{32}$"))')

cancelled=$(api -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-recovery-operations/$operation_id/cancel" -H 'content-type: application/json' --data-binary '{}')
printf '%s' "$cancelled" | jq -e '. == {cancel_requested:true}' >/dev/null

while :; do
  current=$(api "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-recovery-operations/$operation_id")
  status=$(printf '%s' "$current" | jq -er --arg id "$operation_id" --arg source "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" '
    select(.id==$id and .kind=="backup" and .source_platform_id==$source) | .status')
  case "$status" in
    cancelled) break ;;
    succeeded) echo "backup completed before cancellation; this run does not prove cancellation" >&2; exit 1 ;;
    failed) echo "backup failed instead of reaching the cancelled state" >&2; exit 1 ;;
    queued|running) ;;
    *) echo "backup returned an unknown state" >&2; exit 1 ;;
  esac
  [ "$(date +%s)" -lt "$deadline" ] || { echo "cancelled state was not observed before the deadline" >&2; exit 1; }
  sleep 1
done

printf '%s' "$current" | jq -e '
  .status=="cancelled" and .cancel_requested==true and
  ((.result_artifact_id // "")=="") and ((.artifact_id // "")=="")' >/dev/null || {
    echo "cancelled backup published or retained an artifact" >&2; exit 1;
  }

source_after=$(api "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_PLATFORM_ID")
source_after_fingerprint=$(printf '%s' "$source_after" | jq -cS '{id,project,environment,revision,spec,status,observation,reserved_cpu_milli,reserved_memory_bytes,reserved_storage_gib}')
[ "$source_after_fingerprint" = "$source_fingerprint" ] || { echo "source platform changed during cancelled recovery" >&2; exit 1; }
[ "$(printf '%s' "$source_after" | jq -r '.status')" = ready ] || { echo "source platform is not ready after cancellation" >&2; exit 1; }
namespace_after=$(namespace_identity)
[ "$namespace_after" = "$namespace_before" ] || { echo "source namespace ownership changed during cancellation" >&2; exit 1; }

jq -cn --arg operation_id "$operation_id" --arg source_platform_id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" \
  --arg namespace "$namespace" --arg namespace_uid "$HAKOPOD_ACCEPTANCE_NAMESPACE_UID" \
  '{schema_version:1,status:"passed",case:"cancellation-recovery",operation_id:$operation_id,source_platform_id:$source_platform_id,namespace:$namespace,namespace_uid:$namespace_uid,artifact_absent:true,source_ready_unchanged:true,ownership_unchanged:true}'
