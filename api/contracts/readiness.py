schemas["Readiness"] = obj({"protocol": {"type": "string", "enum": ["tcp", "smtp", "smtp_starttls"]}, "port": I, "tls_server_name": S, "tls_ca_file": S, "period_seconds": I, "timeout_seconds": I, "failure_threshold": I}, ["protocol", "port"])
schemas["Service"]["properties"]["readiness"] = ref("Readiness")

schemas["Service"]["properties"]["update_strategy"] = {"type": "string", "enum": ["rolling", "recreate"]}
