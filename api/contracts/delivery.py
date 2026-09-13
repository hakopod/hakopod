schemas["PublicTCPListener"] = obj({"port": I, "target_port": I, "source_cidrs": array(S)}, ["port", "target_port", "source_cidrs"])
schemas["CertificateMount"] = obj({"certificate": S, "hostname": S, "mount_path": S}, ["certificate", "hostname", "mount_path"])
schemas["Service"]["properties"].update({"public_tcp": array(ref("PublicTCPListener")), "certificate_mounts": array(ref("CertificateMount")), "aws_identity": S})
schemas["PublicTCPStatus"] = obj({"port": I, "target_port": I, "status": S, "message": S, "addresses": array(S)}, ["port", "target_port", "status", "message"])
schemas["AWSIdentityState"] = obj({"binding": S, "role_arn": S, "region": S, "service_account": S, "token_audience": S, "status": S, "aws_verified": B, "message": S}, ["binding", "role_arn", "region", "service_account", "token_audience", "status", "aws_verified"])
schemas["PublicTCPPolicy"] = obj({"mode": {"type": "string", "enum": ["self-hosted", "managed-cloud"]}, "allowed": B, "message": S}, ["mode", "allowed", "message"])
schemas["ServiceDelivery"] = obj({"public_tcp": array(ref("PublicTCPStatus")), "public_tcp_policy": ref("PublicTCPPolicy"), "aws_identity": {"anyOf": [ref("AWSIdentityState"), {"type": "null"}]}, "observed_at": T}, ["public_tcp", "public_tcp_policy", "aws_identity", "observed_at"])
schemas["BackendCertificate"] = obj({"certificate": S, "hostname": S, "mount_path": S, "source": S, "ready": B, "not_before": T, "expires_at": T, "message": S}, ["certificate", "hostname", "source", "ready"])
schemas["BackendCertificateInput"] = obj({"hostname": S, "certificate_pem": {"type": "string", "writeOnly": True}, "private_key_pem": {"type": "string", "writeOnly": True}, "from_ingress": B}, ["hostname"])
base = "/applications/{id}/services/{service}"
route(base + "/delivery", "get", "getServiceDelivery", ref("ServiceDelivery"))
route(base + "/certificates", "get", "listBackendCertificates", items("BackendCertificate"))
route(base + "/certificates", "post", "uploadBackendCertificate", ref("BackendCertificate"), ref("BackendCertificateInput"), "201")
for endpoint in [base + "/delivery", base + "/certificates"]:
    for operation in paths[endpoint].values():
        operation["parameters"].append({"name": "service", "in": "path", "required": True, "schema": S})
