schemas["CloudCapabilities"] = obj({
    "version": I, "mode": {"type":"string", "enum":["managed-cloud"]},
    "enforced": B, "node_limit": I, "node_count": I, "node_count_complete": B,
    "services_per_application": I, "replicas_per_service": I,
    "profiles": array(S), "public_tcp": B, "gpu": B, "aws_identity": B,
}, ["version", "mode", "enforced", "node_limit", "node_count", "node_count_complete", "services_per_application", "replicas_per_service", "profiles", "public_tcp", "gpu", "aws_identity"])
route("/cloud/capabilities", "get", "getCloudCapabilities", ref("CloudCapabilities"))
paths["/cloud/capabilities"]["get"]["parameters"] = [{"name":name, "in":"query", "required":True, "schema":S} for name in ["project", "environment"]]
