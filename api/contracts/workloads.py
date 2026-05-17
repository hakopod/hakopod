schemas["Volume"] = obj({"mount_path": S, "size_gib": I, "storage_class": S}, ["mount_path", "size_gib"])
schemas["GPU"] = obj({"count": I}, ["count"])
schemas["Service"]["properties"].update({"volume": ref("Volume"), "gpu": ref("GPU"), "run_as_user": I, "architecture": {"type":"string","enum":["amd64","arm64"]}})
schemas["WorkloadSecret"] = obj({"name": S, "updated_at": T}, ["name", "updated_at"])
route("/secrets", "get", "listWorkloadSecrets", items("WorkloadSecret"), scope=True)
route("/secrets/{name}", "put", "putWorkloadSecret", obj({"name": S, "saved": B, "restart_required": B}), obj({"value": S}, ["value"]), scope=True)
route("/secrets/{name}", "delete", "deleteWorkloadSecret", obj({"deleted": B}), scope=True)
for endpoint in ["/secrets", "/secrets/{name}"]:
    for method in paths[endpoint].values():
        method["parameters"].append({"name": "application", "in": "query", "required": True, "schema": S})
        if "{name}" in endpoint:
            method["parameters"].append({"name": "name", "in": "path", "required": True, "schema": S})
