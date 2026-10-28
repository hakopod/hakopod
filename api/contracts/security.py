"""Security advisories: feed status, per-application findings, fixes and policy."""
nullable_time = {"anyOf": [T, {"type": "null"}]}
def enum(*values): return {"type": "string", "enum": list(values)}
schemas["SecurityFeedStatus"] = obj({"feed_enabled": B, "osv_enabled": B, "revision": I, "issued_at": nullable_time, "expires_at": nullable_time, "checked_at": nullable_time, "stale": B, "error": S},
    ["feed_enabled", "osv_enabled", "revision", "issued_at", "expires_at", "checked_at", "stale", "error"])
schemas["SecurityFinding"] = obj({"advisory_id": S, "aliases": array(S), "advisory_revision": I, "source": enum("hakopod", "osv"), "severity": enum("critical", "high", "moderate", "low"), "title": S, "url": S, "service": S,
    "confidence": enum("confirmed", "possible"), "evidence": S, "fix": enum("reresolve", "rebuild", "manual"), "fix_reason": S, "auto_eligible": B, "fixed_digests": array(S)},
    ["advisory_id", "advisory_revision", "source", "severity", "title", "url", "service", "confidence", "evidence", "fix", "fix_reason", "auto_eligible", "fixed_digests"])
schemas["SecurityPatchAttempt"] = obj({"id": S, "advisory_id": S, "service": S, "status": enum("pending", "accepted", "verified", "failed", "superseded"), "deployment_id": S, "error": S, "created_at": T, "updated_at": T},
    ["id", "advisory_id", "service", "status", "deployment_id", "error", "created_at", "updated_at"])
schemas["ApplicationSecurity"] = obj({"application_id": S, "application_name": S, "project": S, "environment": S, "deployment_id": S, "revision": I, "checked_at": nullable_time, "evaluated_at": nullable_time,
    "findings": array(ref("SecurityFinding")), "unknown_services": array(S), "incomplete": array(S), "auto_patch": B, "approved_by": S, "approval_expires_at": nullable_time, "policy_revision": I, "attempts": array(ref("SecurityPatchAttempt")), "can_write": B},
    ["application_id", "application_name", "project", "environment", "deployment_id", "revision", "checked_at", "evaluated_at", "findings", "unknown_services", "incomplete", "auto_patch", "approved_by", "approval_expires_at", "policy_revision", "attempts", "can_write"])
schemas["SecurityOverview"] = obj({"feed": ref("SecurityFeedStatus"), "applications": array(ref("ApplicationSecurity")), "summary": obj({"affected_applications": I, "critical": I, "high": I}, ["affected_applications", "critical", "high"])},
    ["feed", "applications", "summary"])
schemas["SecurityApplyInput"] = obj({"advisory_id": S, "service": S, "expected_revision": I}, ["advisory_id", "service", "expected_revision"])
schemas["SecurityApplyResult"] = obj({"kind": enum("reresolve", "rebuild"), "deployment_id": S, "build_id": S}, ["kind", "deployment_id", "build_id"])
schemas["SecurityPolicyInput"] = obj({"auto_patch": B, "expected_policy_revision": I}, ["auto_patch", "expected_policy_revision"])
schemas["SecuritySettings"] = obj({"feed_enabled": B, "osv_enabled": B, "revision": I, "can_manage": B}, ["feed_enabled", "osv_enabled", "revision", "can_manage"])
schemas["SecuritySettingsInput"] = obj({"feed_enabled": B, "osv_enabled": B, "expected_revision": I}, ["feed_enabled", "osv_enabled", "expected_revision"])
for path, method, operation, response, request, summary, kw in [
    ("/security/advisories", "get", "getSecurityOverview", ref("SecurityOverview"), None, "List security advisory findings for visible applications.", {}),
    ("/applications/{id}/security", "get", "getApplicationSecurity", ref("ApplicationSecurity"), None, "Get security advisory findings for an application.", {}),
    ("/applications/{id}/security/check", "post", "checkApplicationSecurity", ref("ApplicationSecurity"), None, "Check an application against security advisories now.", {}),
    ("/applications/{id}/security/apply", "post", "applySecurityFix", ref("SecurityApplyResult"), ref("SecurityApplyInput"), "Apply the fix for a security advisory finding.", {"idem": True}),
    ("/applications/{id}/security/policy", "put", "putSecurityPolicy", ref("ApplicationSecurity"), ref("SecurityPolicyInput"), "Set the automatic patching policy for an application.", {}),
    ("/security/settings", "get", "getSecuritySettings", ref("SecuritySettings"), None, "Get installation security advisory settings.", {}),
    ("/security/settings", "put", "putSecuritySettings", ref("SecuritySettings"), ref("SecuritySettingsInput"), "Update installation security advisory settings.", {}),
]:
    route(path, method, operation, response, request, **kw)
    paths[path][method]["summary"] = summary
