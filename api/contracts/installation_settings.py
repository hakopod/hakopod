"""Self-hosted administrator settings. Credentials remain write-only."""
provider_names = {"type": "string", "enum": ["github", "google", "gitlab", "oidc"]}
revision = {"type": "integer", "minimum": 0}
source = {"type": "string", "enum": ["operator", "settings"]}
port = {"type": "integer", "minimum": 1, "maximum": 65535}
security = {"type": "string", "enum": ["starttls", "tls"]}

schemas["InstallationLoginProvider"] = obj({
    "provider": provider_names, "enabled": B,
    "client_id": {**S, "maxLength": 512}, "secret_configured": B,
    "issuer_url": {**S, "maxLength": 2048}, "revision": revision,
    "callback_url": S, "source": source, "encryption_ready": B,
}, ["provider", "enabled", "client_id", "secret_configured", "issuer_url", "revision", "callback_url", "encryption_ready"])
schemas["InstallationLoginProviderInput"] = obj({
    "enabled": B, "client_id": {**S, "maxLength": 512},
    "client_secret": {**S, "writeOnly": True, "maxLength": 8192},
    "clear_secret": B, "issuer_url": {**S, "maxLength": 2048}, "expected_revision": revision,
}, ["enabled", "client_id", "issuer_url", "expected_revision"])

smtp_fields = {
    "enabled": B, "host": {**S, "maxLength": 253}, "port": port, "security": security,
    "username": {**S, "maxLength": 256, "description": "At most 256 UTF-8 bytes, without control characters."},
    "from_email": {**S, "maxLength": 254, "description": "At most 254 UTF-8 bytes."},
}
schemas["InstallationSMTP"] = obj({
    "revision": revision, "source": source, **smtp_fields,
    "password_set": B, "encryption_ready": B,
}, ["revision", "source", *smtp_fields, "password_set", "encryption_ready"])
schemas["InstallationSMTPInput"] = obj({
    "expected_revision": revision, **smtp_fields,
    "password": {**S, "writeOnly": True, "maxLength": 4096, "description": "At most 4096 UTF-8 bytes, without NUL or line breaks. Omit to keep the stored password."},
    "clear_password": B,
}, ["expected_revision", *smtp_fields])
schemas["InstallationSMTPTestInput"] = obj({"expected_revision": revision}, ["expected_revision"])
schemas["InstallationSMTPTestResult"] = obj({"sent": B, "recipient": S}, ["sent", "recipient"])
for name in ["InstallationLoginProvider", "InstallationLoginProviderInput", "InstallationSMTP", "InstallationSMTPInput", "InstallationSMTPTestInput", "InstallationSMTPTestResult"]:
    schemas[name]["additionalProperties"] = False

for path, method, operation, response, request in [
    ("/installation/login-providers/{provider}", "get", "getInstallationLoginProvider", "InstallationLoginProvider", None),
    ("/installation/login-providers/{provider}", "put", "putInstallationLoginProvider", "InstallationLoginProvider", "InstallationLoginProviderInput"),
    ("/installation/smtp", "get", "getInstallationSMTP", "InstallationSMTP", None),
    ("/installation/smtp", "put", "putInstallationSMTP", "InstallationSMTP", "InstallationSMTPInput"),
    ("/installation/smtp/test", "post", "testInstallationSMTP", "InstallationSMTPTestResult", "InstallationSMTPTestInput"),
]:
    route(path, method, operation, ref(response), ref(request) if request else None)
    item = paths[path][method]
    item["responses"]["403"] = {"description": "Self-hosted installation administrator access required"}
    if method != "get":
        item["responses"]["409"] = {"description": "Configuration revision changed"}
    if "{provider}" in path:
        item["parameters"].append({"name": "provider", "in": "path", "required": True, "schema": provider_names})
