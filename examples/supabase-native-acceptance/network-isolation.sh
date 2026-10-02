#!/bin/sh
set -eu
umask 077

: "${KUBECONFIG:?set the dedicated development kubeconfig}"
: "${HAKOPOD_ACCEPTANCE_PLATFORM_ID:?set the disposable platform ID}"
: "${HAKOPOD_ACCEPTANCE_NAMESPACE_UID:?set the observed namespace UID}"
: "${HAKOPOD_ACCEPTANCE_OWNER_OPERATION_ID:?set the owner operation ID}"
: "${HAKOPOD_ACCEPTANCE_RESOURCE_INTENT_ID:?set the resource intent ID}"

context=k3d-hakopod-dev
namespace=managed-platform-$HAKOPOD_ACCEPTANCE_PLATFORM_ID
k() { kubectl --request-timeout=10s --kubeconfig "$KUBECONFIG" --context "$context" -n "$namespace" "$@"; }
[ "$(kubectl --request-timeout=10s --kubeconfig "$KUBECONFIG" config current-context)" = "$context" ]
kubectl --request-timeout=10s --kubeconfig "$KUBECONFIG" --context "$context" get namespace "$namespace" -o json |
  jq -e --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --arg uid "$HAKOPOD_ACCEPTANCE_NAMESPACE_UID" --arg owner "$HAKOPOD_ACCEPTANCE_OWNER_OPERATION_ID" --arg intent "$HAKOPOD_ACCEPTANCE_RESOURCE_INTENT_ID" '.metadata.uid==$uid and .metadata.deletionTimestamp==null and .metadata.labels["app.kubernetes.io/managed-by"]=="hakopod" and .metadata.labels["hakopod.io/managed-platform-id"]==$id and .metadata.labels["hakopod.io/owner-operation-id"]==$owner and .metadata.labels["hakopod.io/resource-intent-id"]==$intent' >/dev/null

studio_json=$(k get pod -l app.kubernetes.io/component=studio -o json)
studio=$(printf '%s' "$studio_json" | jq -er --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" '.items|select(length==1)|.[0]|select(.metadata.deletionTimestamp==null and .metadata.labels["app.kubernetes.io/managed-by"]=="hakopod" and .metadata.labels["hakopod.io/managed-platform-id"]==$id and .status.phase=="Running" and (.metadata.ownerReferences|length)==1 and .metadata.ownerReferences[0].controller==true and .metadata.ownerReferences[0].kind=="ReplicaSet")|.metadata.name')
replica_name=$(printf '%s' "$studio_json" | jq -er '.items[0].metadata.ownerReferences[0].name')
replica_uid=$(printf '%s' "$studio_json" | jq -er '.items[0].metadata.ownerReferences[0].uid')
replica_json=$(k get replicaset "$replica_name" -o json)
deployment_name=$(printf '%s' "$replica_json" | jq -er --arg uid "$replica_uid" --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" '.metadata|select(.uid==$uid and .deletionTimestamp==null and .labels["app.kubernetes.io/managed-by"]=="hakopod" and .labels["hakopod.io/managed-platform-id"]==$id and (.ownerReferences|length)==1 and .ownerReferences[0].controller==true and .ownerReferences[0].kind=="Deployment")|.ownerReferences[0].name')
deployment_uid=$(printf '%s' "$replica_json" | jq -er '.metadata.ownerReferences[0].uid')
k get deployment "$deployment_name" -o json | jq -e --arg id "$HAKOPOD_ACCEPTANCE_PLATFORM_ID" --arg uid "$deployment_uid" --arg nsuid "$HAKOPOD_ACCEPTANCE_NAMESPACE_UID" '.metadata.uid==$uid and .metadata.deletionTimestamp==null and .metadata.labels["app.kubernetes.io/managed-by"]=="hakopod" and .metadata.labels["hakopod.io/managed-platform-id"]==$id and (.metadata.ownerReferences|length)==1 and .metadata.ownerReferences[0].kind=="Namespace" and .metadata.ownerReferences[0].uid==$nsuid' >/dev/null

# Test an allowed dependency first. Negative probes use the same live process
# and DNS path, so a broken probe or failed DNS cannot count as isolation.
timeout 25s kubectl --request-timeout=10s --kubeconfig "$KUBECONFIG" --context "$context" -n "$namespace" exec "$studio" -- node -e '
  const net = require("node:net");
  async function probe(host, port, allowed) {
    const result = await new Promise((resolve, reject) => {
      const socket = net.createConnection({host, port});
      const timer = setTimeout(() => { socket.destroy(); resolve("timeout"); }, 2000);
      socket.once("connect", () => { clearTimeout(timer); socket.destroy(); resolve("connected"); });
      socket.once("error", error => {
        clearTimeout(timer); socket.destroy();
        if (["EAI_AGAIN", "ENOTFOUND"].includes(error.code)) reject(new Error("dns"));
        else resolve(error.code === "ECONNREFUSED" ? "refused" : "unreachable");
      });
    });
    if (allowed ? result !== "connected" : result !== "timeout") throw new Error("policy");
  }
  (async () => {
    await probe("meta", 8080, true);
    for (const [host, port] of [["db", 5432], ["supavisor", 4000], ["functions", 9000], ["rest", 3000], ["imgproxy", 5001]]) {
      await probe(host, port, false);
    }
  })().then(() => process.exit(0)).catch(() => { console.error("network isolation check failed"); process.exit(1); });
'

printf '{"schema_version":1,"status":"passed","case":"network-policy-isolation","allowed_dependency":"studio-to-meta:8080","denied_dependencies":["studio-to-db:5432","studio-to-pooler-admin:4000","studio-to-edge:9000","studio-to-rest:3000","studio-to-image-proxy:5001"]}\n'
