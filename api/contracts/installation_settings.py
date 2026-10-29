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

cidrs = {"type": "array", "maxItems": 256, "items": {**S, "maxLength": 43}}
schemas["InstallationAccessPolicy"] = obj({
    "enabled": B,
    "client_ip_source": {"type": "string", "enum": ["connection", "trusted_proxy"]},
    "trusted_proxy_cidrs": cidrs,
    "client_ip_header": {"type": "string", "enum": ["", "X-Forwarded-For", "X-Real-IP", "CF-Connecting-IP"]},
    "allow_cidrs": cidrs,
    "firewall": {**B, "description": "Also close the dashboard port and the K3s API in the host firewall. Needs the private-access installer module."},
    "vpn": ref("InstallationAccessVPN"),
    "vpn_server": ref("InstallationAccessVPNServer"),
    "vpn_only": {**B, "description": "Admit only networks routed through the WireGuard connector. Needs a handshake from the last two minutes to turn on."},
}, ["enabled", "client_ip_source", "trusted_proxy_cidrs", "client_ip_header", "allow_cidrs"])
schemas["InstallationAccessVPN"] = obj({
    "address": {**S, "maxLength": 43, "description": "The server's tunnel address with its prefix, such as 10.99.0.2/32."},
    "peer_public_key": {**S, "maxLength": 44},
    "peer_endpoint": {**S, "maxLength": 260, "description": "host:port of the WireGuard peer."},
    "allowed_ips": {"type": "array", "maxItems": 64, "items": {**S, "maxLength": 43}},
    "keepalive": {"type": "integer", "minimum": 0, "maximum": 3600},
}, ["address", "peer_public_key", "peer_endpoint", "allowed_ips"])
schemas["InstallationAccessVPNServer"] = obj({
    "listen_port": {"type": "integer", "minimum": 0, "maximum": 65535, "description": "UDP port; 0 means 51820."},
    "network": {**S, "maxLength": 18, "description": "IPv4 network for VPN devices, /16 to /29. The server takes the first address."},
    "endpoint": {**S, "maxLength": 260, "description": "This server's public host:port written into device profiles."},
}, ["network", "endpoint"])
schemas["InstallationAccessVPNPeer"] = obj({
    "id": S, "identity_id": S, "name": S, "public_key": S, "address": S,
    "latest_handshake": {**S, "format": "date-time"}, "created_at": {**S, "format": "date-time"},
    "server_public_key": S, "endpoint": S, "server_address": S, "network": S,
}, ["id", "identity_id", "name", "public_key", "address", "created_at"])
schemas["InstallationAccessVPNPeerList"] = obj({"items": {"type": "array", "items": ref("InstallationAccessVPNPeer")}}, ["items"])
schemas["InstallationAccessVPNPeerInput"] = obj({
    "name": {**S, "minLength": 1, "maxLength": 64},
    "public_key": {**S, "maxLength": 44, "description": "The device's WireGuard public key. Generate the key pair on the device or in the browser; never send a private key."},
}, ["name", "public_key"])
schemas["InstallationAccessVPNStatus"] = obj({
    "public_key": {**S, "description": "The server's WireGuard public key; add it as a peer."},
    "server_public_key": {**S, "description": "The built-in VPN server's public key."},
    "server_error": S,
    "latest_handshake": {**S, "format": "date-time"},
    "rx_bytes": {"type": "integer"}, "tx_bytes": {"type": "integer"},
    "error": S, "reported_at": {**S, "format": "date-time"},
}, ["public_key", "server_public_key", "server_error", "rx_bytes", "tx_bytes", "error", "reported_at"])
schemas["InstallationAccess"] = obj({
    "revision": revision, "policy": ref("InstallationAccessPolicy"),
    "pending_until": {**S, "format": "date-time", "description": "Set while a saved policy waits for confirmation; it reverts after this time."},
    "client_ip": {**S, "description": "The address the installation sees for this request."},
    "licensed": B,
    "vpn_status": ref("InstallationAccessVPNStatus"),
}, ["revision", "policy", "client_ip", "licensed"])
schemas["InstallationAccessInput"] = obj({
    "expected_revision": revision, "policy": ref("InstallationAccessPolicy"),
    "confirm_from_elsewhere": {**B, "description": "Save even though the policy blocks this request's network; confirm from an allowed network within five minutes."},
}, ["expected_revision", "policy"])
schemas["InstallationAccessConfirmInput"] = obj({"revision": revision}, ["revision"])
for name in ["InstallationAccessPolicy", "InstallationAccessVPN", "InstallationAccessVPNServer", "InstallationAccessVPNPeer", "InstallationAccessVPNPeerList", "InstallationAccessVPNPeerInput", "InstallationAccessVPNStatus", "InstallationAccess", "InstallationAccessInput", "InstallationAccessConfirmInput"]:
    schemas[name]["additionalProperties"] = False
for path, method, operation, response, request in [
    ("/installation/access", "get", "getInstallationAccess", "InstallationAccess", None),
    ("/installation/access", "put", "putInstallationAccess", "InstallationAccess", "InstallationAccessInput"),
    ("/installation/access/confirm", "post", "confirmInstallationAccess", "InstallationAccess", "InstallationAccessConfirmInput"),
    ("/installation/access/vpn-peers", "get", "listInstallationVPNPeers", "InstallationAccessVPNPeerList", None),
    ("/installation/access/vpn-peers", "post", "createInstallationVPNPeer", "InstallationAccessVPNPeer", "InstallationAccessVPNPeerInput"),
]:
    route(path, method, operation, ref(response), ref(request) if request else None)
    item = paths[path][method]
    item["responses"]["403"] = {"description": "Self-hosted installation owner in a browser session required"}
    if method != "get":
        item["responses"]["409"] = {"description": "Revision changed, the confirmation window ended, or the policy would block this network"}
route("/installation/access/vpn-peers/{id}", "delete", "revokeInstallationVPNPeer", None, None)
paths["/installation/access/vpn-peers/{id}"]["delete"]["responses"]["403"] = {"description": "Installation administrator in a browser session required"}
