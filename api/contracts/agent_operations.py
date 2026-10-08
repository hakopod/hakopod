"""Bounded workload commands and managed SQL queries."""

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
paths["/applications/{id}/services/{service}/exec"]["post"]["description"] = "Run one command in the specified owned container. Requires pods:exec. CLI and machine credentials require an explicit pods:exec grant. An unknown outcome does not establish process termination."

scalar = {"type": ["string", "number", "boolean", "null"]}
schemas["DatabaseQueryInput"] = obj({
    "sql": {"type": "string", "minLength": 1, "maxLength": 65536},
    "parameters": {"type": "array", "items": scalar, "maxItems": 100},
    "read_only": {"type": "boolean", "default": True},
    "max_rows": {"type": "integer", "minimum": 1, "maximum": 1000, "default": 100},
    "max_bytes": {"type": "integer", "minimum": 1024, "maximum": 1048576, "default": 262144},
    "expected_revision": {"type": "integer", "minimum": 1, "description": "Reviewed database revision. Required when read_only is false."},
    "execution_mode": {"type": "string", "enum": ["transaction", "nontransactional"], "description": "Use a mode from the database query capabilities. Nontransactional writes can commit before the response arrives."},
}, ["sql"])
schemas["DatabaseQueryColumn"] = obj({"name": S, "type_oid": {"type": "integer", "description": "PostgreSQL wire-protocol type OID. Drivers without OIDs return zero."}, "type_name": S}, ["name", "type_oid"])
schemas["DatabaseQueryResult"] = obj({
    "operation_id": S, "database_id": S, "read_only": B,
    "columns": array(ref("DatabaseQueryColumn")),
    "rows": array(array({"type": ["string", "null"]})),
    "rows_affected": I, "truncated": B,
    "outcome": {"type": "string", "enum": ["read", "committed", "applied"]},
}, ["operation_id", "database_id", "read_only", "columns", "rows", "rows_affected", "truncated", "outcome"])
schemas["DatabaseQueryError"] = obj({
    "error": schemas["Error"]["properties"]["error"], "operation_id": S,
    "outcome": {"type": "string", "enum": ["not_started", "rolled_back", "unknown", "read", "committed", "applied"]},
}, ["error", "operation_id", "outcome"])
route("/databases/{id}/query", "post", "queryManagedDatabase", ref("DatabaseQueryResult"), ref("DatabaseQueryInput"))
paths["/databases/{id}/query"]["post"]["description"] = "Run one SQL statement using the managed application identity. Read the engine capabilities first. All queries require databases:query. Writes also require databases:write-query and a reviewed database revision. CLI and machine credentials require explicit SQL grants. Results can contain private data. Check an unknown outcome before retrying."
paths["/databases/{id}/query"]["post"]["responses"]["default"]["content"]["application/json"]["schema"] = {"anyOf": [ref("Error"), ref("DatabaseQueryError")]}

schemas["DatabaseQueryCapabilities"] = obj({
    "engine": S,
    "supported": {"type": "boolean", "description": "The query API can execute statements for this engine. This value does not grant permission."},
    "read_only_supported": {"type": "boolean", "description": "The engine enforces read-only execution. If false, every SQL request requires explicit write access."},
    "execution_modes": {"type": "array", "items": {"type": "string", "enum": ["transaction", "nontransactional"]}},
    "application_identity": S, "parameter_style": S, "read_only_enforcement": S,
    "transaction_scope": {"type": "string", "enum": ["none", "connection", "single_shard"], "description": "Scope of transaction guarantees. Scatter reads do not imply a globally synchronized snapshot."},
    "cross_shard_dml": {"type": "boolean", "description": "Whether data changes may span shards. If false, data changes must target one shard, including nontransactional requests. Schema changes can partially apply."},
    "transactional_dml": B, "transactional_ddl": B, "ddl_commit": S, "cancellation": S,
}, ["engine", "supported", "read_only_supported", "execution_modes", "application_identity", "parameter_style", "read_only_enforcement", "transactional_dml", "transactional_ddl", "ddl_commit", "cancellation", "transaction_scope"])
route("/databases/{id}/query-capabilities", "get", "getDatabaseQueryCapabilities", ref("DatabaseQueryCapabilities"))
paths["/databases/{id}/query-capabilities"]["get"]["description"] = "Read SQL execution support, modes, parameter format, and transaction behavior for this database engine. Requires access to read the database."
