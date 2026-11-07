"""SaaS custom hostnames: tenant domains a multi-tenant service attaches at runtime, and the installation cap."""
schemas["CustomHostnamesConfig"] = obj({
    "enabled": B,
    "max": {"type": "integer", "minimum": 1, "maximum": 50000, "description": "Most hostnames this service may hold; 1000 when omitted. Never above the installation's max_per_service."},
    "target": {**S, "description": "The CNAME target customers point at. Defaults to the service's generated public hostname."},
}, ["enabled"])
schemas["CustomHostnamesConfig"]["description"] = "Opt-in tenant hostnames added through the API without a new revision. Requires public with a port; rejected for jobs, serverless services, actions runners and sandbox sessions. See docs/custom-hostnames.md."
schemas["Service"]["properties"]["custom_hostnames"] = ref("CustomHostnamesConfig")
NULLABLE_TIME = {"anyOf": [T, {"type": "null"}]}
HOSTNAME_STATUS = {"type": "string", "enum": ["pending_dns", "issuing", "active", "failed", "deleting"]}
schemas["CustomHostname"] = obj({
    "id": S, "application_id": S, "service": S, "hostname": S, "status": HOSTNAME_STATUS, "status_reason": S,
    "created_at": T, "updated_at": T, "verified_at": NULLABLE_TIME, "active_at": NULLABLE_TIME,
    "last_checked_at": NULLABLE_TIME, "next_check_at": T, "attempts": I, "target": S,
}, ["id", "application_id", "service", "hostname", "status", "status_reason", "created_at", "updated_at", "verified_at", "active_at", "last_checked_at", "next_check_at", "attempts", "target"])
schemas["CustomHostnameList"] = obj({"items": array(ref("CustomHostname")), "next_cursor": S, "target": S}, ["items", "next_cursor", "target"])
schemas["CustomHostnameSettings"] = obj({"max_per_service": {"type": "integer", "minimum": 1, "maximum": 50000}, "revision": I}, ["max_per_service", "revision"])
schemas["CustomHostnameSettingsInput"] = obj({"max_per_service": {"type": "integer", "minimum": 1, "maximum": 50000}, "expected_revision": I}, ["max_per_service", "expected_revision"])

BASE = "/applications/{id}/services/{service}/custom-hostnames"
ONE = BASE + "/{hostname}"
route(BASE, "get", "listCustomHostnames", ref("CustomHostnameList"))
route(BASE, "post", "createCustomHostname", ref("CustomHostname"), obj({"hostname": S}, ["hostname"]), "201")
route(ONE, "get", "getCustomHostname", ref("CustomHostname"))
route(ONE, "delete", "deleteCustomHostname", ref("CustomHostname"), status="202")
paths[BASE]["post"]["responses"]["200"] = {"description": "The hostname is already attached to this service", "content": {"application/json": {"schema": ref("CustomHostname")}}}
paths[BASE]["get"]["parameters"] += [
    {"name": "status", "in": "query", "schema": HOSTNAME_STATUS},
    {"name": "cursor", "in": "query", "schema": S},
    {"name": "limit", "in": "query", "schema": {"type": "integer", "minimum": 1, "maximum": 500, "default": 100}},
]
route("/settings/custom-hostnames", "get", "getCustomHostnameSettings", ref("CustomHostnameSettings"))
route("/settings/custom-hostnames", "put", "putCustomHostnameSettings", ref("CustomHostnameSettings"), ref("CustomHostnameSettingsInput"))
for path, method, text, description in [
    (BASE, "get", "List a service's custom hostnames", "Cursor-paginated by hostname. Requires deployments:read."),
    (BASE, "post", "Attach a custom hostname to a service", "Returns 201 for a new hostname and 200 when it is already attached to this service; a failed hostname starts over. Requires hostnames:manage. At most 120 creations per application per minute; a 429 carries Retry-After."),
    (ONE, "get", "Show one custom hostname", "Requires deployments:read."),
    (ONE, "delete", "Remove a custom hostname", "Marks the hostname deleting; its route, certificate and TLS secret are removed in the background. Requires hostnames:manage."),
    ("/settings/custom-hostnames", "get", "Get the custom hostname installation cap", "Installation administrators only."),
    ("/settings/custom-hostnames", "put", "Update the custom hostname installation cap", "Installation administrators only."),
]:
    paths[path][method]["summary"] = text
    paths[path][method]["description"] = description
