#!/usr/bin/env python3
"""Generate the checked-in OpenAPI contract; no runtime dependency."""
import json
import runpy
from pathlib import Path

S = {"type": "string"}
I = {"type": "integer"}
B = {"type": "boolean"}
T = {"type": "string", "format": "date-time"}
def ref(name): return {"$ref": f"#/components/schemas/{name}"}
def array(item): return {"type": "array", "items": item}
def obj(properties, required=()): return {"type": "object", "properties": properties, "required": list(required)}
def mapping(value): return {"type": "object", "additionalProperties": value}
schemas = {}
schemas["Error"] = obj({"error": obj({"code": S, "message": S}, ["code", "message"])}, ["error"])
schemas["Network"] = obj({"internal": B})
schemas["SecretRef"] = obj({"ref": S}, ["ref"])
schemas["Autoscaling"] = obj({"min_replicas": I, "max_replicas": I, "target_cpu": I})
schemas["Service"] = obj({"image": S, "port": I, "public": B, "size": S, "replicas": I, "healthcheck": S, "env": mapping(S), "command": array(S), "args": array(S), "depends_on": array(S), "networks": array(S), "secrets": mapping(ref("SecretRef")), "autoscaling": ref("Autoscaling")}, ["image"])
schemas["Spec"] = obj({"schema_version": I, "name": S, "services": mapping(ref("Service")), "networks": mapping(ref("Network"))}, ["schema_version", "name", "services"])
schemas["Event"] = obj({"id": I, "time": T, "type": S, "message": S, "service": S}, ["id", "time", "type", "message", "service"])
schemas["ServiceStatus"] = obj({"name": S, "status": S, "ready": I, "desired": I, "image": S, "url": S, "internal_address": S, "message": S}, ["name", "status", "ready", "desired"])
schemas["Observation"] = obj({"status": S, "observed_at": T, "services": array(ref("ServiceStatus"))})
schemas["Deployment"] = obj({"id": S, "application_id": S, "identity_id": S, "revision": I, "status": {"type":"string", "enum":["queued", "running", "succeeded", "failed", "cancelled", "superseded"]}, "spec": ref("Spec"), "resolved_spec": {"anyOf": [ref("Spec"), {"type":"null"}]}, "result": ref("Observation"), "error": S, "cancel_requested": B, "created_at": T, "started_at": {"anyOf":[T,{"type":"null"}]}, "finished_at": {"anyOf":[T,{"type":"null"}]}, "events": array(ref("Event"))}, ["id", "application_id", "revision", "status", "spec", "result", "error", "created_at", "events"])
schemas["Application"] = obj({"id": S, "name": S, "project": S, "environment": S, "revision": I, "status": S, "spec": ref("Spec"), "observed": ref("Observation"), "created_at": T, "updated_at": T, "deployments": array(ref("Deployment"))}, ["id", "name", "project", "environment", "revision", "status", "spec", "observed", "created_at", "updated_at"])
schemas["Project"] = obj({"id": S, "name": S, "environments": array(obj({"name":S},["name"]))}, ["id", "name", "environments"])
schemas["Principal"] = obj({"id": S, "name": S, "admin": B, "permissions": array(S), "project": S, "environment": S, "application": S}, ["id", "name", "admin", "permissions", "project", "environment"])
schemas["Node"] = obj({"name": S, "ready": B, "unschedulable": B, "architecture": S, "kubelet_version": S, "allocatable_cpu": S, "allocatable_memory": S, "pods": I}, ["name", "ready", "unschedulable", "architecture", "kubelet_version", "allocatable_cpu", "allocatable_memory", "pods"])
schemas["KeyInput"] = obj({"name": S, "project": S, "environment": S, "application": S, "permissions": array(S), "expires_at": T}, ["name", "permissions", "expires_at"])
schemas["Key"] = obj({**schemas["KeyInput"]["properties"], "id": S, "identity_id": S, "prefix": S, "created_at": T, "revoked_at": {"anyOf":[T,{"type":"null"}]}, "last_used_at": {"anyOf":[T,{"type":"null"}]}}, ["id", "identity_id", "name", "prefix", "permissions", "project", "environment", "expires_at", "created_at"])
schemas["KeyCreated"] = obj({"key": S, "metadata": ref("Key"), "previous_key_expires_within_seconds": I}, ["key", "metadata"])
schemas["DeployInput"] = obj({"project": S, "environment": S, "spec": ref("Spec"), "toml": S, "service": S, "services": {"type": "array", "items": S, "minItems": 1, "maxItems": 20, "uniqueItems": True, "description": "Selected services to deploy together. Mutually exclusive with service. Omit both selectors for an application-wide deployment."}, "expected_revision": I}, ["project", "environment", "expected_revision"])
schemas["PlanInput"] = obj(schemas["DeployInput"]["properties"], ["project", "environment"])
schemas["Change"] = obj({"service": S, "field": S, "before": {}, "after": {}, "sensitive": B}, ["service", "field", "before", "after", "sensitive"])
schemas["Plan"] = obj({"application_id": S, "expected_revision": I, "spec": ref("Spec"), "changes": array(ref("Change")), "warnings": array(S), "resource_profiles": mapping(obj({"CPURequest":S,"CPULimit":S,"MemoryRequest":S,"MemoryLimit":S}))}, ["application_id", "expected_revision", "spec", "changes", "warnings"])
schemas["Audit"] = obj({"id":I,"identity_id":S,"key_id":S,"action":S,"resource":S,"time":T,"metadata":{}},["id","identity_id","action","resource","time"])
schemas["DeploymentSummary"] = obj({k:v for k,v in schemas["Deployment"]["properties"].items() if k not in {"spec","resolved_spec","events","identity_id","cancel_requested"}}, ["id","application_id","revision","status","result","error","created_at"])
schemas["Application"]["properties"]["deployments"] = array(ref("DeploymentSummary"))
paths = {}
def route(path, method, operation, response, request=None, status="200", scope=False, idem=False):
    parameters=[]
    if "{id}" in path: parameters.append({"name":"id","in":"path","required":True,"schema":S})
    if scope: parameters += [{"name":v,"in":"query","required":True,"schema":S} for v in ["project","environment"]]
    if idem: parameters.append({"name":"Idempotency-Key","in":"header","required":True,"schema":{"type":"string","minLength":8,"maxLength":128}})
    item={"operationId":operation, "parameters":parameters, "responses":{status:{"description":"Success","content":{"application/json":{"schema":response}}}, "default":{"description":"Error","content":{"application/json":{"schema":ref("Error")}}}}}
    if request: item["requestBody"]={"required":True,"content":{"application/json":{"schema":request}}}
    paths.setdefault(path,{})[method]=item
def items(name): return obj({"items":array(ref(name))},["items"])
route("/me","get","getIdentity",ref("Principal"))
route("/projects","get","listProjects",items("Project"))
route("/projects","post","createProject",obj({"name":S,"environment":S}),obj({"name":S,"environment":S},["name","environment"]),"201")
route("/applications","get","listApplications",items("Application"),scope=True)
paths["/applications"]["get"]["parameters"].append({"name":"cursor","in":"query","schema":S})
paths["/applications"]["get"]["responses"]["200"]["content"]["application/json"]["schema"]["properties"]["next_cursor"] = S
route("/applications/{id}","get","getApplication",ref("Application"))
route("/plan","post","planDeployment",ref("Plan"),ref("PlanInput"))
route("/deployments","post","createDeployment",ref("Deployment"),ref("DeployInput"),"202",idem=True)
route("/deployments/{id}","get","getDeployment",ref("Deployment"))
route("/idempotency/{key}","get","getIdempotentDeployment",ref("Deployment"))
paths["/idempotency/{key}"]["get"]["parameters"].append({"name":"key","in":"path","required":True,"schema":S})
route("/applications/{id}/rollback","post","rollbackApplication",ref("Deployment"),obj({"revision":I,"expected_revision":I},["revision","expected_revision"]),"202",idem=True)
route("/deployments/{id}/cancel","post","cancelDeployment",obj({"id":S,"status":S}),obj({}),"202")
route("/nodes","get","listNodes",obj({"items":array(ref("Node")),"observed_at":T},["items","observed_at"]))
route("/keys","get","listKeys",items("Key"))
route("/keys","post","createKey",ref("KeyCreated"),ref("KeyInput"),"201")
route("/keys/{id}","delete","revokeKey",obj({"status":S}))
route("/keys/{id}/rotate","post","rotateKey",ref("KeyCreated"),obj({"expires_at":T},["expires_at"]),"201")
route("/audit","get","listAudit",items("Audit"))
for endpoint, operation, content in [("/applications/{id}/logs","streamLogs","text/plain"),("/deployments/{id}/events","streamEvents","text/event-stream")]:
    route(endpoint,"get",operation,S)
    paths[endpoint]["get"]["responses"]["200"]["content"]={content:{"schema":S}}
paths["/applications/{id}/logs"]["get"]["parameters"] += [{"name":"service","in":"query","required":True,"schema":S},{"name":"tail","in":"query","schema":{"type":"integer","minimum":1,"maximum":1000,"default":100}},{"name":"follow","in":"query","schema":B}]
# Feature contracts execute with the same small schema helpers. Each feature owns its file.
for extension in sorted(Path(__file__).with_name("contracts").glob("*.py")):
    runpy.run_path(str(extension), init_globals={"S": S, "I": I, "B": B, "T": T, "ref": ref, "array": array, "obj": obj, "mapping": mapping, "schemas": schemas, "paths": paths, "route": route, "items": items})
doc={"openapi":"3.1.0", "info":{"title":"Hakopod Management API","version":"0.1.0","description":"Durable container deployments. JSON or strict TOML specifications. An accepted 202 survives caller disconnection. Machine keys are scoped to project/environment; only admin keys may manage infrastructure or credentials."}, "servers":[{"url":"/api/v1"}], "security":[{"bearerAuth":[]}], "paths":paths,"components":{"securitySchemes":{"bearerAuth":{"type":"http","scheme":"bearer"}},"schemas":schemas}}
Path(__file__).with_name("openapi.json").write_text(json.dumps(doc,indent=2)+"\n")
