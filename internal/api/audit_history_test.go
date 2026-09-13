package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestUserAuditHistoryRequiresExplicitEntitlement(t *testing.T) {
	h := newAuthHarness(t, nil)
	ctx := context.Background()
	owner := h.owner()
	user := h.call("GET", "/me", owner, nil, 200)["id"].(string)
	endpoint := "/audit/history?identity_id=" + user
	// Legacy Pro tokens cover only the now-Free capabilities.
	h.call("GET", endpoint, owner, nil, 402)
	h.call("GET", "/audit/export?identity_id="+user, owner, nil, 402)
	h.call("GET", "/audit", owner, nil, 200)
	status := h.call("GET", "/license", owner, nil, 200)
	claims := testLicenseClaims(status["installation_id"].(string), 2)
	claims.Features = []string{"audit_history"}
	h.call("PUT", "/license", owner, map[string]any{"license": testSignedLicense(h.licenseKey, claims), "expected_revision": status["revision"]}, 200)
	// Fixed project roles never accept a custom or installation-level role name.
	h.call("PUT", "/projects/demo/members", owner, map[string]string{"identity_id": user, "role": "custom-admin"}, 400)
	for i := 0; i < 1005; i++ {
		if _, err := h.db.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,action,resource) VALUES($1,'fixture.user.event',$2)", user, fmt.Sprintf("=untrusted-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	h.call("GET", "/audit/history?identity_id=invalid", owner, nil, 400)
	first := h.call("GET", endpoint, owner, nil, 200)
	if len(first["items"].([]any)) != 100 || first["next_cursor"].(float64) <= 0 {
		t.Fatal("history is not bounded or paginated")
	}
	second := h.call("GET", endpoint+"&before="+strconv.FormatInt(int64(first["next_cursor"].(float64)), 10), owner, nil, 200)
	if second["items"].([]any)[0].(map[string]any)["id"].(float64) >= first["next_cursor"].(float64) {
		t.Fatal("history cursor overlaps")
	}
	req, _ := http.NewRequest("GET", h.server.URL+"/api/v1/audit/export?identity_id="+user, nil)
	req.Header.Set("Authorization", "Bearer "+owner)
	response, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	records, err := csv.NewReader(response.Body).ReadAll()
	if err != nil || response.StatusCode != 200 || len(records) != 1001 || response.Header.Get("X-Hakopod-Next-Cursor") == "" {
		t.Fatal("CSV export was not bounded", err)
	}
	if !strings.HasPrefix(records[1][4], "'=") {
		t.Fatal("CSV formula was not escaped")
	}
	// Ordinary members cannot read paid administrator history even on active Pro.
	invite := h.call("POST", "/projects/demo/invites", owner, map[string]string{"email": "viewer@example.test", "role": "viewer"}, 201)
	link, _ := url.Parse(invite["invite_url"].(string))
	accepted := h.call("POST", "/auth/invites/accept", "", map[string]string{"token": link.Query().Get("token"), "name": "Viewer", "password": "viewer password strong"}, 200)
	h.call("GET", endpoint, accepted["token"].(string), nil, 403)
	// Correct signatures alone cannot revive expired authority.
	claims.IssuedAt = time.Now().Add(-2 * time.Hour).Unix()
	claims.NotBefore = claims.IssuedAt
	claims.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	raw := testSignedLicense(h.licenseKey, claims)
	digest := sha256.Sum256([]byte(raw))
	if _, err = h.db.Pool.Exec(ctx, "UPDATE installation_license SET token=$1,token_digest=$2 WHERE singleton", raw, digest[:]); err != nil {
		t.Fatal(err)
	}
	h.call("GET", endpoint, owner, nil, 402)
	h.call("GET", "/audit/export?identity_id="+user, owner, nil, 402)
	core := h.call("GET", "/audit", owner, nil, 200)
	if len(core["items"].([]any)) != 100 {
		t.Fatal("expiry suppressed core security records")
	}
	h.call("POST", "/teams", owner, map[string]string{"name": "Still Free"}, 201)
}
