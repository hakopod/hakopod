"""Raw Kubernetes patches on generated service objects, their installation switch and the rendered manifests."""
PATCH = {"type": "string", "maxLength": 16384}
schemas["KubernetesPatches"] = obj({
    "deployment": {**PATCH, "description": "Strategic-merge patch, as YAML, applied to the generated Deployment."},
    "service": {**PATCH, "description": "Strategic-merge patch, as YAML, applied to the generated ClusterIP Service. Requires a port."},
    "ingress": {**PATCH, "description": "Strategic-merge patch, as YAML, applied to the generated Ingress. Requires public with a port; only haproxy.org/ annotations may change."},
})
schemas["KubernetesPatches"]["description"] = "Opt-in patches on the objects Hakopod generates for a long-running service. Protected fields are refused. Adding or changing a patch needs the installation setting and kubernetes:patch. See docs/kubernetes-patches.md."
schemas["Service"]["properties"]["kubernetes"] = ref("KubernetesPatches")
schemas["KubernetesPatchSettings"] = obj({"enabled": B, "revision": I}, ["enabled", "revision"])
schemas["KubernetesPatchSettingsInput"] = obj({"enabled": B, "expected_revision": I}, ["enabled", "expected_revision"])
schemas["ApplicationManifests"] = obj({"revision": I, "manifests": {**S, "description": "YAML documents for each long-running service's Deployment, Service and Ingress, patches applied. Secrets are never included."}}, ["revision", "manifests"])
route("/settings/kubernetes-patches", "get", "getKubernetesPatchSettings", ref("KubernetesPatchSettings"))
route("/settings/kubernetes-patches", "put", "putKubernetesPatchSettings", ref("KubernetesPatchSettings"), ref("KubernetesPatchSettingsInput"))
route("/applications/{id}/manifests", "get", "getApplicationManifests", ref("ApplicationManifests"))
paths["/applications/{id}/manifests"]["get"]["parameters"] += [{"name": "service", "in": "query", "schema": S}]
paths["/applications/{id}/manifests"]["get"]["description"] = "Render the generated Kubernetes objects of the current revision without applying anything. Bindings resolved during a deployment are not shown. Requires deployments:read."
for path, method, text in [
    ("/settings/kubernetes-patches", "get", "Get the raw Kubernetes patch setting"),
    ("/settings/kubernetes-patches", "put", "Turn raw Kubernetes patches on or off"),
    ("/applications/{id}/manifests", "get", "Render an application's generated Kubernetes manifests"),
]:
    paths[path][method]["summary"] = text
