"""Bounded workload commands and managed PostgreSQL queries."""

schemas["PodExecInput"] = obj({
    "pod": S, "container": S,
    "command": {"type": "array", "items": {"type": "string", "maxLength": 4096}, "minItems": 1, "maxItems": 32},
    "timeout_seconds": {"type": "integer", "minimum": 1, "maximum": 20, "default": 20},
    "max_output_bytes": {"type": "integer", "minimum": 1, "maximum": 65536, "default": 65536},
}, ["pod", "container", "command"])
schemas["PodExecResult"] = obj({
    "execution_id": S, "pod": S, "container": S, "pod_uid": S,
    "outcome": {"type": "string", "enum": ["exited", "unknown"]},
    "exit_code": {"anyOf": [I, {"type": "null"}]}, "reason": S,
    "stdout_base64": S, "stderr_base64": S,
    "stdout_truncated": B, "stderr_truncated": B, "audit_recorded": B,
}, ["execution_id", "pod", "container", "pod_uid", "outcome", "exit_code", "stdout_base64", "stderr_base64", "stdout_truncated", "stderr_truncated", "audit_recorded"])
route("/applications/{id}/services/{service}/exec", "post", "executePodCommand", ref("PodExecResult"), ref("PodExecInput"))
paths["/applications/{id}/services/{service}/exec"]["post"]["parameters"].append({"name": "service", "in": "path", "required": True, "schema": S})
paths["/applications/{id}/services/{service}/exec"]["post"]["description"] = "Run one command in the specified owned container. Requires pods:exec. A machine key must explicitly grant this permission. An unknown outcome does not establish process termination."

scalar = {"type": ["string", "number", "boolean", "null"]}
schemas["DatabaseQueryInput"] = obj({
    "sql": {"type": "string", "minLength": 1, "maxLength": 65536},
    "parameters": {"type": "array", "items": scalar, "maxItems": 100},
    "read_only": {"type": "boolean", "default": True},
    "max_rows": {"type": "integer", "minimum": 1, "maximum": 1000, "default": 100},
    "max_bytes": {"type": "integer", "minimum": 1024, "maximum": 1048576, "default": 262144},
    "expected_revision": {"type": "integer", "minimum": 1, "description": "Reviewed database revision. Required when read_only is false."},
}, ["sql"])
schemas["DatabaseQueryColumn"] = obj({"name": S, "type_oid": I}, ["name", "type_oid"])
schemas["DatabaseQueryResult"] = obj({
    "operation_id": S, "database_id": S, "read_only": B,
    "columns": array(ref("DatabaseQueryColumn")),
    "rows": array(array({"type": ["string", "null"]})),
    "rows_affected": I, "truncated": B,
    "outcome": {"type": "string", "enum": ["read", "committed", "rolled_back"]},
}, ["operation_id", "database_id", "read_only", "columns", "rows", "rows_affected", "truncated", "outcome"])
schemas["DatabaseQueryError"] = obj({
    "error": schemas["Error"]["properties"]["error"], "operation_id": S,
    "outcome": {"type": "string", "enum": ["not_started", "rolled_back", "unknown", "read", "committed"]},
}, ["error", "operation_id", "outcome"])
route("/databases/{id}/query", "post", "queryManagedDatabase", ref("DatabaseQueryResult"), ref("DatabaseQueryInput"))
paths["/databases/{id}/query"]["post"]["description"] = "Run one PostgreSQL statement that supports EXPLAIN. Requires databases:query. Writes also require databases:write-query. Transaction control and COPY are unavailable. Results can contain private data."
paths["/databases/{id}/query"]["post"]["responses"]["default"]["content"]["application/json"]["schema"] = {"anyOf": [ref("Error"), ref("DatabaseQueryError")]}
