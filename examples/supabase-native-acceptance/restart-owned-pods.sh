#!/bin/sh
set -eu
umask 077

: "${KUBECONFIG:?set the protected development kubeconfig}"
: "${HAKOPOD_SUPABASE_NAMESPACE:?set the disposable namespace}"
: "${HAKOPOD_ACCEPTANCE_PLATFORM_ID:?set the disposable platform ID}"
: "${HAKOPOD_ACCEPTANCE_NAMESPACE_UID:?set the recorded namespace UID}"
: "${HAKOPOD_ACCEPTANCE_IMAGES:?set the reviewed image map}"

kubectl_bin=${KUBECTL_BIN:-kubectl}
context=k3d-hakopod-dev
[ "$($kubectl_bin --request-timeout=15s --kubeconfig "$KUBECONFIG" config current-context)" = "$context" ]
k() { "$kubectl_bin" --request-timeout=15s --kubeconfig "$KUBECONFIG" --context "$context" -n "$HAKOPOD_SUPABASE_NAMESPACE" "$@"; }
work=$(mktemp -d /tmp/hakopod-supabase-restart.XXXXXX)
chmod 700 "$work"
proxy_pid=
cleanup() { [ -z "$proxy_pid" ] || { kill "$proxy_pid" 2>/dev/null || true; wait "$proxy_pid" 2>/dev/null || true; }; rm -rf "$work"; }
trap cleanup EXIT HUP INT TERM

k get pods --chunk-size=32 -l "hakopod.io/managed-platform-id=$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -o json >"$work/pods.json"
jq -e '(.items|length)==11 and (.items|map(.metadata.labels["app.kubernetes.io/component"])|unique|length)==11' "$work/pods.json" >/dev/null
jq -r '.items[]|[.metadata.name,.metadata.uid,.metadata.labels["app.kubernetes.io/component"],.metadata.ownerReferences[0].kind,.metadata.ownerReferences[0].name,.metadata.ownerReferences[0].uid]|@tsv' "$work/pods.json" >"$work/inventory.tsv"
: >"$work/controllers.tsv"

while IFS="$(printf '\t')" read -r name uid component owner_kind owner_name owner_uid; do
  [ -n "$name" ] && [ -n "$uid" ] && [ -n "$component" ] && [ -n "$owner_uid" ]
  case "$owner_kind" in
    StatefulSet)
      owner=$(k get statefulset "$owner_name" -o json)
      printf '%s' "$owner" | jq -e --arg uid "$owner_uid" '.metadata.uid==$uid' >/dev/null
      printf '%s\t%s\t%s\n' "$component" StatefulSet "$owner_uid" >>"$work/controllers.tsv"
      ;;
    ReplicaSet)
      owner=$(k get replicaset "$owner_name" -o json)
      deployment_name=$(printf '%s' "$owner" | jq -er --arg uid "$owner_uid" '.metadata as $m | if $m.uid==$uid and ($m.ownerReferences|length)==1 and $m.ownerReferences[0].kind=="Deployment" then $m.ownerReferences[0].name else error("invalid ReplicaSet owner") end')
      deployment_uid=$(printf '%s' "$owner" | jq -er '.metadata.ownerReferences[0].uid')
      owner=$(k get deployment "$deployment_name" -o json)
      printf '%s' "$owner" | jq -e --arg uid "$deployment_uid" '.metadata.uid==$uid' >/dev/null
      printf '%s\t%s\t%s\n' "$component" Deployment "$deployment_uid" >>"$work/controllers.tsv"
      ;;
    *) echo "unexpected owner for $component Pod" >&2; exit 1;;
  esac
  printf '%s' "$owner" | jq -e --arg nsuid "$HAKOPOD_ACCEPTANCE_NAMESPACE_UID" --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" '.metadata.labels["hakopod.io/managed-platform-id"]==$id and (.metadata.ownerReferences|length)==1 and .metadata.ownerReferences[0].kind=="Namespace" and .metadata.ownerReferences[0].uid==$nsuid' >/dev/null
done <"$work/inventory.tsv"

port=$((29000 + ($$ % 9000)))
"$kubectl_bin" --request-timeout=15s --kubeconfig "$KUBECONFIG" --context "$context" proxy --port="$port" --accept-hosts='^127\.0\.0\.1$' >"$work/proxy.log" 2>&1 &
proxy_pid=$!
tries=0
until curl --fail --silent --max-time 1 "http://127.0.0.1:$port/version" >/dev/null 2>&1; do tries=$((tries+1)); [ "$tries" -lt 20 ] || exit 1; sleep 1; done
while IFS="$(printf '\t')" read -r name uid component owner_kind owner_name owner_uid; do
  current=$(k get pod "$name" -o json)
  [ "$(printf '%s' "$current" | jq -er '.metadata.uid')" = "$uid" ] || { echo "$component Pod UID changed before fenced deletion" >&2; exit 1; }
  resource_version=$(printf '%s' "$current" | jq -er '.metadata.resourceVersion')
  body=$(jq -cn --arg uid "$uid" --arg rv "$resource_version" '{apiVersion:"v1",kind:"DeleteOptions",gracePeriodSeconds:0,preconditions:{uid:$uid,resourceVersion:$rv}}')
  curl --fail --silent --show-error --max-time 15 -X DELETE "http://127.0.0.1:$port/api/v1/namespaces/$HAKOPOD_SUPABASE_NAMESPACE/pods/$name" -H 'content-type: application/json' --data-binary "$body" >/dev/null
done <"$work/inventory.tsv"

while IFS="$(printf '\t')" read -r name uid component owner_kind owner_name owner_uid; do
  tries=0
  while [ "$tries" -lt 180 ]; do
    pods=$(k get pods --chunk-size=8 -l "app.kubernetes.io/component=$component,hakopod.io/managed-platform-id=$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -o json)
    new_uid=$(printf '%s' "$pods" | jq -r --arg old "$uid" 'if (.items|length)==1 and .items[0].metadata.uid!=$old and .items[0].status.phase=="Running" and ([.items[0].status.containerStatuses[]?]|length)>0 and all(.items[0].status.containerStatuses[]; .ready==true) then .items[0].metadata.uid else "" end')
    [ -n "$new_uid" ] && break
    tries=$((tries+1)); sleep 1
  done
  [ -n "$new_uid" ] || { echo "$component replacement did not become ready with a new UID" >&2; exit 1; }
  new_name=$(printf '%s' "$pods" | jq -er '.items[0].metadata.name')
  new_owner_kind=$(printf '%s' "$pods" | jq -er 'if (.items[0].metadata.ownerReferences|length)==1 then .items[0].metadata.ownerReferences[0].kind else error("replacement Pod owner count changed") end')
  new_owner_name=$(printf '%s' "$pods" | jq -er '.items[0].metadata.ownerReferences[0].name')
  new_owner_uid=$(printf '%s' "$pods" | jq -er '.items[0].metadata.ownerReferences[0].uid')
  case "$new_owner_kind" in
    StatefulSet)
      new_owner=$(k get statefulset "$new_owner_name" -o json)
      expected_controller_uid=$(awk -F '\t' -v component="$component" '$1==component && $2=="StatefulSet" {print $3}' "$work/controllers.tsv")
      printf '%s' "$new_owner" | jq -e --arg uid "$new_owner_uid" --arg nsuid "$HAKOPOD_ACCEPTANCE_NAMESPACE_UID" --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" '.metadata.uid==$uid and .metadata.labels["hakopod.io/managed-platform-id"]==$id and (.metadata.ownerReferences|length)==1 and .metadata.ownerReferences[0].kind=="Namespace" and .metadata.ownerReferences[0].uid==$nsuid' >/dev/null
      [ "$new_owner_uid" = "$expected_controller_uid" ] || { echo "$component StatefulSet UID changed during Pod replacement" >&2; exit 1; }
      ;;
    ReplicaSet)
      new_rs=$(k get replicaset "$new_owner_name" -o json)
      new_deployment_name=$(printf '%s' "$new_rs" | jq -er --arg uid "$new_owner_uid" '.metadata as $m | if $m.uid==$uid and ($m.ownerReferences|length)==1 and $m.ownerReferences[0].kind=="Deployment" then $m.ownerReferences[0].name else error("replacement ReplicaSet owner changed") end')
      new_deployment_uid=$(printf '%s' "$new_rs" | jq -er '.metadata.ownerReferences[0].uid')
      new_deployment=$(k get deployment "$new_deployment_name" -o json)
      expected_controller_uid=$(awk -F '\t' -v component="$component" '$1==component && $2=="Deployment" {print $3}' "$work/controllers.tsv")
      printf '%s' "$new_deployment" | jq -e --arg uid "$new_deployment_uid" --arg nsuid "$HAKOPOD_ACCEPTANCE_NAMESPACE_UID" --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" '.metadata.uid==$uid and .metadata.labels["hakopod.io/managed-platform-id"]==$id and (.metadata.ownerReferences|length)==1 and .metadata.ownerReferences[0].kind=="Namespace" and .metadata.ownerReferences[0].uid==$nsuid' >/dev/null
      [ "$new_deployment_uid" = "$expected_controller_uid" ] || { echo "$component Deployment UID changed during Pod replacement" >&2; exit 1; }
      ;;
    *) echo "unexpected owner for replacement $component Pod $new_name" >&2; exit 1;;
  esac
  expected=$(jq -er --arg component "$component" '.[$component]' "$HAKOPOD_ACCEPTANCE_IMAGES")
  expected_digest=${expected##*@}
  printf '%s' "$pods" | jq -e --arg digest "$expected_digest" 'all(.items[0].status.containerStatuses[]; (.imageID|endswith($digest)))' >/dev/null || { echo "$component replacement image digest changed" >&2; exit 1; }
done <"$work/inventory.tsv"

echo "all eleven owned Pods were UID-fenced and replaced by ready digest-matched Pods"
