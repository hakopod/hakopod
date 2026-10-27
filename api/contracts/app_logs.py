"""Historic application logs: the pods that served an app and the installation retention settings."""
schemas["AppLogSource"] = obj({
    "pod_uid": S, "pod_name": S, "service": S, "revision": I, "node": S,
    "source": {"type": "string", "enum": ["file", "api"]},
    "started_at": T, "ended_at": {"anyOf": [T, {"type": "null"}]},
    "end_reason": {"type": "string", "enum": ["", "completed", "error", "oom_killed", "evicted", "replaced", "deleted"]},
    "restarts": I, "gap_seconds": I, "gap_note": S, "current": B,
}, ["pod_uid", "pod_name", "service", "revision", "node", "source", "started_at", "ended_at", "end_reason", "restarts", "gap_seconds", "current"])
schemas["AppLogSourceList"] = obj({"items": array(ref("AppLogSource")), "retention_days": I, "available": B, "message": S}, ["items", "retention_days", "available", "message"])
schemas["AppLogSettings"] = obj({
    "enabled": B, "retention_days": {"type": "integer", "minimum": 1, "maximum": 30},
    "max_mib_per_app": {"type": "integer", "minimum": 64, "maximum": 4096}, "revision": I,
}, ["enabled", "retention_days", "max_mib_per_app", "revision"])
schemas["AppLogSettingsInput"] = obj({
    "enabled": B, "retention_days": {"type": "integer", "minimum": 1, "maximum": 30},
    "max_mib_per_app": {"type": "integer", "minimum": 64, "maximum": 4096}, "expected_revision": I,
}, ["enabled", "retention_days", "max_mib_per_app", "expected_revision"])
route("/applications/{id}/log-sources", "get", "listApplicationLogSources", ref("AppLogSourceList"))
paths["/applications/{id}/log-sources"]["get"]["parameters"] += [
    {"name": "from", "in": "query", "schema": T}, {"name": "to", "in": "query", "schema": T},
    {"name": "limit", "in": "query", "schema": {"type": "integer", "minimum": 1, "maximum": 500, "default": 200}},
]
route("/settings/app-logs", "get", "getAppLogSettings", ref("AppLogSettings"))
route("/settings/app-logs", "put", "putAppLogSettings", ref("AppLogSettings"), ref("AppLogSettingsInput"))
for path, method, text in [
    ("/applications/{id}/log-sources", "get", "List current and past pods that wrote logs for an application"),
    ("/settings/app-logs", "get", "Get application log history settings"),
    ("/settings/app-logs", "put", "Update application log history settings"),
]:
    paths[path][method]["summary"] = text
