#!/bin/sh
set -eu
umask 077

: "${KUBECONFIG:?set the protected development kubeconfig}"
: "${HAKOPOD_SUPABASE_NAMESPACE:?set the disposable managed-platform namespace}"
: "${HAKOPOD_ACCEPTANCE_PLATFORM_ID:?set the disposable managed-platform ID}"
: "${HAKOPOD_ACCEPTANCE_NAMESPACE_UID:?set the recorded disposable namespace UID}"
: "${HAKOPOD_ACCEPTANCE_ADVERSARIAL_CERT_DIR:?set the protected VM certificate directory}"
[ "${HAKOPOD_ACCEPTANCE_DISPOSABLE:-}" = 1 ] || { echo "adversarial TLS requires a disposable fixture" >&2; exit 2; }
scheduling_pool=${HAKOPOD_ACCEPTANCE_SCHEDULING_POOL:-}
scheduling_runtime_class=${HAKOPOD_ACCEPTANCE_SCHEDULING_RUNTIME_CLASS:-}
node_names=[]
node_uids={}
if [ -n "$scheduling_pool$scheduling_runtime_class" ]; then
  [ -n "$scheduling_pool" ] && [ -n "$scheduling_runtime_class" ] && [ -n "${HAKOPOD_ACCEPTANCE_NODE_NAMES_JSON:-}" ] && [ -n "${HAKOPOD_ACCEPTANCE_NODE_UIDS_JSON:-}" ] || { echo "adversarial TLS scheduling policy is incomplete" >&2; exit 2; }
  printf '%s' "$scheduling_pool" | grep -Eq '^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$' || { echo "adversarial TLS scheduling pool is invalid" >&2; exit 2; }
  printf '%s' "$scheduling_runtime_class" | grep -Eq '^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$' || { echo "adversarial TLS scheduling RuntimeClass is invalid" >&2; exit 2; }
  node_names=$(printf '%s' "$HAKOPOD_ACCEPTANCE_NODE_NAMES_JSON" | jq -ce 'select(type=="array" and length>=1 and length<=48 and all(.[]; type=="string" and length>0) and (unique|length)==length)')
  node_uids=$(printf '%s' "$HAKOPOD_ACCEPTANCE_NODE_UIDS_JSON" | jq -ce 'select(type=="object" and length>=1 and length<=48 and all(to_entries[]; (.key|type)=="string" and (.value|type)=="string" and (.value|length)>0))')
  jq -ne --argjson nodes "$node_names" --argjson uids "$node_uids" 'all($nodes[]; $uids[.] != null)' >/dev/null || { echo "adversarial TLS placement is not bound to reviewed node UIDs" >&2; exit 2; }
fi

kubectl_bin=${KUBECTL_BIN:-kubectl}
context=k3d-hakopod-dev
[ "$($kubectl_bin --request-timeout=15s --kubeconfig "$KUBECONFIG" config current-context)" = "$context" ]
k() { "$kubectl_bin" --request-timeout=15s --kubeconfig "$KUBECONFIG" --context "$context" -n "$HAKOPOD_SUPABASE_NAMESPACE" "$@"; }
for command in "$kubectl_bin" curl jq openssl awk grep date stat; do command -v "$command" >/dev/null 2>&1 || { echo "$command is required" >&2; exit 2; }; done

service_json=$(k get service db -o json)
service_uid=$(printf '%s' "$service_json" | jq -er '.metadata.uid')
original_selector=$(printf '%s' "$service_json" | jq -c '.spec.selector')
[ -n "$service_uid" ] && [ "$original_selector" != null ]
database_json=$(k get statefulset supabase-database -o json)
namespace_json=$(k get namespace "$HAKOPOD_SUPABASE_NAMESPACE" -o json)
printf '%s' "$namespace_json" | jq -e --arg uid "$HAKOPOD_ACCEPTANCE_NAMESPACE_UID" --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" '.metadata.uid==$uid and .metadata.labels["hakopod.io/managed-platform-id"]==$id' >/dev/null || { echo "disposable namespace ownership changed" >&2; exit 2; }
printf '%s' "$service_json" | jq -e --arg uid "$HAKOPOD_ACCEPTANCE_NAMESPACE_UID" --arg ns "$HAKOPOD_SUPABASE_NAMESPACE" '.metadata.ownerReferences|length==1 and .[0].uid==$uid and .[0].kind=="Namespace" and .[0].name==$ns' >/dev/null || { echo "db Service ownership is invalid" >&2; exit 2; }
database_set_uid=$(printf '%s' "$database_json" | jq -er '.metadata.uid')
printf '%s' "$database_json" | jq -e --arg uid "$HAKOPOD_ACCEPTANCE_NAMESPACE_UID" --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" '.metadata.labels["hakopod.io/managed-platform-id"]==$id and (.metadata.ownerReferences|length)==1 and .metadata.ownerReferences[0].uid==$uid and .metadata.ownerReferences[0].kind=="Namespace"' >/dev/null || { echo "database StatefulSet ownership is invalid" >&2; exit 2; }
database_pod_json=$(k get pod supabase-database-0 -o json)
printf '%s' "$database_pod_json" | jq -e --arg uid "$database_set_uid" --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" '.metadata.labels["hakopod.io/managed-platform-id"]==$id and (.metadata.ownerReferences|length)==1 and .metadata.ownerReferences[0].uid==$uid and .metadata.ownerReferences[0].kind=="StatefulSet"' >/dev/null || { echo "database Pod ownership is invalid" >&2; exit 2; }
database_image=$(printf '%s' "$database_json" | jq -er '.spec.template.spec.containers[0].image')
database_uid=$(printf '%s' "$database_json" | jq -er '.spec.template.spec.securityContext.runAsUser')
database_gid=$(printf '%s' "$database_json" | jq -er '.spec.template.spec.securityContext.runAsGroup')
platform=$(printf '%s' "$database_json" | jq -er '.metadata.labels["hakopod.io/managed-platform"]')
platform_id=$(printf '%s' "$database_json" | jq -er '.metadata.labels["hakopod.io/managed-platform-id"]')
database_name=$(printf '%s' "$database_json" | jq -er '.spec.template.spec.containers[0].env[] | select(.name=="POSTGRES_DB") | .value')
case "$database_image" in *@sha256:*) ;; *) echo "database image is not digest-pinned" >&2; exit 2;; esac

original_ip=$(k get pod supabase-database-0 -o jsonpath='{.status.podIP}')
original_slices=$(k get endpointslice -l kubernetes.io/service-name=db -o json | jq -c '[.items[]|{name:.metadata.name,uid:.metadata.uid}]|sort_by(.name)')
[ "$(printf '%s' "$original_slices" | jq length)" -eq 1 ] || { echo "db Service must have one EndpointSlice" >&2; exit 2; }
k get endpointslice "$(printf '%s' "$original_slices" | jq -r '.[0].name')" -o json | jq -e --arg ip "$original_ip" '[.endpoints[]?.addresses[]?]==[$ip]' >/dev/null

suffix=$(awk 'BEGIN{srand();printf "%d-%06d",systime(),int(rand()*1000000)}')
active_name=
active_pod_uid=
active_pod_rv=
active_secret_uid=
active_secret_rv=
selector_changed=0
clients_disturbed=0
raw_delete() {
  kind=$1 name=$2 uid=$3 resource_version=$4 port=$((28000 + ($$ % 10000))) log=/tmp/hakopod-proxy-$suffix.log
  "$kubectl_bin" --request-timeout=15s --kubeconfig "$KUBECONFIG" --context "$context" proxy --port="$port" --accept-hosts='^127\.0\.0\.1$' >"$log" 2>&1 & proxy_pid=$!
  tries=0
  until curl --fail --silent --max-time 1 "http://127.0.0.1:$port/version" >/dev/null 2>&1; do
    tries=$((tries+1)); [ "$tries" -lt 20 ] || { kill "$proxy_pid" 2>/dev/null || true; wait "$proxy_pid" 2>/dev/null || true; return 1; }; sleep 1
  done
  body=$(jq -cn --arg uid "$uid" --arg rv "$resource_version" '{apiVersion:"v1",kind:"DeleteOptions",gracePeriodSeconds:0,preconditions:{uid:$uid,resourceVersion:$rv}}')
  plural=${kind}s status=0
  curl --fail --silent --show-error --max-time 15 -X DELETE "http://127.0.0.1:$port/api/v1/namespaces/$HAKOPOD_SUPABASE_NAMESPACE/$plural/$name" -H 'content-type: application/json' --data-binary "$body" >/dev/null || status=$?
  kill "$proxy_pid" 2>/dev/null || true; wait "$proxy_pid" 2>/dev/null || true; rm -f "$log"
  return "$status"
}
restore_selector() {
  [ "$selector_changed" -eq 1 ] || return 0
  patch=$(jq -cn --arg uid "$service_uid" --argjson selector "$original_selector" '[{op:"test",path:"/metadata/uid",value:$uid},{op:"replace",path:"/spec/selector",value:$selector}]')
  k patch service db --type=json -p "$patch" >/dev/null
  selector_changed=0
  [ "$(k get service db -o json | jq -c '.spec.selector')" = "$original_selector" ]
  tries=0
  while [ "$tries" -lt 60 ]; do
    slices=$(k get endpointslice -l kubernetes.io/service-name=db -o json 2>/dev/null || true)
    [ -n "$slices" ] && [ "$(printf '%s' "$slices" | jq -c '[.items[]|{name:.metadata.name,uid:.metadata.uid}]|sort_by(.name)')" = "$original_slices" ] && printf '%s' "$slices" | jq -e --arg ip "$original_ip" '(.items|length)==1 and [.items[0].endpoints[]?.addresses[]?]==[$ip]' >/dev/null 2>&1 && return 0
    tries=$((tries+1)); sleep 1
  done
  echo "original db EndpointSlice was not restored exactly" >&2; return 1
}
cleanup() {
  status=0; restore_selector || status=1
  if [ "$clients_disturbed" -eq 1 ]; then restart_clients || status=1; wait_client_rollouts || status=1; clients_disturbed=0; fi
  if [ -n "$active_name" ] && [ -z "$active_pod_uid" ]; then
    current=$(k get pod "$active_name" -o json 2>/dev/null || true)
    if [ -n "$current" ]; then
      active_pod_uid=$(printf '%s' "$current" | jq -er --arg nsuid "$HAKOPOD_ACCEPTANCE_NAMESPACE_UID" --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" '.metadata as $m | if $m.labels["hakopod.io/managed-platform-id"]==$id and ($m.ownerReferences|length)==1 and $m.ownerReferences[0].kind=="Namespace" and $m.ownerReferences[0].uid==$nsuid then $m.uid else error("unowned adversarial Pod") end') || status=1
    fi
  fi
  if [ -n "$active_pod_uid" ]; then
    current=$(k get pod "$active_name" -o json 2>/dev/null || true)
    [ -n "$current" ] && [ "$(printf '%s' "$current" | jq -r '.metadata.uid')" = "$active_pod_uid" ] && active_pod_rv=$(printf '%s' "$current" | jq -er '.metadata.resourceVersion') && raw_delete pod "$active_name" "$active_pod_uid" "$active_pod_rv" || status=1
  fi
  if [ -n "$active_secret_uid" ]; then
    current=$(k get secret "$active_name" -o json 2>/dev/null || true)
    [ -n "$current" ] && [ "$(printf '%s' "$current" | jq -r '.metadata.uid')" = "$active_secret_uid" ] && active_secret_rv=$(printf '%s' "$current" | jq -er '.metadata.resourceVersion') && raw_delete secret "$active_name" "$active_secret_uid" "$active_secret_rv" || status=1
  fi
  if [ -n "$active_name" ] && [ -z "$active_secret_uid" ]; then
    current=$(k get secret "$active_name" -o json 2>/dev/null || true)
    if [ -n "$current" ]; then
      discovered_uid=$(printf '%s' "$current" | jq -er --arg nsuid "$HAKOPOD_ACCEPTANCE_NAMESPACE_UID" --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" '.metadata as $m | if $m.labels["hakopod.io/managed-platform-id"]==$id and ($m.ownerReferences|length)==1 and $m.ownerReferences[0].kind=="Namespace" and $m.ownerReferences[0].uid==$nsuid then $m.uid else error("unowned adversarial Secret") end') || status=1
      if [ -n "$discovered_uid" ]; then discovered_rv=$(printf '%s' "$current" | jq -er '.metadata.resourceVersion') && raw_delete secret "$active_name" "$discovered_uid" "$discovered_rv" || status=1; fi
    fi
  fi
  return "$status"
}
on_exit() { status=$?; trap - EXIT HUP INT TERM; cleanup || [ "$status" -ne 0 ] || status=1; exit "$status"; }
trap on_exit EXIT
trap 'exit 130' HUP INT TERM

cert_dir=$HAKOPOD_ACCEPTANCE_ADVERSARIAL_CERT_DIR
[ ! -L "$cert_dir" ] && [ -d "$cert_dir" ] && [ "$(stat -c %u "$cert_dir")" -eq "$(id -u)" ] && [ "$(stat -c %a "$cert_dir")" = 700 ] || { echo "certificate directory must be owned mode 700 and not a symlink" >&2; exit 2; }
for file in trusted-ca.crt good-db.crt good-db.key wrong-ca.crt wrong-ca.key wrong-host.crt wrong-host.key; do
  [ -f "$cert_dir/$file" ] && [ ! -L "$cert_dir/$file" ] && [ "$(stat -c %u "$cert_dir/$file")" -eq "$(id -u)" ] || { echo "certificate input must be an owned regular file: $file" >&2; exit 2; }
  mode=$(stat -c %a "$cert_dir/$file")
  case "$file:$mode" in *.key:600|*.crt:400|*.crt:440|*.crt:444|*.crt:600|*.crt:640|*.crt:644) ;; *) echo "unsafe certificate input mode: $file" >&2; exit 2;; esac
done
openssl verify -CAfile "$cert_dir/trusted-ca.crt" -verify_hostname db "$cert_dir/good-db.crt" >/dev/null
! openssl verify -CAfile "$cert_dir/trusted-ca.crt" -verify_hostname db "$cert_dir/wrong-ca.crt" >/dev/null 2>&1
openssl verify -CAfile "$cert_dir/trusted-ca.crt" "$cert_dir/wrong-host.crt" >/dev/null
! openssl verify -CAfile "$cert_dir/trusted-ca.crt" -verify_hostname db "$cert_dir/wrong-host.crt" >/dev/null 2>&1

clients='auth pooler postgres-meta realtime rest storage'
strict_certificate_failure='certificate signed by unknown authority|unknown ca|self[- ]signed certificate|unable to get local issuer|certificate verify failed|tls: bad certificate|hostname verification failed|hostname mismatch|certificate[^[:cntrl:]]*(not valid for|does not match)|no alternative certificate subject name matches'
restart_clients() {
  for component in $clients; do
    pod_json=$(k get pod -l "app.kubernetes.io/component=$component" -o json)
    old_uid=$(printf '%s' "$pod_json" | jq -er --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" '.items|if length==1 and .[0].metadata.labels["hakopod.io/managed-platform-id"]==$id and (.[0].metadata.ownerReferences|length)==1 and .[0].metadata.ownerReferences[0].kind=="ReplicaSet" then .[0].metadata.uid else error("invalid client pod ownership") end')
    old_name=$(printf '%s' "$pod_json" | jq -er '.items[0].metadata.name')
    old_rv=$(printf '%s' "$pod_json" | jq -er '.items[0].metadata.resourceVersion')
    rs_name=$(printf '%s' "$pod_json" | jq -er '.items[0].metadata.ownerReferences[0].name')
    rs_uid=$(printf '%s' "$pod_json" | jq -er '.items[0].metadata.ownerReferences[0].uid')
    rs_json=$(k get replicaset "$rs_name" -o json)
    deployment_json=$(k get deployment "supabase-$component" -o json)
    printf '%s' "$deployment_json" | jq -e --arg nsuid "$HAKOPOD_ACCEPTANCE_NAMESPACE_UID" --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" '.metadata.labels["hakopod.io/managed-platform-id"]==$id and (.metadata.ownerReferences|length)==1 and .metadata.ownerReferences[0].uid==$nsuid and .metadata.ownerReferences[0].kind=="Namespace"' >/dev/null || { echo "$component Deployment ownership is invalid" >&2; return 1; }
    deployment_uid=$(printf '%s' "$deployment_json" | jq -er '.metadata.uid')
    printf '%s' "$rs_json" | jq -e --arg uid "$rs_uid" --arg owner "$deployment_uid" '.metadata.uid==$uid and (.metadata.ownerReferences|length)==1 and .metadata.ownerReferences[0].uid==$owner and .metadata.ownerReferences[0].kind=="Deployment"' >/dev/null || { echo "$component ReplicaSet ownership is invalid" >&2; return 1; }
    clients_disturbed=1
    raw_delete pod "$old_name" "$old_uid" "$old_rv"
    tries=0
    while [ "$tries" -lt 90 ]; do
      pods=$(k get pod -l "app.kubernetes.io/component=$component" -o json 2>/dev/null || true)
      new_uid=$(printf '%s' "$pods" | jq -r --arg old "$old_uid" 'if (.items|length)==1 and .items[0].metadata.uid!=$old then .items[0].metadata.uid else "" end')
      [ -n "$new_uid" ] && break
      tries=$((tries+1)); sleep 1
    done
    [ "$tries" -lt 90 ] || { echo "$component replacement UID did not appear" >&2; return 1; }
  done
}
wait_client_rollouts() { for component in $clients; do k rollout status "deployment/supabase-$component" --timeout=5m >/dev/null; done; }
run_phase() {
  phase=$1 cert=$2 key=$3 expected=$4
  active_name=hakopod-db-tls-$phase-$suffix
  ! k get pod "$active_name" >/dev/null 2>&1 && ! k get secret "$active_name" >/dev/null 2>&1 || { echo "fresh adversarial resource name is occupied" >&2; return 1; }
  secret_manifest=$(k create secret generic "$active_name" --from-file=tls.crt="$cert_dir/$cert" --from-file=tls.key="$cert_dir/$key" --dry-run=client -o json | jq --arg nsuid "$HAKOPOD_ACCEPTANCE_NAMESPACE_UID" --arg ns "$HAKOPOD_SUPABASE_NAMESPACE" --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" '.metadata.labels["hakopod.io/managed-platform-id"]=$id | .metadata.ownerReferences=[{apiVersion:"v1",kind:"Namespace",name:$ns,uid:$nsuid}]')
  secret_identity=$(printf '%s' "$secret_manifest" | k create -f - -o json | jq -er '.metadata.uid+" "+.metadata.resourceVersion')
  active_secret_uid=${secret_identity% *}
  active_secret_rv=${secret_identity#* }
  pod_manifest=$(cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $active_name
  ownerReferences:
  - {apiVersion: v1, kind: Namespace, name: $HAKOPOD_SUPABASE_NAMESPACE, uid: $HAKOPOD_ACCEPTANCE_NAMESPACE_UID}
  labels:
    app.kubernetes.io/component: database
    app.kubernetes.io/managed-by: hakopod
    app.kubernetes.io/name: supabase-database
    hakopod.io/managed-platform: $platform
    hakopod.io/managed-platform-id: $platform_id
    hakopod.io/tls-adversary: $phase-$suffix
spec:
  automountServiceAccountToken: false
  enableServiceLinks: false
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: $database_uid
    runAsGroup: $database_gid
    fsGroup: $database_gid
    seccompProfile: {type: RuntimeDefault}
  containers:
  - name: postgres
    image: $database_image
    imagePullPolicy: IfNotPresent
    command: [/bin/sh, -ceu]
    args:
    - |
      cp /cert/tls.crt /work/server.crt
      cp /cert/tls.key /work/server.key
      chmod 600 /work/server.key
      initdb -D /work/data -A trust >/dev/null
      printf 'local all all trust\nhostssl all all 0.0.0.0/0 trust\nhostssl all all ::/0 trust\n' > /work/pg_hba.conf
      postgres -D /work/data -c listen_addresses='' -c unix_socket_directories=/work -c port=55432 -c hba_file=/work/pg_hba.conf &
      bootstrap_pid=\$!
      tries=0
      until pg_isready -h /work -p 55432 >/dev/null 2>&1; do tries=\$((tries+1)); [ "\$tries" -lt 30 ] || exit 1; sleep 1; done
      psql -h /work -p 55432 -U postgres -v ON_ERROR_STOP=1 -c 'CREATE ROLE authenticator LOGIN SUPERUSER; CREATE ROLE pgbouncer LOGIN SUPERUSER; CREATE ROLE supabase_admin LOGIN SUPERUSER; CREATE ROLE supabase_auth_admin LOGIN SUPERUSER; CREATE ROLE supabase_storage_admin LOGIN SUPERUSER; CREATE ROLE supabase_functions_admin LOGIN SUPERUSER; CREATE ROLE hakopod_realtime LOGIN SUPERUSER; CREATE ROLE hakopod_meta LOGIN SUPERUSER;'
      if [ "$database_name" != postgres ]; then createdb -h /work -p 55432 -U postgres "$database_name"; fi
      kill "\$bootstrap_pid"; wait "\$bootstrap_pid"
      exec postgres -D /work/data -c listen_addresses='*' -c port=5432 -c ssl=on -c ssl_min_protocol_version=TLSv1.2 -c ssl_cert_file=/work/server.crt -c ssl_key_file=/work/server.key -c hba_file=/work/pg_hba.conf -c log_connections=on -c log_line_prefix='[%h] '
    ports: [{name: postgres, containerPort: 5432}]
    readinessProbe: {exec: {command: [/bin/sh, -c, 'pg_isready -h 127.0.0.1 -p 5432']}, periodSeconds: 1, timeoutSeconds: 1, failureThreshold: 30}
    resources:
      requests: {cpu: 50m, memory: 128Mi}
      limits: {cpu: 250m, memory: 512Mi}
    securityContext:
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      capabilities: {drop: [ALL]}
      seccompProfile: {type: RuntimeDefault}
    volumeMounts:
    - {name: cert, mountPath: /cert, readOnly: true}
    - {name: work, mountPath: /work}
    - {name: tmp, mountPath: /tmp}
  volumes:
  - name: cert
    secret: {secretName: $active_name, defaultMode: 0440}
  - name: work
    emptyDir: {sizeLimit: 1Gi}
  - name: tmp
    emptyDir: {sizeLimit: 64Mi}
EOF
)
  if [ -n "$scheduling_pool" ]; then
    pod_manifest=$(printf '%s' "$pod_manifest" | k create --dry-run=client -f - -o json | jq --arg pool "$scheduling_pool" --arg runtime "$scheduling_runtime_class" --argjson nodes "$node_names" '.spec.runtimeClassName=$runtime | .spec.nodeSelector["hakopod.com/pool"]=$pool | .spec.tolerations=[{key:"hakopod.com/pool",operator:"Equal",value:$pool,effect:"NoSchedule"}] | .spec.affinity={nodeAffinity:{requiredDuringSchedulingIgnoredDuringExecution:{nodeSelectorTerms:[{matchFields:[{key:"metadata.name",operator:"In",values:$nodes}]}]}}}')
  fi
  pod_json=$(printf '%s' "$pod_manifest" | k create -f - -o json)
  active_pod_uid=$(printf '%s' "$pod_json" | jq -er '.metadata.uid')
  active_pod_rv=$(printf '%s' "$pod_json" | jq -er '.metadata.resourceVersion')
  k wait --for=condition=Ready "pod/$active_name" --timeout=2m >/dev/null
  if [ -n "$scheduling_pool" ]; then
    observed_pod=$(k get pod "$active_name" -o json)
    observed_node=$(printf '%s' "$observed_pod" | jq -er --arg pool "$scheduling_pool" --arg runtime "$scheduling_runtime_class" --argjson nodes "$node_names" '.spec.nodeName as $node | select(.status.phase=="Running" and any(.status.conditions[]?; .type=="Ready" and .status=="True") and ($nodes|index($node))!=null and .spec.runtimeClassName==$runtime and .spec.nodeSelector["hakopod.com/pool"]==$pool and ([.spec.tolerations[]? | select(.key=="hakopod.com/pool" and .operator=="Equal" and .value==$pool and .effect=="NoSchedule")] | length)==1) | $node')
    expected_node_uid=$(printf '%s' "$node_uids" | jq -er --arg node "$observed_node" '.[$node]')
    observed_node_json=$("$kubectl_bin" --request-timeout=15s --kubeconfig "$KUBECONFIG" --context "$context" get node "$observed_node" -o json)
    printf '%s' "$observed_node_json" | jq -e --arg uid "$expected_node_uid" --arg pool "$scheduling_pool" '.metadata.uid==$uid and .spec.unschedulable!=true and .metadata.labels["hakopod.com/pool"]==$pool and any(.status.conditions[]?; .type=="Ready" and .status=="True") and ([.spec.taints[]? | select(.effect=="NoSchedule" or .effect=="NoExecute")] | length)==1 and ([.spec.taints[]? | select(.key=="hakopod.com/pool" and .value==$pool and .effect=="NoSchedule")] | length)==1' >/dev/null || { echo "adversarial TLS Pod node scheduling identity differs" >&2; return 1; }
  fi
  selector=$(jq -cn --arg value "$phase-$suffix" '{"hakopod.io/tls-adversary":$value}')
  patch=$(jq -cn --arg uid "$service_uid" --argjson selector "$selector" '[{op:"test",path:"/metadata/uid",value:$uid},{op:"replace",path:"/spec/selector",value:$selector}]')
  selector_changed=1
  k patch service db --type=json -p "$patch" >/dev/null
  adversary_ip=$(k get pod "$active_name" -o jsonpath='{.status.podIP}')
  tries=0
  until k get endpointslice -l kubernetes.io/service-name=db -o json | jq -e --arg ip "$adversary_ip" '(.items|length)==1 and [.items[0].endpoints[]?.addresses[]?]==[$ip]' >/dev/null; do
    tries=$((tries+1)); [ "$tries" -lt 30 ] || { echo "db DNS endpoint did not select the UID-fenced adversary" >&2; exit 1; }; sleep 1
  done
  phase_started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  restart_clients
  for component in $clients; do
    client_json=$(k get pod -l "app.kubernetes.io/component=$component" -o json)
    pod=$(printf '%s' "$client_json" | jq -er '.items|if length==1 then .[0].metadata.name else error("expected one pod") end')
    client_uid=$(printf '%s' "$client_json" | jq -er '.items[0].metadata.uid')
    client_ip=$(k get pod "$pod" -o jsonpath='{.status.podIP}')
    tries=0
    until k logs "$active_name" --since-time="$phase_started" --tail=200 --limit-bytes=262144 2>&1 | grep -F "[$client_ip]" >/dev/null; do
      tries=$((tries+1)); [ "$tries" -lt 45 ] || { echo "$component did not reach the selected adversarial database endpoint" >&2; return 1; }; sleep 2
    done
    case "$expected" in
      reference)
        tries=0
        until k logs "$active_name" --since-time="$phase_started" --tail=200 --limit-bytes=262144 2>&1 | grep -F "[$client_ip]" | grep -E 'connection authorized:.*SSL enabled' >/dev/null; do
          tries=$((tries+1)); [ "$tries" -lt 45 ] || { echo "$component lacked positive TLS authentication evidence" >&2; return 1; }; sleep 2
        done
        ;;
      certificate-failure)
        tries=0
        until k logs "$pod" --all-containers --since-time="$phase_started" --tail=200 --limit-bytes=262144 2>&1 | grep -Eiq "$strict_certificate_failure"; do
          tries=$((tries+1)); [ "$tries" -lt 45 ] || { echo "$component lacked certificate-specific rejection evidence for $phase" >&2; return 1; }; sleep 2
        done
        ;;
    esac
    [ "$(k get pod "$pod" -o jsonpath='{.metadata.uid}')" = "$client_uid" ] || { echo "$component changed UID during evidence collection" >&2; return 1; }
  done
  restore_selector
  restart_clients
  wait_client_rollouts
  clients_disturbed=0
  current_pod=$(k get pod "$active_name" -o json)
  [ "$(printf '%s' "$current_pod" | jq -er '.metadata.uid')" = "$active_pod_uid" ] || { echo "adversarial Pod UID changed before cleanup" >&2; return 1; }
  active_pod_rv=$(printf '%s' "$current_pod" | jq -er '.metadata.resourceVersion')
  raw_delete pod "$active_name" "$active_pod_uid" "$active_pod_rv"
  active_pod_uid=
  active_pod_rv=
  current_secret=$(k get secret "$active_name" -o json)
  [ "$(printf '%s' "$current_secret" | jq -er '.metadata.uid')" = "$active_secret_uid" ] || { echo "adversarial Secret UID changed before cleanup" >&2; return 1; }
  active_secret_rv=$(printf '%s' "$current_secret" | jq -er '.metadata.resourceVersion')
  raw_delete secret "$active_name" "$active_secret_uid" "$active_secret_rv"
  active_secret_uid=
  active_secret_rv=
  active_name=
}

run_phase reference good-db.crt good-db.key reference
run_phase wrong-ca wrong-ca.crt wrong-ca.key certificate-failure
run_phase wrong-host wrong-host.crt wrong-host.key certificate-failure
echo "all direct database clients accepted the trusted reference and rejected wrong CA and wrong host certificates"
