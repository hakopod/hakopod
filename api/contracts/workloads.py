schemas["Volume"] = obj({"mount_path": S, "size_gib": I, "storage_class": S}, ["mount_path", "size_gib"])
schemas["GPU"] = obj({"count": I}, ["count"])
schemas["Service"]["properties"].update({"volume": ref("Volume"), "gpu": ref("GPU"), "run_as_user": I, "architecture": {"type":"string","enum":["amd64","arm64"]}})
schemas["NamedVolume"] = obj({"size_gib": {"type":"integer","minimum":1,"maximum":200}, "storage_class": S, "access_mode": {"type":"string","enum":["ReadWriteOnce","ReadWriteMany"]}}, ["size_gib"])
schemas["Mount"] = obj({"volume": S, "mount_path": S, "sub_path": S, "read_only": B}, ["volume","mount_path"])
schemas["TemporaryMount"] = obj({"mount_path": S, "size_mib": {"type":"integer","minimum":1,"maximum":128}, "memory": B}, ["mount_path","size_mib"])
schemas["PrivatePort"] = obj({"name": S, "port": I, "target_port": I, "protocol": {"type":"string","enum":["TCP","UDP"]}}, ["name","port"])
schemas["NetworkAccess"] = obj({"from": array(S), "from_applications": array(S)}, ["from"])
schemas["Spec"]["properties"]["volumes"] = mapping(ref("NamedVolume"))
schemas["Service"]["properties"].update({"mounts": array(ref("Mount")), "temporary_mounts": array(ref("TemporaryMount")), "ports": array(ref("PrivatePort")), "network_access": ref("NetworkAccess"), "run_as_group": I, "fs_group": I, "read_only_root_filesystem": B, "working_dir": S, "termination_grace_seconds": I})
schemas["WorkloadSecret"] = obj({"name": S, "updated_at": T}, ["name", "updated_at"])
route("/secrets", "get", "listWorkloadSecrets", items("WorkloadSecret"), scope=True)
route("/secrets/{name}", "put", "putWorkloadSecret", obj({"name": S, "saved": B, "restart_required": B}), obj({"value": S}, ["value"]), scope=True)
route("/secrets/{name}", "delete", "deleteWorkloadSecret", obj({"deleted": B}), scope=True)
for endpoint in ["/secrets", "/secrets/{name}"]:
    for method in paths[endpoint].values():
        method["parameters"].append({"name": "application", "in": "query", "required": True, "schema": S})
        if "{name}" in endpoint:
            method["parameters"].append({"name": "name", "in": "path", "required": True, "schema": S})

schemas["Spec"]["properties"].update({
    "env": {**mapping(S), "description": "Application-wide plain environment defaults. Service values override these defaults."},
    "secrets": mapping(ref("SecretRef")),
    "inject_env": {**B, "description": "Inject all application env defaults into services. Service values override these defaults."},
})

schemas["Service"]["properties"]["private_egress"] = {"type": "array", "maxItems": 16, "uniqueItems": True, "items": {"type": "string", "pattern": "^[a-z][a-z0-9-]{0,38}[a-z0-9]$|^[a-z]$"}, "description": "Self-hosted: named private destinations approved by the installation administrator for this service."}

schemas["Service"]["properties"]["node_name"] = {**S,"maxLength":253,"description":"Exact Kubernetes node name. Uses scheduler affinity; never bypasses taints, runtime policy or resource checks."}

schemas["PlacementNode"] = obj({"name":S,"architecture":S,"available":B,"reason":S}, ["name","architecture","available","reason"])
route("/placement/nodes", "get", "listPlacementNodes", obj({"items":array(ref("PlacementNode")),"serverless_available":B},["items","serverless_available"]), scope=True)
paths["/placement/nodes"]["get"]["parameters"].append({"name":"application","in":"query","schema":S})
schemas["Serverless"] = obj({"min_replicas":{"type":"integer","minimum":0,"maximum":1,"default":0},"idle_seconds":{"type":"integer","minimum":30,"maximum":86400,"default":300},"startup_timeout_seconds":{"type":"integer","minimum":5,"maximum":300,"default":60},"request_timeout_seconds":{"type":"integer","minimum":1,"maximum":300,"default":60},"max_concurrency":{"type":"integer","minimum":1,"maximum":64,"default":16}})
schemas["Service"]["properties"]["serverless"] = ref("Serverless")

route("/secrets/requirements", "post", "checkSecretRequirements", obj({"required_secrets": array(S), "missing_secrets": array(S)}, ["required_secrets", "missing_secrets"]), obj({"project": S, "environment": S, "spec": ref("Spec")}, ["project", "environment", "spec"]))
route("/secrets/{name}", "post", "createWorkloadSecret", obj({"name": S, "saved": B}, ["name", "saved"]), obj({"value": S, "generate": B, "format": {"type": "string", "enum": ["base64url", "hex"]}}), "201", scope=True)
paths["/secrets/{name}"]["post"]["parameters"] += [{"name": "name", "in": "path", "required": True, "schema": S}, {"name": "application", "in": "query", "required": True, "schema": S}]
