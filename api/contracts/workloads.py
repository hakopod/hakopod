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
