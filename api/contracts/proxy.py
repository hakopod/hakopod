schemas["ProxyField"] = obj({"name": S, "description": S, "example": S}, ["name", "description", "example"])

edge_cidrs = {**array({**S, "maxLength": 64}), "maxItems": 64, "description": "IPv4 or IPv6 CIDRs. Addresses are normalized to network prefixes; invalid values are rejected."}
edge_countries = {**array({**S, "minLength": 2, "maxLength": 2}), "maxItems": 64, "description": "ISO 3166-1 alpha-2 country codes. Requires a country header from an explicitly trusted proxy."}
schemas["EdgeRule"] = {
    **obj({
        "id": {**S, "minLength": 1, "maxLength": 32, "description": "Unique stable rule identifier."},
        "host": {**S, "minLength": 1, "maxLength": 253, "description": "Exact DNS hostname. Wildcards, ports and schemes are rejected."},
        "path_prefix": {**S, "maxLength": 128, "default": "/", "description": "Case-sensitive prefix of the canonical request path; queries are excluded. Noncanonical paths on covered hosts are rejected before routing."},
        "allow_cidrs": edge_cidrs,
        "deny_cidrs": edge_cidrs,
        "allow_countries": edge_countries,
        "deny_countries": edge_countries,
        "requests_per_second": {**I, "minimum": 0, "maximum": 100000, "description": "Per-client one-second HAProxy request rate. Zero disables limiting. Counters are local to each listener/process, not a global quota."},
    }, ["id", "host"]),
    "additionalProperties": False,
}
schemas["EdgePolicy"] = {
    **obj({
        "enabled": B,
        "client_ip_source": {**S, "enum": ["connection", "trusted_proxy"], "default": "connection"},
        "trusted_proxy_cidrs": {**edge_cidrs, "maxItems": 32, "description": "Peer networks allowed to assert client identity. Required in trusted_proxy mode; a /0 trust range is rejected."},
        "client_ip_header": {**S, "enum": ["", "CF-Connecting-IP", "X-Real-IP"]},
        "country_header": {**S, "enum": ["", "CF-IPCountry", "CloudFront-Viewer-Country"]},
        "rules": {**array(ref("EdgeRule")), "maxItems": 32, "description": "Ordered rules. The first matching host/path rule wins; rules do not inherit or combine. Deny lists take precedence. At most 512 CIDRs across the policy."},
    }, ["enabled", "client_ip_source", "rules"]),
    "additionalProperties": False,
    "description": "Hakopod Edge traffic protection. Replaces the whole policy when supplied. Disabled policies retain validated rules. Trusted proxy mode rejects protected requests with untrusted peers or unavailable client identity; country rules reject unavailable geography. No GeoIP database is installed.",
}
schemas["ProxyConfiguration"] = obj({"namespace": S, "name": S, "resource_version": S, "settings": mapping(S), "edge": ref("EdgePolicy"), "fields": array(ref("ProxyField")), "applied_revision": S}, ["namespace", "name", "resource_version", "settings", "edge", "fields"])
schemas["ProxyChange"] = obj({"settings": mapping(S), "edge": ref("EdgePolicy"), "status": S, "error": S, "applied_version": S, "attempts": I, "retry_at": T}, ["status", "error"])
schemas["ProxyStatus"] = obj({"observed": ref("ProxyConfiguration"), "revision": I, "change": ref("ProxyChange"), "drift": B}, ["observed", "revision", "change", "drift"])
schemas["ProxySettingsPatch"] = {**mapping({**S, "maxLength": 32}), "maxProperties": 21, "description": "Changed HAProxy controller fields. Read the observed catalog for supported names and bounds. All values are strings; an empty string resets a field, and omitted fields remain unchanged. Arbitrary directives are rejected."}
route("/settings/haproxy", "get", "getProxy", ref("ProxyStatus"))
edge_patch = obj({"settings": ref("ProxySettingsPatch"), "edge": ref("EdgePolicy"), "expected_revision": {**I, "minimum": 0}, "expected_resource_version": {**S, "minLength": 1}}, ["expected_revision", "expected_resource_version"])
edge_patch["description"] = "Provide a nonempty settings patch, an edge policy, or both. Edge replaces the whole policy; omission preserves it. Both changes share one reviewed durable revision."
edge_patch["additionalProperties"] = False
route("/settings/haproxy", "patch", "setProxy", obj({"revision": I, "status": S}, ["revision", "status"]), edge_patch, "202")
