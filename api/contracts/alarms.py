"""Persisted, scoped alarms. The background worker does not depend on a browser."""
nullable_time = {"anyOf": [T, {"type": "null"}]}
schemas["Observation"]["properties"]["revision"] = I
schemas["Alarm"] = obj({
    "id": S, "rule": S, "resource_type": {"type": "string", "enum": ["application", "service", "node"]},
    "resource_id": S, "resource_name": S, "project": S, "environment": S, "application_id": S, "service": S,
    "status": {"type": "string", "enum": ["active", "recovered"]}, "summary": S,
    "first_observed_at": T, "fired_at": T, "recovered_at": nullable_time, "last_observed_at": T,
    "updated_at": T, "last_event_id": I, "read": B, "acknowledged": B,
    "observation_status": {"type": "string", "enum": ["healthy", "unhealthy", "unknown"]},
}, ["id", "rule", "resource_type", "resource_id", "resource_name", "project", "environment", "application_id", "service", "status", "summary", "first_observed_at", "fired_at", "recovered_at", "last_observed_at", "updated_at", "last_event_id", "read", "acknowledged", "observation_status"])
schemas["AlarmList"] = obj({"items": array(ref("Alarm")), "next_cursor": S, "summary": obj({"active": I, "unread": I}, ["active", "unread"])}, ["items", "next_cursor", "summary"])
schemas["AlarmSettings"] = obj({
    "project": S, "environment": S, "application_id": S, "enabled": B,
    "hold_seconds": {"type": "integer", "minimum": 0, "maximum": 3600}, "email_enabled": B,
    "revision": I, "inherited": B, "source": {"type": "string", "enum": ["default", "installation", "project", "environment", "application"]},
    "email_available": B, "can_manage": B,
}, ["project", "environment", "application_id", "enabled", "hold_seconds", "email_enabled", "revision", "inherited", "source", "email_available", "can_manage"])
schemas["AlarmSettingsInput"] = obj({"enabled": B, "hold_seconds": {"type": "integer", "minimum": 0, "maximum": 3600}, "email_enabled": B, "expected_revision": I}, ["enabled", "hold_seconds", "email_enabled", "expected_revision"])
schemas["AlarmReadInput"] = obj({"expected_event_id": I})
alarm_scope = [{"name": name, "in": "query", "schema": S} for name in ["project", "environment", "application_id"]]
route("/alarms", "get", "listAlarms", ref("AlarmList"))
paths["/alarms"]["get"]["parameters"] += alarm_scope + [{"name": "cursor", "in": "query", "schema": S}, {"name": "limit", "in": "query", "schema": {"type": "integer", "minimum": 1, "maximum": 50, "default": 25}}, {"name": "status", "in": "query", "schema": {"type": "string", "enum": ["active", "recovered"]}}]
route("/alarms/{id}/acknowledge", "post", "acknowledgeAlarm", ref("Alarm"), ref("AlarmReadInput"))
route("/alarms/{id}/read", "post", "readAlarm", ref("Alarm"), ref("AlarmReadInput"))
for path in ["/alarms/{id}/acknowledge", "/alarms/{id}/read"]:
    paths[path]["post"]["requestBody"]["required"] = False
route("/alarm-settings", "get", "getAlarmSettings", ref("AlarmSettings"))
route("/alarm-settings", "put", "putAlarmSettings", ref("AlarmSettings"), ref("AlarmSettingsInput"))
for method in ["get", "put"]:
    paths["/alarm-settings"][method]["parameters"] += alarm_scope
