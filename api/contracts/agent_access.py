"""Agent policies: block a machine key's request or hold it for human approval."""
nullable_time = {"anyOf": [T, {"type": "null"}]}
nullable_string = {"anyOf": [S, {"type": "null"}]}
status = {"type": "string", "enum": ["draft", "enforced", "monitor"]}
published_status = {"type": "string", "enum": ["enforced", "monitor"]}
decision = {"type": "string", "enum": ["allow", "block", "require_approval", "approved", "exception", "monitor_block", "monitor_approval"]}
schemas["AgentPolicyCondition"] = obj({
    "field": {"type": "string", "enum": ["team", "key", "category", "operation", "project", "environment"]},
    "operator": {"type": "string", "enum": ["is_any_of", "is_none_of", "matches"]},
    "values": {"type": "array", "items": {"type": "string", "minLength": 1, "maxLength": 128}, "minItems": 1, "maxItems": 16},
}, ["field", "operator", "values"])
schemas["AgentPolicyApprover"] = obj({"kind": {"type": "string", "enum": ["project_admins", "team", "identity"]}, "id": S}, ["kind"])
schemas["AgentPolicyDocument"] = obj({
    "schema_version": {"type": "integer", "const": 1},
    "description": {"type": "string", "maxLength": 200},
    "when": {"type": "array", "items": ref("AgentPolicyCondition"), "minItems": 1, "maxItems": 8},
    "then": obj({
        "effect": {"type": "string", "enum": ["block", "require_approval"]},
        "approvers": {"type": "array", "items": ref("AgentPolicyApprover"), "maxItems": 8},
        "approval_ttl_seconds": {"type": "integer", "minimum": 300, "maximum": 86400},
        "max_credential_ttl_seconds": {"type": "integer", "minimum": 300, "maximum": 2592000},
    }, ["effect"]),
    "except": obj({"active_incident": B}, ["active_incident"]),
}, ["schema_version", "description", "when", "then"])
schemas["AgentPolicy"] = obj({
    "id": S, "name": S, "status": status, "current_version": I, "revision": I,
    "document": {"anyOf": [ref("AgentPolicyDocument"), {"type": "null"}]},
    "draft": {"anyOf": [ref("AgentPolicyDocument"), {"type": "null"}]},
    "created_at": T, "updated_at": T, "updated_by": S, "updated_by_name": S,
}, ["id", "name", "status", "current_version", "revision", "document", "draft", "created_at", "updated_at", "updated_by", "updated_by_name"])
schemas["AgentPolicyList"] = obj({
    "items": array(ref("AgentPolicy")),
    "summary": obj({"total": I, "enforced": I, "monitor": I, "draft": I, "pending_approvals": I}, ["total", "enforced", "monitor", "draft", "pending_approvals"]),
    "licensed": B,
    "evaluating_since": nullable_time,
}, ["items", "summary", "licensed", "evaluating_since"])
schemas["AgentPolicyCreateInput"] = obj({"name": {"type": "string", "minLength": 1, "maxLength": 100}, "document": ref("AgentPolicyDocument")}, ["name", "document"])
schemas["AgentPolicyDraftInput"] = obj({"expected_revision": I, "name": {"type": "string", "minLength": 1, "maxLength": 100}, "document": ref("AgentPolicyDocument")}, ["expected_revision", "name", "document"])
schemas["AgentPolicyPublishInput"] = obj({"expected_revision": I, "status": published_status}, ["expected_revision", "status"])
schemas["AgentPolicyVersion"] = obj({
    "version": I, "name": S, "status": published_status, "document": ref("AgentPolicyDocument"),
    "published_at": T, "published_by": S, "published_by_name": S,
}, ["version", "name", "status", "document", "published_at", "published_by", "published_by_name"])
schemas["AgentPolicyPreviewInput"] = obj({"document": ref("AgentPolicyDocument"), "days": {"type": "integer", "minimum": 1, "maximum": 30}}, ["document", "days"])
schemas["AgentPolicySample"] = obj({
    "observed_at": T, "key_id": S, "key_name": S, "operation": S, "category": S,
    "project": S, "environment": S, "decision": decision,
}, ["observed_at", "key_id", "key_name", "operation", "category", "project", "environment", "decision"])
schemas["AgentPolicyPreview"] = obj({
    "days": I, "evaluated": I, "matched": I, "require_approval": I, "blocked": I, "exceptions": I,
    "evaluating_since": nullable_time, "samples": array(ref("AgentPolicySample")),
}, ["days", "evaluated", "matched", "require_approval", "blocked", "exceptions", "evaluating_since", "samples"])
schemas["AgentPolicyMatch"] = obj({"policy_id": S, "name": S, "version": I, "status": S, "effect": S, "exception": B}, ["policy_id", "name", "version", "status", "effect", "exception"])
schemas["AgentPolicySimulationInput"] = obj({
    "key_id": S, "operation": S, "project": S, "environment": S, "active_incident": B,
    "policy_id": S, "document": ref("AgentPolicyDocument"),
}, ["key_id", "operation"])
schemas["AgentPolicySimulation"] = obj({
    "decision": decision, "policy": {"anyOf": [ref("AgentPolicyMatch"), {"type": "null"}]},
    "matches": array(ref("AgentPolicyMatch")), "reason": S, "category": S,
}, ["decision", "policy", "matches", "reason", "category"])
schemas["AgentApproval"] = obj({
    "id": S, "policy_id": S, "policy_name": S, "policy_version": I,
    "key_id": S, "key_name": S, "identity_id": S, "identity_name": S,
    "operation": S, "category": S, "method": S, "path": S, "project": S, "environment": S,
    "digest": S, "approvers": array(ref("AgentPolicyApprover")),
    "state": {"type": "string", "enum": ["pending", "approved", "denied", "expired", "consumed"]},
    "decided_by": nullable_string, "decided_by_name": nullable_string, "decided_at": nullable_time,
    "reason": S, "expires_at": T, "consumed_at": nullable_time, "created_at": T,
    "can_decide": B,
}, ["id", "policy_id", "policy_name", "policy_version", "key_id", "key_name", "identity_id", "identity_name", "operation", "category", "method", "path", "project", "environment", "digest", "approvers", "state", "decided_by", "decided_by_name", "decided_at", "reason", "expires_at", "consumed_at", "created_at", "can_decide"])
schemas["AgentApprovalList"] = obj({"items": array(ref("AgentApproval")), "next_cursor": S, "pending": I}, ["items", "next_cursor", "pending"])
schemas["AgentApprovalDecisionInput"] = obj({"reason": {"type": "string", "maxLength": 500}})

route("/agent-policies", "get", "listAgentPolicies", ref("AgentPolicyList"))
route("/agent-policies", "post", "createAgentPolicy", ref("AgentPolicy"), ref("AgentPolicyCreateInput"), "201")
route("/agent-policies/preview", "post", "previewAgentPolicy", ref("AgentPolicyPreview"), ref("AgentPolicyPreviewInput"))
route("/agent-policies/simulate", "post", "simulateAgentPolicy", ref("AgentPolicySimulation"), ref("AgentPolicySimulationInput"))
route("/agent-policies/{id}", "get", "getAgentPolicy", ref("AgentPolicy"))
route("/agent-policies/{id}", "delete", "deleteAgentPolicy", obj({"status": S}, ["status"]))
paths["/agent-policies/{id}"]["delete"]["parameters"].append({"name": "expected_revision", "in": "query", "required": True, "schema": I})
route("/agent-policies/{id}/draft", "put", "saveAgentPolicyDraft", ref("AgentPolicy"), ref("AgentPolicyDraftInput"))
route("/agent-policies/{id}/publish", "post", "publishAgentPolicy", ref("AgentPolicy"), ref("AgentPolicyPublishInput"))
route("/agent-policies/{id}/versions", "get", "listAgentPolicyVersions", items("AgentPolicyVersion"))
route("/agent-approvals", "get", "listAgentApprovals", ref("AgentApprovalList"))
paths["/agent-approvals"]["get"]["parameters"] += [
    {"name": "state", "in": "query", "schema": {"type": "string", "enum": ["pending", "decided"], "default": "pending"}},
    {"name": "cursor", "in": "query", "schema": S},
    {"name": "limit", "in": "query", "schema": {"type": "integer", "minimum": 1, "maximum": 50, "default": 25}},
]
route("/agent-approvals/{id}", "get", "getAgentApproval", ref("AgentApproval"))
route("/agent-approvals/{id}/approve", "post", "approveAgentApproval", ref("AgentApproval"), ref("AgentApprovalDecisionInput"))
route("/agent-approvals/{id}/deny", "post", "denyAgentApproval", ref("AgentApproval"), ref("AgentApprovalDecisionInput"))
for path in ["/agent-approvals/{id}/approve", "/agent-approvals/{id}/deny"]:
    paths[path]["post"]["requestBody"]["required"] = False
