#!/bin/sh
set -eu

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
: "${HAKOPOD_ACCEPTANCE_DISPOSABLE:?set HAKOPOD_ACCEPTANCE_DISPOSABLE=1 for an isolated disposable fixture}"
[ "$HAKOPOD_ACCEPTANCE_DISPOSABLE" = 1 ] || { echo "acceptance requires an explicit disposable fixture marker" >&2; exit 2; }
for command in kubectl curl jq websocat sha256sum openssl; do command -v "$command" >/dev/null 2>&1 || { echo "$command is required" >&2; exit 2; }; done
context=$(kubectl --kubeconfig "$KUBECONFIG" config current-context)
[ "$context" = "$expected_context" ] || { echo "refusing Kubernetes context: $context" >&2; exit 2; }
jq -e '.schema_version==1 and .kind=="supabase"' "$HAKOPOD_ACCEPTANCE_SPEC" >/dev/null
gateway_host=$(jq -er '.supabase.public_url | sub("^https://";"") | sub("/$";"")' "$HAKOPOD_ACCEPTANCE_SPEC")
[ -n "$gateway_host" ] && ! printf '%s' "$gateway_host" | grep -q '[:/]' || { echo "public_url must be an exact HTTPS origin on port 443" >&2; exit 2; }
[ -s "$HAKOPOD_ACCEPTANCE_GATEWAY_CA" ] || { echo "gateway CA is unavailable" >&2; exit 2; }
jq -e --slurpfile pins "$HAKOPOD_ACCEPTANCE_IMAGES" 'type=="object" and length==11 and all(to_entries[]; (.value.uid|type)=="number" and .value.uid>0 and (.value.gid|type)=="number" and .value.gid>0 and .value.image==$pins[0][.key])' "$HAKOPOD_ACCEPTANCE_IDENTITIES" >/dev/null || { echo "all eleven digest-qualified non-root identities are required" >&2; exit 3; }
create_review=$(jq -n --slurpfile spec "$HAKOPOD_ACCEPTANCE_SPEC" --arg project "$HAKOPOD_ACCEPTANCE_PROJECT" --arg environment "$HAKOPOD_ACCEPTANCE_ENVIRONMENT" '{project:$project,environment:$environment,expected_revision:0,kind:"create",spec:$spec[0]}' | curl --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/reviews" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_API_TOKEN" -H 'content-type: application/json' --data-binary @-)
printf '%s' "$create_review" | jq -e '.blocked==false and .review.id!=null' >/dev/null || { echo "Supabase capability gate remains closed; acceptance did not run" >&2; exit 3; }
HAKOPOD_ACCEPTANCE_PLATFORM_ID=$(printf '%s' "$create_review" | jq -er '.platform.id')
create_operation=$(printf '%s' "$create_review" | jq '{id:.platform.id,project:.platform.project,environment:.platform.environment,expected_revision:0,kind:"create",spec:.platform.spec,review:.review}' | curl --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/operations" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_API_TOKEN" -H "Idempotency-Key: hakopod-test-create-$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -H 'content-type: application/json' --data-binary @-)
create_operation_id=$(printf '%s' "$create_operation" | jq -er '.id')
tries=0
while :; do create_status=$(curl --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-operations/$create_operation_id" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_API_TOKEN" | jq -er '.status'); [ "$create_status" = succeeded ] && break; [ "$create_status" != failed ] && [ "$create_status" != cancelled ] || { echo "Supabase creation failed" >&2; exit 1; }; tries=$((tries+1)); [ "$tries" -lt 180 ] || { echo "Supabase creation timed out" >&2; exit 1; }; sleep 2; done
jq -e 'type=="object" and length==11 and all(.[];test("^[^@]+@sha256:[0-9a-f]{64}$"))' "$HAKOPOD_ACCEPTANCE_IMAGES" >/dev/null
namespace=managed-platform-$HAKOPOD_ACCEPTANCE_PLATFORM_ID
platform=$(curl --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_API_TOKEN")
namespace_uid=$(kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get namespace "$namespace" -o jsonpath='{.metadata.uid}')
[ -n "$namespace_uid" ]
tls_ref=$(jq -er '.secrets["gateway-tls-certificate"] | .name + "-r" + (.revision|tostring)' "$HAKOPOD_ACCEPTANCE_SPEC")
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
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get deployment,statefulset -o json >"/tmp/hakopod-supabase-workloads-$HAKOPOD_ACCEPTANCE_PLATFORM_ID.json"
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
      .securityContext.seccompProfile.type == "RuntimeDefault"))' "/tmp/hakopod-supabase-workloads-$HAKOPOD_ACCEPTANCE_PLATFORM_ID.json" >/dev/null
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get service api-gw -o json | jq -e '.spec.type=="ClusterIP" and (.spec.ports|length)==1 and .spec.ports[0].port==8443 and .spec.ports[0].targetPort==8443' >/dev/null
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get networkpolicy supabase-envoy-ingress -o json | jq -e '[.spec.ingress[].ports[].port] == [8443]' >/dev/null

pvc_before=/tmp/hakopod-supabase-pvc-before-$HAKOPOD_ACCEPTANCE_PLATFORM_ID.json
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get pvc -o json | jq -S '[.items[] | {name:.metadata.name,uid:.metadata.uid,volume:.spec.volumeName,storage_class:.spec.storageClassName,access_modes:.spec.accessModes,request:.spec.resources.requests.storage}] | sort_by(.name)' >"$pvc_before"
[ "$(jq length "$pvc_before")" -eq 5 ] && jq -e 'all(.[]; .uid!="" and .volume!="" and .storage_class!="" and (.access_modes|length)>0 and .request!="")' "$pvc_before" >/dev/null
pv_before=/tmp/hakopod-supabase-pv-before-$HAKOPOD_ACCEPTANCE_PLATFORM_ID.json
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

port=${HAKOPOD_ACCEPTANCE_LOCAL_PORT:-18443}
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" port-forward service/api-gw "$port:8443" >/tmp/hakopod-supabase-port-forward.log 2>&1 &
forward_pid=$!
cleanup(){ kill "$forward_pid" 2>/dev/null || true; }
trap cleanup EXIT INT TERM
base=https://$gateway_host:$port
curl_tls="--noproxy $gateway_host --resolve $gateway_host:$port:127.0.0.1 --cacert $HAKOPOD_ACCEPTANCE_GATEWAY_CA"
tries=0
until openssl s_client -connect "127.0.0.1:$port" -servername "$gateway_host" -CAfile "$HAKOPOD_ACCEPTANCE_GATEWAY_CA" -verify_return_error </dev/null 2>/dev/null | grep -F 'Verify return code: 0 (ok)' >/dev/null; do tries=$((tries+1)); [ "$tries" -lt 30 ] || { echo "Supabase gateway TLS identity did not become verifiable" >&2; exit 1; }; sleep 1; done
if curl --silent --show-error --max-time 3 "http://127.0.0.1:$port/auth/v1/health" >/dev/null 2>&1; then echo "Supabase gateway served plaintext on its TLS listener" >&2; exit 1; fi
tries=0
until curl $curl_tls --fail --silent --max-time 2 "$base/auth/v1/health" >/dev/null; do tries=$((tries+1)); [ "$tries" -lt 30 ] || { echo "Supabase gateway did not become reachable" >&2; exit 1; }; sleep 1; done

signup=$(curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/auth/v1/signup" -H "apikey: $HAKOPOD_ACCEPTANCE_ANON_KEY" -H 'content-type: application/json' --data '{"data":{"acceptance":"hakopod-test-native"}}')
access_token=$(printf '%s' "$signup" | jq -er '.access_token')
[ -n "$access_token" ]

row_id=hakopod-test-$(date +%s)
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec statefulset/supabase-database -- psql -U postgres -v ON_ERROR_STOP=1 -c 'create table if not exists public.hakopod_acceptance (id text primary key, owner uuid not null default auth.uid(), value text not null); alter table public.hakopod_acceptance enable row level security; drop policy if exists hakopod_acceptance_owner on public.hakopod_acceptance; create policy hakopod_acceptance_owner on public.hakopod_acceptance using (owner = auth.uid()) with check (owner = auth.uid()); grant select,insert,update,delete on public.hakopod_acceptance to authenticated, service_role;' >/dev/null
curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/rest/v1/hakopod_acceptance" -H "apikey: $HAKOPOD_ACCEPTANCE_ANON_KEY" -H "authorization: Bearer $access_token" -H 'content-type: application/json' -H 'prefer: return=minimal' --data "{\"id\":\"$row_id\",\"value\":\"before-restart\"}"
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/rest/v1/hakopod_acceptance?id=eq.$row_id&select=id,value" -H "apikey: $HAKOPOD_ACCEPTANCE_SERVICE_KEY" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_SERVICE_KEY" | jq -e --arg id "$row_id" 'length==1 and .[0].id==$id and .[0].value=="before-restart"' >/dev/null
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/rest/v1/hakopod_acceptance?id=eq.$row_id&select=id" -H "apikey: $HAKOPOD_ACCEPTANCE_ANON_KEY" | jq -e 'length==0' >/dev/null

printf '{"topic":"realtime:public:hakopod_acceptance","event":"phx_join","payload":{"config":{"broadcast":{"ack":false,"self":false},"presence":{"enabled":false},"postgres_changes":[]},"access_token":"%s"},"ref":"1"}\n' "$access_token" | SSL_CERT_FILE="$HAKOPOD_ACCEPTANCE_GATEWAY_CA" timeout 10 websocat -1 -t -H="apikey: $HAKOPOD_ACCEPTANCE_ANON_KEY" --tls-domain="$gateway_host" "wss://127.0.0.1:$port/realtime/v1/websocket?apikey=$HAKOPOD_ACCEPTANCE_ANON_KEY&vsn=1.0.0" | jq -e 'select(.event=="phx_reply" and .ref=="1")' >/dev/null

realtime_output=/tmp/hakopod-supabase-realtime-$HAKOPOD_ACCEPTANCE_PLATFORM_ID.jsonl
(sleep 2; curl $curl_tls --fail --silent --show-error --max-time 10 -X PATCH "$base/rest/v1/hakopod_acceptance?id=eq.$row_id" -H "apikey: $HAKOPOD_ACCEPTANCE_ANON_KEY" -H "authorization: Bearer $access_token" -H 'content-type: application/json' -H 'prefer: return=minimal' --data '{"value":"realtime-observed"}') &
realtime_trigger=$!
{ printf '{"topic":"realtime:public:hakopod_acceptance","event":"phx_join","payload":{"config":{"broadcast":{"ack":false,"self":false},"presence":{"enabled":false},"postgres_changes":[{"event":"UPDATE","schema":"public","table":"hakopod_acceptance"}]},"access_token":"%s"},"ref":"2"}\n' "$access_token"; sleep 8; } | SSL_CERT_FILE="$HAKOPOD_ACCEPTANCE_GATEWAY_CA" timeout 12 websocat -t -H="apikey: $HAKOPOD_ACCEPTANCE_ANON_KEY" --tls-domain="$gateway_host" "wss://127.0.0.1:$port/realtime/v1/websocket?apikey=$HAKOPOD_ACCEPTANCE_ANON_KEY&vsn=1.0.0" >"$realtime_output" || true
wait "$realtime_trigger"
jq -e --arg id "$row_id" 'select(.event=="postgres_changes" and .payload.data.record.id==$id and .payload.data.record.value=="realtime-observed")' "$realtime_output" >/dev/null
rm -f "$realtime_output"

bucket=hakopod-test-native
object=roundtrip.txt
curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/storage/v1/bucket" -H "apikey: $HAKOPOD_ACCEPTANCE_SERVICE_KEY" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_SERVICE_KEY" -H 'content-type: application/json' --data "{\"id\":\"$bucket\",\"name\":\"$bucket\",\"public\":false}" >/dev/null
printf 'hakopod-native-storage' | curl $curl_tls --fail --silent --show-error --max-time 10 -X POST "$base/storage/v1/object/$bucket/$object" -H "apikey: $HAKOPOD_ACCEPTANCE_SERVICE_KEY" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_SERVICE_KEY" -H 'content-type: text/plain' --data-binary @- >/dev/null
[ "$(curl $curl_tls --fail --silent --show-error --max-time 10 "$base/storage/v1/object/authenticated/$bucket/$object" -H "apikey: $HAKOPOD_ACCEPTANCE_SERVICE_KEY" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_SERVICE_KEY")" = hakopod-native-storage ]
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/functions/v1/hello" -H "authorization: Bearer $access_token" -H "apikey: $HAKOPOD_ACCEPTANCE_ANON_KEY" >/dev/null
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/" >/dev/null

kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" delete pod -l "hakopod.io/managed-platform-id=$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --wait=false >/dev/null
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" rollout status statefulset/supabase-database --timeout=5m >/dev/null
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" rollout status deployment --timeout=5m >/dev/null
kill "$forward_pid" 2>/dev/null || true
wait "$forward_pid" 2>/dev/null || true
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" port-forward service/api-gw "$port:8443" >/tmp/hakopod-supabase-port-forward.log 2>&1 &
forward_pid=$!
tries=0
until curl $curl_tls --fail --silent --max-time 2 "$base/auth/v1/health" >/dev/null; do tries=$((tries+1)); [ "$tries" -lt 30 ] || { echo "Supabase gateway did not return after restart" >&2; exit 1; }; sleep 1; done
pvc_after=/tmp/hakopod-supabase-pvc-after-$HAKOPOD_ACCEPTANCE_PLATFORM_ID.json
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get pvc -o json | jq -S '[.items[] | {name:.metadata.name,uid:.metadata.uid,volume:.spec.volumeName,storage_class:.spec.storageClassName,access_modes:.spec.accessModes,request:.spec.resources.requests.storage}] | sort_by(.name)' >"$pvc_after"
cmp -s "$pvc_before" "$pvc_after" || { echo "Supabase PVC or PV identity changed across pod replacement" >&2; exit 1; }
pv_after=/tmp/hakopod-supabase-pv-after-$HAKOPOD_ACCEPTANCE_PLATFORM_ID.json
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get pv $(jq -r '.[].volume' "$pvc_after") -o json | jq -S '[.items[] | {name:.metadata.name,uid:.metadata.uid,claim_namespace:.spec.claimRef.namespace,claim_name:.spec.claimRef.name,claim_uid:.spec.claimRef.uid}] | sort_by(.name)' >"$pv_after"
cmp -s "$pv_before" "$pv_after" || { echo "Supabase PV resource identity changed across pod replacement" >&2; exit 1; }
[ "$(curl $curl_tls --fail --silent --show-error --max-time 10 "$base/storage/v1/object/authenticated/$bucket/$object" -H "apikey: $HAKOPOD_ACCEPTANCE_SERVICE_KEY" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_SERVICE_KEY")" = hakopod-native-storage ]
curl $curl_tls --fail --silent --show-error --max-time 10 "$base/rest/v1/hakopod_acceptance?id=eq.$row_id&select=id,value" -H "apikey: $HAKOPOD_ACCEPTANCE_SERVICE_KEY" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_SERVICE_KEY" | jq -e --arg id "$row_id" 'length==1 and .[0].id==$id' >/dev/null

platform=$(curl --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_API_TOKEN")
revision=$(printf '%s' "$platform" | jq -er '.revision')
update_review=$(printf '%s' "$platform" | jq --argjson revision "$revision" '{id:.id,project:.project,environment:.environment,expected_revision:$revision,kind:"update",confirm_name:.spec.name,spec:.spec}' | curl --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/reviews" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_API_TOKEN" -H 'content-type: application/json' --data-binary @-)
printf '%s' "$update_review" | jq -e '.blocked==false and .review.id!=null' >/dev/null
update_operation=$(printf '%s' "$update_review" | jq '{id:.platform.id,project:.platform.project,environment:.platform.environment,expected_revision:.review.expected_revision,kind:"update",confirm_name:.platform.spec.name,spec:.platform.spec,review:.review}' | curl --fail --silent --show-error --max-time 15 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/operations" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_API_TOKEN" -H "Idempotency-Key: hakopod-test-update-$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -H 'content-type: application/json' --data-binary @-)
update_operation_id=$(printf '%s' "$update_operation" | jq -er '.id')
tries=0
while :; do update_status=$(curl --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-operations/$update_operation_id" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_API_TOKEN" | jq -er '.status'); [ "$update_status" = succeeded ] && break; [ "$update_status" != failed ] && [ "$update_status" != cancelled ] || { echo "Supabase update failed" >&2; exit 1; }; tries=$((tries+1)); [ "$tries" -lt 180 ] || { echo "Supabase update timed out" >&2; exit 1; }; sleep 2; done
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" get pvc -o json | jq -S '[.items[] | {name:.metadata.name,uid:.metadata.uid,volume:.spec.volumeName,storage_class:.spec.storageClassName,access_modes:.spec.accessModes,request:.spec.resources.requests.storage}] | sort_by(.name)' >"$pvc_after"
cmp -s "$pvc_before" "$pvc_after" || { echo "Supabase PVC or PV identity changed across no-change update" >&2; exit 1; }
kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get pv $(jq -r '.[].volume' "$pvc_after") -o json | jq -S '[.items[] | {name:.metadata.name,uid:.metadata.uid,claim_namespace:.spec.claimRef.namespace,claim_name:.spec.claimRef.name,claim_uid:.spec.claimRef.uid}] | sort_by(.name)' >"$pv_after"
cmp -s "$pv_before" "$pv_after" || { echo "Supabase PV resource identity changed across no-change update" >&2; exit 1; }
[ "$(curl $curl_tls --fail --silent --show-error --max-time 10 "$base/storage/v1/object/authenticated/$bucket/$object" -H "apikey: $HAKOPOD_ACCEPTANCE_SERVICE_KEY" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_SERVICE_KEY")" = hakopod-native-storage ]

curl $curl_tls --fail --silent --show-error --max-time 10 -X DELETE "$base/storage/v1/object/$bucket/$object" -H "apikey: $HAKOPOD_ACCEPTANCE_SERVICE_KEY" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_SERVICE_KEY" >/dev/null
curl $curl_tls --fail --silent --show-error --max-time 10 -X DELETE "$base/rest/v1/hakopod_acceptance?id=eq.$row_id" -H "apikey: $HAKOPOD_ACCEPTANCE_SERVICE_KEY" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_SERVICE_KEY" >/dev/null

kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" -n "$namespace" exec statefulset/supabase-database -- psql -U postgres -v ON_ERROR_STOP=1 -c 'drop table public.hakopod_acceptance;' >/dev/null

platform=$(curl --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_API_TOKEN")
revision=$(printf '%s' "$platform" | jq -er '.revision')
name=$(printf '%s' "$platform" | jq -er '.spec.name')
review=$(curl --fail --silent --show-error --max-time 10 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/reviews" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_API_TOKEN" -H 'content-type: application/json' --data "$(printf '%s' "$platform" | jq --argjson revision "$revision" '{id:.id,project:.project,environment:.environment,expected_revision:$revision,kind:"delete",confirm_name:.spec.name,spec:.spec}')")
printf '%s' "$review" | jq -e '.blocked==false and .review.id!=null' >/dev/null
operation=$(curl --fail --silent --show-error --max-time 10 -X POST "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platforms/operations" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_API_TOKEN" -H "Idempotency-Key: hakopod-test-delete-$HAKOPOD_ACCEPTANCE_PLATFORM_ID" -H 'content-type: application/json' --data "$(printf '%s' "$review" | jq '{id:.platform.id,project:.platform.project,environment:.platform.environment,expected_revision:(.review.expected_revision),kind:"delete",confirm_name:.platform.spec.name,spec:.platform.spec,review:.review}')")
operation_id=$(printf '%s' "$operation" | jq -er '.id')
tries=0
while kubectl --kubeconfig "$KUBECONFIG" --context "$expected_context" get namespace "$namespace" >/dev/null 2>&1; do tries=$((tries+1)); [ "$tries" -lt 180 ] || { echo "namespace deletion timed out" >&2; exit 1; }; sleep 2; done
status=$(curl --fail --silent --show-error --max-time 10 "$HAKOPOD_ACCEPTANCE_API_URL/api/v1/managed-platform-operations/$operation_id" -H "authorization: Bearer $HAKOPOD_ACCEPTANCE_API_TOKEN" | jq -er '.status')
[ "$status" = succeeded ]
printf '{"context":"%s","platform_id":"%s","namespace_uid":"%s","operation_id":"%s","status":"passed"}\n' "$expected_context" "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" "$namespace_uid" "$operation_id"
