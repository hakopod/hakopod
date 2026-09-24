"""Explicit durable reclamation of deleted applications' persistent data."""
schemas["RetainedApplicationData"] = obj({"application_id":S,"project":S,"environment":S,"name":S,"status":S,"error":S,"reserved_gib":I},["application_id","project","environment","name","status","error","reserved_gib"])
route("/storage/retained","get","listRetainedApplicationData",items("RetainedApplicationData"),scope=True)
route("/storage/retained/{id}","delete","deleteRetainedApplicationData",obj({"status":S},["status"]),obj({"confirm_name":S},["confirm_name"]),"202")
paths["/applications/{id}"]["delete"]["requestBody"]["content"]["application/json"]["schema"]["properties"]["delete_data"] = B

for name in ["PlanInput", "DeployInput"]:
    schemas[name]["properties"]["delete_service_volumes"] = {**array(S), "maxItems":20, "uniqueItems":True}
schemas["VolumeCleanup"] = obj({"claims":array(S),"status":S,"error":S},["claims","status","error"])
schemas["Deployment"]["properties"]["volume_cleanup"] = ref("VolumeCleanup")
route("/deployments/{id}/volume-cleanup","post","retryServiceVolumeCleanup",obj({"status":S},["status"]),obj({}),"202")
