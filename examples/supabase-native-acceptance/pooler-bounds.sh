#!/bin/sh
set -eu
umask 077

: "${KUBECONFIG:?set the protected development kubeconfig}"
: "${HAKOPOD_ACCEPTANCE_SPEC:?set the reviewed Supabase specification}"
: "${HAKOPOD_ACCEPTANCE_PLATFORM_ID:?set the platform ID}"
: "${HAKOPOD_ACCEPTANCE_NAMESPACE_UID:?set the namespace UID}"
: "${HAKOPOD_ACCEPTANCE_OWNER_OPERATION_ID:?set the owner operation ID}"
: "${HAKOPOD_ACCEPTANCE_RESOURCE_INTENT_ID:?set the resource intent ID}"

context=k3d-hakopod-dev
namespace=managed-platform-$HAKOPOD_ACCEPTANCE_PLATFORM_ID
k() { kubectl --request-timeout=15s --kubeconfig "$KUBECONFIG" --context "$context" -n "$namespace" "$@"; }
[ "$(kubectl --request-timeout=15s --kubeconfig "$KUBECONFIG" config current-context)" = "$context" ]
namespace_json=$(kubectl --request-timeout=15s --kubeconfig "$KUBECONFIG" --context "$context" get namespace "$namespace" -o json)
printf '%s' "$namespace_json" | jq -e --arg uid "$HAKOPOD_ACCEPTANCE_NAMESPACE_UID" --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --arg owner "$HAKOPOD_ACCEPTANCE_OWNER_OPERATION_ID" --arg intent "$HAKOPOD_ACCEPTANCE_RESOURCE_INTENT_ID" '.metadata.uid==$uid and .metadata.deletionTimestamp==null and .metadata.labels["app.kubernetes.io/managed-by"]=="hakopod" and .metadata.labels["hakopod.io/managed-platform-id"]==$id and .metadata.labels["hakopod.io/owner-operation-id"]==$owner and .metadata.labels["hakopod.io/resource-intent-id"]==$intent' >/dev/null

pool_size=$(jq -er '.supabase.pool_size | select(type=="number" and .>=1 and .<=64 and floor==.)' "$HAKOPOD_ACCEPTANCE_SPEC")
max_clients=$(jq -er '.supabase.pool_max_clients | select(type=="number" and .>=1 and .<=64 and floor==.)' "$HAKOPOD_ACCEPTANCE_SPEC")
[ "$max_clients" -ge "$pool_size" ]
pooler=$(k get pod -l app.kubernetes.io/component=pooler -o json | jq -er '.items | select(length==1) | .[0] | select(.status.phase=="Running") | .metadata.name')
database=$(k get pod -l app.kubernetes.io/component=database -o json | jq -er '.items | select(length==1) | .[0] | select(.status.phase=="Running") | .metadata.name')
pooler_pod=$(k get pod "$pooler" -o json)
printf '%s' "$pooler_pod" | jq -e --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --arg ns "$namespace" '.metadata.namespace==$ns and .metadata.labels["hakopod.io/managed-platform-id"]==$id and (.metadata.ownerReferences|length)==1 and .metadata.ownerReferences[0].controller==true and .metadata.ownerReferences[0].kind=="ReplicaSet"' >/dev/null
replica_name=$(printf '%s' "$pooler_pod" | jq -er '.metadata.ownerReferences[0].name')
replica_uid=$(printf '%s' "$pooler_pod" | jq -er '.metadata.ownerReferences[0].uid')
replica=$(k get replicaset "$replica_name" -o json)
printf '%s' "$replica" | jq -e --arg uid "$replica_uid" --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --arg ns "$namespace" '.metadata.namespace==$ns and .metadata.uid==$uid and .metadata.labels["hakopod.io/managed-platform-id"]==$id and (.metadata.ownerReferences|length)==1 and .metadata.ownerReferences[0].controller==true and .metadata.ownerReferences[0].kind=="Deployment"' >/dev/null
deployment_name=$(printf '%s' "$replica" | jq -er '.metadata.ownerReferences[0].name')
deployment_uid=$(printf '%s' "$replica" | jq -er '.metadata.ownerReferences[0].uid')
k get deployment "$deployment_name" -o json | jq -e --arg uid "$deployment_uid" --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --arg ns "$namespace" '.metadata.namespace==$ns and .metadata.uid==$uid and .metadata.deletionTimestamp==null and .metadata.labels["app.kubernetes.io/managed-by"]=="hakopod" and .metadata.labels["hakopod.io/managed-platform-id"]==$id' >/dev/null

timeout 60 kubectl --request-timeout=50s --kubeconfig "$KUBECONFIG" --context "$context" -n "$namespace" exec "$pooler" -- /bin/sh -ceu '
  [ "$POOLER_DEFAULT_POOL_SIZE" = "$1" ] && [ "$POOLER_MAX_CLIENT_CONN" = "$2" ]
  [ "$(id -u):$(id -g)" = "$3" ]
  pids=""
  cleanup() { for pid in $pids; do kill "$pid" 2>/dev/null || true; done; for pid in $pids; do wait "$pid" 2>/dev/null || true; done; rm -f /tmp/pool-bound-*; }
  trap cleanup EXIT HUP INT TERM
  connect() { PGCONNECT_TIMEOUT=3 PGAPPNAME=hakopod_pool_bound PGPASSWORD="$POSTGRES_PASSWORD" stdbuf -oL psql -h 127.0.0.1 -p 6543 -U "postgres.$POOLER_TENANT_ID" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -Atqc "$1"; }
  count=0
  while [ "$count" -lt "$2" ]; do
    fifo=/tmp/pool-bound-$count.fifo; output=/tmp/pool-bound-$count.out; mkfifo "$fifo"
    (printf "select 1;\n"; exec sleep 20) >"$fifo" & pids="$pids $!"
    PGCONNECT_TIMEOUT=3 PGAPPNAME=hakopod_pool_idle PGPASSWORD="$POSTGRES_PASSWORD" stdbuf -oL psql -h 127.0.0.1 -p 6543 -U "postgres.$POOLER_TENANT_ID" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -Atq <"$fifo" >"$output" 2>/dev/null & pids="$pids $!"
    count=$((count+1))
  done
  sleep 2
  count=0; while [ "$count" -lt "$2" ]; do grep -Fx 1 "/tmp/pool-bound-$count.out" >/dev/null || { echo "a permitted pooler client did not authenticate" >&2; exit 40; }; count=$((count+1)); done
  overflow=/tmp/pool-bound-overflow; if connect "select 1" >/dev/null 2>"$overflow"; then echo "pooler admitted a client above its configured maximum" >&2; exit 41; fi
  grep -Eiq "maximum client|too many client|connection limit|max_client" "$overflow" || { echo "overflow did not return the pooler client-limit rejection" >&2; exit 41; }
  cleanup; pids=""
  count=0; active=$(( $1 + 1 ))
  while [ "$count" -lt "$active" ]; do connect "select pg_sleep(15)" >/dev/null 2>&1 & pids="$pids $!"; count=$((count+1)); done
  sleep 2
  backend_count=$(PGCONNECT_TIMEOUT=3 PGSSLMODE=verify-full PGSSLROOTCERT="$DATABASE_SSL_CA_CERT" PGAPPNAME=hakopod_pool_observer PGPASSWORD="$POSTGRES_PASSWORD" psql -h db -U postgres -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -Atqc "select count(*) from pg_stat_activity where application_name=\$\$hakopod_pool_bound\$\$ and pid<>pg_backend_pid()")
  [ "$backend_count" -eq "$1" ] || { echo "pooler did not enforce its exact upstream backend bound" >&2; exit 42; }
  for pid in $pids; do kill -0 "$pid" 2>/dev/null || { echo "an active transaction ended before the bound was observed" >&2; exit 42; }; done
  cleanup; pids=""; connect "select 1" | grep -Fx 1 >/dev/null
' bounds "$pool_size" "$max_clients" "$(jq -er '.pooler | [.uid,.gid] | map(tostring) | join(":")' "${HAKOPOD_ACCEPTANCE_IDENTITIES:?set qualified identities}")"

printf '{"schema_version":1,"status":"passed","case":"pooler-connection-bounds","pool_size":%s,"pool_max_clients":%s,"client_overflow_refused":true,"backend_bound_observed":true,"released_connection_reused":true}\n' "$pool_size" "$max_clients"
