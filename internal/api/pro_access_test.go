package api_test

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/url"
	"testing"
)

func (h *authHarness) grantAccess(owner string, features ...string) {
	status := h.call("GET", "/license", owner, nil, 200)
	claims := testLicenseClaims(status["installation_id"].(string), int64(status["sequence"].(float64))+1)
	claims.Features = features
	h.call("PUT", "/license", owner, map[string]any{"license": testSignedLicense(h.licenseKey, claims), "expected_revision": status["revision"]}, 200)
}

func TestCustomRoleAuthorizationAndRevocation(t *testing.T) {
	h := newAuthHarness(t, nil)
	owner := h.owner()
	input := map[string]any{"name": "Deploy without logs", "permissions": []string{"deployments:read", "deployments:write"}, "expected_revision": 0}
	h.call("POST", "/roles", owner, input, 402)
	h.grantAccess(owner, "custom_roles")
	role := h.call("POST", "/roles", owner, input, 200)
	id := role["id"].(string)
	input["permissions"] = []string{"admin"}
	h.call("POST", "/roles", owner, input, 400)
	invite := h.call("POST", "/projects/demo/invites", owner, map[string]string{"email": "role@example.test", "role": id}, 201)
	link, _ := url.Parse(invite["invite_url"].(string))
	joined := h.call("POST", "/auth/invites/accept", "", map[string]string{"token": link.Query().Get("token"), "name": "Role fixture", "password": "fixture password strong"}, 200)
	token := joined["token"].(string)
	p, err := h.db.Authenticate(context.Background(), token)
	if err != nil || !p.Allows("deployments:write", "demo", "development", "") || p.Allows("logs:read", "demo", "development", "") || p.Allows("deployments:write", "other", "development", "") || p.CanManageProject("demo") {
		t.Fatal("custom authority incorrect", err)
	}
	h.call("POST", "/roles", token, input, 403)
	updated := map[string]any{"name": "Inspect only", "permissions": []string{"deployments:read"}, "expected_revision": 1}
	h.call("PUT", "/roles/"+id, owner, updated, 200)
	h.call("PUT", "/roles/"+id, owner, updated, 409)
	p, err = h.db.Authenticate(context.Background(), token)
	if err != nil || p.Allows("deployments:write", "demo", "development", "") {
		t.Fatal("stale custom authority", err)
	}
	h.grantAccess(owner)
	p, err = h.db.Authenticate(context.Background(), token)
	if err != nil || p.Allows("deployments:read", "demo", "development", "") {
		t.Fatal("expired role granted access", err)
	}
	h.call("DELETE", "/roles/"+id, owner, map[string]int{"expected_revision": 2}, 200)
	h.call("PUT", "/projects/demo/members", owner, map[string]string{"identity_id": p.ID, "role": "viewer"}, 200)
	p, err = h.db.Authenticate(context.Background(), token)
	if err != nil || !p.Allows("logs:read", "demo", "development", "") {
		t.Fatal("free fixed role regression", err)
	}
}

func TestOrganizationMFAEnforcesExistingSessionsAndSurvivesDowngrade(t *testing.T) {
	h := newAuthHarness(t, nil)
	owner := h.owner()
	h.grantAccess(owner, "team_mfa")
	// A license does not make an unverified session sufficient to enforce policy.
	h.call("PUT", "/organization/security", owner, map[string]any{"require_mfa": true, "expected_revision": 1}, 401)
	old := h.call("POST", "/auth/login", "", map[string]string{"email": "owner@example.test", "password": "correct horse battery staple"}, 200)["token"].(string)
	start := h.call("POST", "/auth/mfa/totp/start", owner, map[string]string{"password": "correct horse battery staple"}, 200)
	enabled := h.call("POST", "/auth/mfa/totp/confirm", owner, map[string]any{"challenge": start["challenge"], "code": codeFor(start["secret"].(string))}, 200)
	codes := enabled["recovery_codes"].([]any)
	h.call("PUT", "/organization/security", owner, map[string]any{"require_mfa": true, "expected_revision": 1}, 200)
	h.call("GET", "/projects", old, nil, 403)
	restricted := h.call("GET", "/me", old, nil, 200)
	if restricted["mfa_required"] != true {
		t.Fatal("restriction missing")
	}
	h.call("GET", "/auth/security", old, nil, 200)
	h.call("POST", "/auth/mfa/verify", old, map[string]any{"code": codes[0]}, 200)
	h.call("GET", "/projects", old, nil, 200)
	h.call("POST", "/auth/mfa/totp/disable", owner, map[string]any{"password": "correct horse battery staple", "code": codes[1]}, 403)
	h.grantAccess(owner)
	status := h.call("GET", "/organization/security", owner, nil, 200)
	if status["require_mfa"] != true {
		t.Fatal("downgrade weakened policy")
	}
	h.call("PUT", "/organization/security", owner, map[string]any{"require_mfa": false, "expected_revision": 2}, 200)
	h.call("POST", "/auth/mfa/totp/disable", owner, map[string]any{"password": "correct horse battery staple", "code": codes[2]}, 200)
}

func TestOrganizationMFADeviceConsentCarriesAssurance(t *testing.T) {
	h := newAuthHarness(t, nil)
	owner := h.owner()
	h.grantAccess(owner, "team_mfa")
	start := h.call("POST", "/auth/mfa/totp/start", owner, map[string]string{"password": "correct horse battery staple"}, 200)
	h.call("POST", "/auth/mfa/totp/confirm", owner, map[string]any{"challenge": start["challenge"], "code": codeFor(start["secret"].(string))}, 200)
	h.call("PUT", "/organization/security", owner, map[string]any{"require_mfa": true, "expected_revision": 1}, 200)
	device := h.call("POST", "/auth/device/start", "", map[string]any{"project": "demo", "environment": "development", "permissions": []string{"deployments:read", "deployments:write"}}, 200)
	h.call("POST", "/auth/device/approve", owner, map[string]any{"user_code": device["user_code"], "approve": true}, 200)
	cli := h.call("POST", "/auth/device/token", "", map[string]any{"device_code": device["device_code"]}, 200)
	principal, err := h.db.Authenticate(context.Background(), cli["token"].(string))
	if err != nil || !principal.MFAVerified || principal.MFARequired || !principal.Allows("deployments:write", "demo", "development", "") {
		t.Fatal("device flow lost verified scope", err)
	}
}

func TestCustomTeamRoleAndPendingInviteLoseAuthorityOnDowngrade(t *testing.T) {
	h := newAuthHarness(t, nil)
	owner := h.owner()
	h.grantAccess(owner, "custom_roles")
	team := h.call("POST", "/teams", owner, map[string]string{"name": "Role fixture"}, 201)
	teamID := team["id"].(string)
	role := h.call("POST", "/roles", owner, map[string]any{"name": "Logs only", "permissions": []string{"deployments:read", "logs:read"}, "expected_revision": 0}, 200)
	roleID := role["id"].(string)
	h.call("PUT", "/projects/demo/members", owner, map[string]string{"team_id": teamID, "role": roleID}, 200)
	invite := h.call("POST", "/teams/"+teamID+"/invites", owner, map[string]string{"email": "team-role@example.test", "role": "member"}, 201)
	link, _ := url.Parse(invite["invite_url"].(string))
	joined := h.call("POST", "/auth/invites/accept", "", map[string]string{"token": link.Query().Get("token"), "name": "Role fixture", "password": "fixture strong password"}, 200)
	session := joined["token"].(string)
	p, err := h.db.Authenticate(context.Background(), session)
	if err != nil || !p.Allows("logs:read", "demo", "development", "") || p.Allows("deployments:write", "demo", "development", "") {
		t.Fatal("team custom role widened", err)
	}
	pending := h.call("POST", "/projects/demo/invites", owner, map[string]string{"email": "pending-role@example.test", "role": roleID}, 201)
	link, _ = url.Parse(pending["invite_url"].(string))
	h.grantAccess(owner)
	p, err = h.db.Authenticate(context.Background(), session)
	if err != nil || p.Allows("logs:read", "demo", "development", "") {
		t.Fatal("team custom role survived expiry", err)
	}
	h.call("POST", "/auth/invites/accept", "", map[string]string{"token": link.Query().Get("token"), "name": "Pending fixture", "password": "fixture strong password"}, 402)
}

func TestEmbeddingMFAProtectsFinalFactorWithoutProLicense(t *testing.T) {
	h := newAuthHarness(t, nil)
	owner := h.owner()
	start := h.call("POST", "/auth/mfa/totp/start", owner, map[string]string{"password": "correct horse battery staple"}, 200)
	enabled := h.call("POST", "/auth/mfa/totp/confirm", owner, map[string]any{"challenge": start["challenge"], "code": codeFor(start["secret"].(string))}, 200)
	h.db.ExternalFactorPolicy = func(context.Context, pgx.Tx, string) (bool, error) { return true, nil }
	status := h.call("GET", "/auth/security", owner, nil, 200)
	if status["organization_mfa_required"] != true {
		t.Fatal("embedding policy not disclosed")
	}
	h.call("POST", "/auth/mfa/totp/disable", owner, map[string]any{"password": "correct horse battery staple", "code": enabled["recovery_codes"].([]any)[0]}, 403)
}
