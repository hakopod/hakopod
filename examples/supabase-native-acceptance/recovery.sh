#!/bin/sh
set -eu
umask 077

: "${HAKOPOD_ACCEPTANCE_API_URL:?set the API base URL}"
: "${HAKOPOD_ACCEPTANCE_API_HEADERS:?set a protected curl header file}"
: "${HAKOPOD_ACCEPTANCE_PROJECT:?set the project}"
: "${HAKOPOD_ACCEPTANCE_ENVIRONMENT:?set the environment}"
: "${HAKOPOD_ACCEPTANCE_PLATFORM_ID:?set the source platform ID}"
: "${HAKOPOD_ACCEPTANCE_RECOVERY_DESTINATION_ID:?set the tested backup destination ID}"
: "${HAKOPOD_ACCEPTANCE_RECOVERY_DESTINATION_REVISION:?set its current revision}"
: "${HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_ID:?set a separate empty Supabase target ID}"
: "${HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_REVISION:?set its current revision}"
: "${HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_NAME:?set its exact name}"

[ -f "$HAKOPOD_ACCEPTANCE_API_HEADERS" ] && [ ! -L "$HAKOPOD_ACCEPTANCE_API_HEADERS" ] || { echo "protected API header file is invalid" >&2; exit 2; }
api() { curl --max-filesize 1048576 --fail --silent --show-error --max-time 15 --header @"$HAKOPOD_ACCEPTANCE_API_HEADERS" "$@"; }
poll() {
  operation_id=$1 tries=0
  while [ "$tries" -lt 900 ]; do
    operation=$(api "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-recovery-operations/$operation_id")
    status=$(printf '%s' "$operation" | jq -er '.status')
    [ "$status" = succeeded ] && { printf '%s' "$operation"; return 0; }
    [ "$status" != failed ] && [ "$status" != cancelled ] || { echo "Supabase recovery operation $operation_id ended $status" >&2; return 1; }
    tries=$((tries+1)); sleep 2
  done
  echo "Supabase recovery operation $operation_id timed out" >&2
  return 1
}

source=$(api "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_PLATFORM_ID")
source_revision=$(printf '%s' "$source" | jq -er '.revision')
backup_intent=$(jq -cn --arg kind backup --arg project "$HAKOPOD_ACCEPTANCE_PROJECT" --arg environment "$HAKOPOD_ACCEPTANCE_ENVIRONMENT" --arg source "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --arg destination "$HAKOPOD_ACCEPTANCE_RECOVERY_DESTINATION_ID" --argjson source_revision "$source_revision" --argjson destination_revision "$HAKOPOD_ACCEPTANCE_RECOVERY_DESTINATION_REVISION" '{kind:$kind,project:$project,environment:$environment,source_platform_id:$source,destination_id:$destination,destination_revision:$destination_revision,expected_source_revision:$source_revision}')
backup_review=$(printf '%s' "$backup_intent" | api -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-recovery/reviews" -H 'content-type: application/json' --data-binary @-)
backup_request=$(jq -cn --argjson intent "$backup_intent" --argjson review "$backup_review" '$intent+{review:$review}')
backup_operation=$(printf '%s' "$backup_request" | api -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-recovery/operations" -H "Idempotency-Key: hakopod-supabase-backup-$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -H 'content-type: application/json' --data-binary @-)
backup_result=$(poll "$(printf '%s' "$backup_operation" | jq -er '.id')")
artifact_id=$(printf '%s' "$backup_result" | jq -er '.result_artifact_id | select(test("^[0-9a-f]{32}$"))')

target=$(api "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_ID")
printf '%s' "$target" | jq -e --arg project "$HAKOPOD_ACCEPTANCE_PROJECT" --arg environment "$HAKOPOD_ACCEPTANCE_ENVIRONMENT" --arg name "$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_NAME" --argjson revision "$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_REVISION" '.project==$project and .environment==$environment and .spec.kind=="supabase" and .spec.name==$name and .revision==$revision' >/dev/null
restore_intent=$(jq -cn --arg kind restore --arg project "$HAKOPOD_ACCEPTANCE_PROJECT" --arg environment "$HAKOPOD_ACCEPTANCE_ENVIRONMENT" --arg source "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --arg target "$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_ID" --arg artifact "$artifact_id" --argjson source_revision "$source_revision" --argjson target_revision "$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_REVISION" '{kind:$kind,project:$project,environment:$environment,source_platform_id:$source,target_platform_id:$target,artifact_id:$artifact,expected_source_revision:$source_revision,expected_target_revision:$target_revision}')
restore_review_request=$(jq -cn --argjson intent "$restore_intent" --arg name "$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_NAME" '$intent+{confirm_target_name:$name}')
restore_review=$(printf '%s' "$restore_review_request" | api -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-recovery/reviews" -H 'content-type: application/json' --data-binary @-)
restore_request=$(jq -cn --argjson intent "$restore_intent" --argjson review "$restore_review" --arg name "$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_NAME" '$intent+{review:$review,confirm_target_name:$name}')
restore_operation=$(printf '%s' "$restore_request" | api -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-recovery/operations" -H "Idempotency-Key: hakopod-supabase-restore-$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -H 'content-type: application/json' --data-binary @-)
restore_result=$(poll "$(printf '%s' "$restore_operation" | jq -er '.id')")
printf '%s' "$restore_result" | jq -e --arg artifact "$artifact_id" --arg target "$HAKOPOD_ACCEPTANCE_RECOVERY_TARGET_ID" '.status=="succeeded" and .artifact_id==$artifact and .target_platform_id==$target' >/dev/null
printf '{"artifact_id":"%s","backup_operation_id":"%s","restore_operation_id":"%s","status":"passed"}\n' "$artifact_id" "$(printf '%s' "$backup_operation" | jq -er '.id')" "$(printf '%s' "$restore_operation" | jq -er '.id')"
