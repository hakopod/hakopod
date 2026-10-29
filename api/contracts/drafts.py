"""Staged changes: one shared draft per application, reviewed and deployed together."""
schemas["Plan"]["properties"]["draft_revision"] = I
schemas["DraftEntry"] = obj({"identity_id": S, "author": S, "at": T, "changes": array(ref("Change"))}, ["identity_id", "author", "at", "changes"])
schemas["Draft"] = obj({
    "application_id": S, "revision": I, "base_revision": I, "current_revision": I, "stale": B,
    "conflicts": array(S), "spec": ref("Spec"), "changes": array(ref("Change")),
    "entries": array(ref("DraftEntry")), "created_at": T, "updated_at": T,
}, ["application_id", "revision", "base_revision", "current_revision", "stale", "conflicts", "spec", "changes", "entries", "created_at", "updated_at"])
# Staging takes the same configuration input as a plan; project and environment come from the application.
draft_input = {k: v for k, v in schemas["PlanInput"]["properties"].items() if k not in ("project", "environment", "expected_revision", "env_files", "delete_service_volumes")}
schemas["DraftInput"] = obj({**draft_input, "expected_draft_revision": I}, ["expected_draft_revision"])
schemas["DraftReviewInput"] = obj({"expected_draft_revision": I}, ["expected_draft_revision"])
route("/applications/{id}/draft", "get", "getApplicationDraft", ref("Draft"))
route("/applications/{id}/draft", "put", "stageApplicationDraft", ref("Draft"), ref("DraftInput"))
route("/applications/{id}/draft", "delete", "discardApplicationDraft", obj({"discarded": B}, ["discarded"]), ref("DraftReviewInput"))
route("/applications/{id}/draft/plan", "post", "planApplicationDraft", ref("Plan"), ref("DraftReviewInput"))
route("/applications/{id}/draft/deploy", "post", "deployApplicationDraft", ref("Deployment"), obj({"expected_draft_revision": I, "expected_revision": I}, ["expected_draft_revision", "expected_revision"]), "202", idem=True)
