package api_test

import (
	"context"
	"crypto/sha256"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLicensePaidGatesDowngradeAndRecovery(t *testing.T) {
	h := newAuthHarness(t, nil)
	ctx := context.Background()
	if _, err := h.db.Pool.Exec(ctx, "UPDATE installation_license SET token='',token_digest=NULL,highest_sequence=0,revision=0 WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	owner := h.owner()
	status := h.call("GET", "/license", owner, nil, 200)
	installation := status["installation_id"].(string)
	if status["plan"] != "free" || status["valid"] != false {
		t.Fatal("empty installation was not Free")
	}
	h.call("POST", "/teams", owner, map[string]string{"name": "Must be licensed"}, 402)
	h.call("POST", "/projects/demo/invites", owner, map[string]string{"email": "blocked@example.test", "role": "developer"}, 402)
	claims := testLicenseClaims(installation, 1)
	pro := testSignedLicense(h.licenseKey, claims)
	h.call("PUT", "/license", owner, map[string]any{"license": pro, "expected_revision": 0}, 200)
	team := h.call("POST", "/teams", owner, map[string]string{"name": "Licensed team"}, 201)
	teamID := team["id"].(string)
	invite := h.call("POST", "/teams/"+teamID+"/invites", owner, map[string]string{"email": "licensed-member@example.test", "role": "developer", "project": "demo"}, 201)
	u, _ := url.Parse(invite["invite_url"].(string))
	accepted := h.call("POST", "/auth/invites/accept", "", map[string]string{"token": u.Query().Get("token"), "name": "Licensed Member", "password": "licensed member password"}, 200)
	memberToken := accepted["token"].(string)
	memberID := accepted["user"].(map[string]any)["id"].(string)
	h.call("PUT", "/projects/demo/members", owner, map[string]string{"identity_id": memberID, "role": ""}, 200)
	h.call("PUT", "/projects/demo/members", owner, map[string]string{"team_id": teamID, "role": "developer"}, 200)
	principal, err := h.db.Authenticate(ctx, memberToken)
	if err != nil || !principal.Allows("deployments:write", "demo", "development", "test") {
		t.Fatal("licensed team-derived authorization missing")
	}
	if err = h.db.Reauthorize(ctx, principal.KeyID, "demo", "development", "test"); err != nil {
		t.Fatal(err)
	}
	pending := h.call("POST", "/teams/"+teamID+"/invites", owner, map[string]string{"email": "pending-member@example.test", "role": "member"}, 201)
	pendingURL, _ := url.Parse(pending["invite_url"].(string))
	downgrade := claims
	downgrade.Plan = "free"
	downgrade.Features = nil
	downgrade.Sequence = 2
	free := h.call("PUT", "/license", owner, map[string]any{"license": testSignedLicense(h.licenseKey, downgrade), "expected_revision": 1}, 200)
	if free["plan"] != "free" {
		t.Fatal("signed downgrade retained Pro")
	}
	h.call("POST", "/teams", owner, map[string]string{"name": "Blocked after downgrade"}, 402)
	h.call("POST", "/teams/"+teamID+"/invites", owner, map[string]string{"email": "denied@example.test", "role": "member"}, 402)
	h.call("PUT", "/teams/"+teamID+"/members/"+memberID, owner, map[string]string{"role": "admin"}, 402)
	h.call("PUT", "/projects/demo/members", owner, map[string]string{"identity_id": memberID, "role": "developer"}, 402)
	h.call("PUT", "/projects/demo/members", owner, map[string]string{"team_id": teamID, "role": "developer"}, 402)
	h.call("POST", "/auth/invites/accept", "", map[string]string{"token": pendingURL.Query().Get("token"), "name": "Pending Member", "password": "pending member password"}, 402)
	current, err := h.db.Authenticate(ctx, memberToken)
	if err != nil || current.Allows("deployments:write", "demo", "development", "test") {
		t.Fatal("downgrade retained team authority or blocked ordinary authentication")
	}
	if err = h.db.Reauthorize(ctx, principal.KeyID, "demo", "development", "test"); err == nil {
		t.Fatal("durable work retained paid authorization after downgrade")
	}
	h.call("PUT", "/license", owner, map[string]any{"license": pro, "expected_revision": 2}, 409)
	for _, kind := range []string{"wrong installation", "expired", "tampered"} {
		bad := claims
		bad.Sequence = 3
		if kind == "wrong installation" {
			bad.InstallationID = strings.Repeat("f", 32)
		}
		if kind == "expired" {
			bad.IssuedAt = time.Now().Add(-2 * time.Hour).Unix()
			bad.NotBefore = bad.IssuedAt
			bad.ExpiresAt = time.Now().Add(-time.Minute).Unix()
		}
		raw := testSignedLicense(h.licenseKey, bad)
		if kind == "tampered" {
			raw = raw[:len(raw)-8] + "AAAAAAAA"
		}
		h.call("PUT", "/license", owner, map[string]any{"license": raw, "expected_revision": 2}, 400)
	}
	// Safe removal of existing grants remains available while paid features lock.
	h.call("PUT", "/teams/"+teamID+"/members/"+memberID, owner, map[string]string{"role": ""}, 200)
	h.call("PUT", "/projects/demo/members", owner, map[string]string{"team_id": teamID, "role": ""}, 200)
	h.call("GET", "/auth/security", owner, nil, 200)
	if h.call("GET", "/me", owner, nil, 200)["admin"] != true {
		t.Fatal("license downgrade disabled recovery administrator")
	}
	claims.Sequence = 3
	renewal := testSignedLicense(h.licenseKey, claims)
	h.call("PUT", "/license", owner, map[string]any{"license": renewal, "expected_revision": 2}, 200)
	// Seed the correctly signed expired state to exercise time expiry without a
	// wall-clock sleep or trusting a database entitlement flag.
	expired := claims
	expired.IssuedAt = time.Now().Add(-2 * time.Hour).Unix()
	expired.NotBefore = expired.IssuedAt
	expired.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	expiredRaw := testSignedLicense(h.licenseKey, expired)
	digest := sha256.Sum256([]byte(expiredRaw))
	if _, err = h.db.Pool.Exec(ctx, "UPDATE installation_license SET token=$1,token_digest=$2 WHERE singleton", expiredRaw, digest[:]); err != nil {
		t.Fatal(err)
	}
	if h.call("GET", "/license", owner, nil, 200)["state"] != "expired" {
		t.Fatal("stored signed expiry was not enforced")
	}
	h.call("POST", "/teams", owner, map[string]string{"name": "Expired blocked"}, 402)
	h.call("DELETE", "/teams/"+teamID, owner, nil, 200)
	h.call("DELETE", "/license", owner, map[string]int64{"expected_revision": 3}, 200)
	h.call("PUT", "/license", owner, map[string]any{"license": renewal, "expected_revision": 4}, 409)
	claims.Sequence = 4
	h.call("PUT", "/license", owner, map[string]any{"license": testSignedLicense(h.licenseKey, claims), "expected_revision": 4}, 200)
	t.Log("real PostgreSQL: Free gates, paid teams/invites/role grants, signed downgrade, active-session and worker reauthorization, tamper/wrong installation/expiry, anti-replay sequence and recovery cleanup passed")
}
