package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

// accessCall sends a request as the Node dashboard does: from loopback, naming
// the browser's peer address. peer "" sends no peer header (a local process).
func (h *authHarness) accessCall(method, path, token, peer string, body any) (int, map[string]any) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(store.JSON(body))
	}
	req, _ := http.NewRequest(method, h.server.URL+path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if peer != "" {
		req.Header.Set("X-Hakopod-Peer-IP", peer)
	}
	client := h.server.Client()
	client.Timeout = 10 * time.Second
	response, err := client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer response.Body.Close()
	var result map[string]any
	_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result)
	return response.StatusCode, result
}

func grantPrivateAccess(t *testing.T, h *authHarness) {
	t.Helper()
	ctx := context.Background()
	status, err := h.db.LicenseStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claims := testLicenseClaims(status.InstallationID, 2)
	claims.Features = append(claims.Features, "private_access")
	token := testSignedLicense(h.licenseKey, claims)
	digest := sha256.Sum256([]byte(token))
	if _, err = h.db.Pool.Exec(ctx, "UPDATE installation_license SET token=$1,token_digest=$2,highest_sequence=2,revision=revision+1 WHERE singleton", token, digest[:]); err != nil {
		t.Fatal(err)
	}
}

func TestInstallationAccessPolicyGatesConfirmsRevertsAndResets(t *testing.T) {
	h := newAuthHarness(t, nil)
	ctx := context.Background()
	owner := h.owner()
	const office, home = "203.0.113.20", "198.51.100.9"
	policy := map[string]any{"enabled": true, "client_ip_source": "connection", "trusted_proxy_cidrs": []string{}, "client_ip_header": "", "allow_cidrs": []string{"203.0.113.0/24"}}
	save := map[string]any{"expected_revision": 0, "policy": policy}

	// Off by default: every network passes and the owner sees their address.
	if code, view := h.accessCall("GET", "/api/v1/installation/access", owner, home, nil); code != 200 || view["client_ip"] != home || view["licensed"] != false {
		t.Fatalf("initial view %d %v", code, view)
	}
	// Enabling needs the Pro entitlement.
	if code, _ := h.accessCall("PUT", "/api/v1/installation/access", owner, office, save); code != 402 {
		t.Fatalf("unlicensed enable = %d", code)
	}
	grantPrivateAccess(t, h)
	// A save that would block the owner's own network is refused.
	if code, body := h.accessCall("PUT", "/api/v1/installation/access", owner, home, save); code != 409 || body["error"].(map[string]any)["code"] != "would_lock_out" {
		t.Fatalf("self-lockout save = %d %v", code, body)
	}
	code, saved := h.accessCall("PUT", "/api/v1/installation/access", owner, office, save)
	if code != 200 || saved["revision"] != float64(1) || saved["pending_until"] == nil {
		t.Fatalf("save = %d %v", code, saved)
	}

	// The gate runs before authentication and covers public auth routes.
	if code, _ := h.accessCall("GET", "/api/v1/auth/status", "", home, nil); code != 403 {
		t.Fatalf("denied network reached auth status: %d", code)
	}
	if code, _ := h.accessCall("GET", "/internal/access/check", "", home, nil); code != 403 {
		t.Fatalf("check for denied network = %d", code)
	}
	if code, _ := h.accessCall("GET", "/internal/access/check", "", office, nil); code != 204 {
		t.Fatalf("check for allowed network = %d", code)
	}
	// Loopback without a named peer is a local process; health stays open.
	if code, _ := h.accessCall("GET", "/api/v1/auth/status", "", "", nil); code != 200 {
		t.Fatalf("local process denied: %d", code)
	}
	if code, _ := h.accessCall("GET", "/healthz", "", home, nil); code != 200 {
		t.Fatalf("health check denied: %d", code)
	}
	// Signed webhooks are exempt from the network gate; the signature check
	// still rejects this unsigned body.
	if code, _ := h.accessCall("POST", "/api/v1/webhooks/github", "", home, map[string]any{}); code == 403 {
		t.Fatal("webhook was blocked by the network gate")
	}
	// A malformed named peer fails closed.
	if code, _ := h.accessCall("GET", "/internal/access/check", "", "not-an-ip", nil); code != 403 {
		t.Fatalf("malformed peer = %d", code)
	}

	// Confirming from an allowed network keeps the policy.
	if code, body := h.accessCall("POST", "/api/v1/installation/access/confirm", owner, office, map[string]any{"revision": 1}); code != 200 || body["pending_until"] != nil {
		t.Fatalf("confirm = %d %v", code, body)
	}

	// An unconfirmed narrowing reverts to the confirmed policy.
	narrower := map[string]any{"enabled": true, "client_ip_source": "connection", "trusted_proxy_cidrs": []string{}, "client_ip_header": "", "allow_cidrs": []string{"192.0.2.0/24"}}
	if code, body := h.accessCall("PUT", "/api/v1/installation/access", owner, office, map[string]any{"expected_revision": 1, "policy": narrower, "confirm_from_elsewhere": true}); code != 200 {
		t.Fatalf("narrowing save = %d %v", code, body)
	}
	if _, err := h.db.Pool.Exec(ctx, "UPDATE installation_access_policy SET pending_until=now()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	record, err := h.db.AccessPolicy(ctx)
	if err != nil || record.PendingUntil != nil || !bytes.Contains(record.Policy, []byte("203.0.113.0/24")) || record.Revision != 3 {
		t.Fatalf("expired policy did not revert: %+v %v", record, err)
	}

	// Break-glass turns the policy off without a session or license.
	record, err = h.db.ResetAccessPolicy(ctx, "operator")
	var reset struct{ Enabled bool }
	if err != nil || json.Unmarshal(record.Policy, &reset) != nil || reset.Enabled {
		t.Fatalf("reset = %+v %v", record, err)
	}
	var actions []string
	rows, _ := h.db.Pool.Query(ctx, "SELECT action FROM audit_events WHERE resource='installation-access' ORDER BY id")
	for rows.Next() {
		var action string
		_ = rows.Scan(&action)
		actions = append(actions, action)
	}
	rows.Close()
	want := []string{"installation.access.updated", "installation.access.confirmed", "installation.access.updated", "installation.access.reverted", "installation.access.reset"}
	if len(actions) != len(want) {
		t.Fatalf("audit = %v", actions)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Fatalf("audit = %v", actions)
		}
	}
}

func TestInstallationAccessPolicyRequiresBrowserOwner(t *testing.T) {
	h := newAuthHarness(t, nil)
	ctx := context.Background()
	owner := h.owner()
	p, err := h.db.Authenticate(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := h.db.NewSession(ctx, p.ID, "cli", "demo", "development", []string{"admin"})
	if err != nil {
		t.Fatal(err)
	}
	_, machine, err := h.db.CreateKey(ctx, p, store.KeyInput{Name: "access-test-machine", Permissions: []string{"admin"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"expected_revision": 0, "policy": map[string]any{"enabled": false, "client_ip_source": "connection", "trusted_proxy_cidrs": []string{}, "client_ip_header": "", "allow_cidrs": []string{}}}
	for _, credential := range []string{cli.Token, machine} {
		if code, _ := h.accessCall("PUT", "/api/v1/installation/access", credential, "", body); code != 403 {
			t.Fatalf("non-browser credential changed access policy: %d", code)
		}
	}
	if code, _ := h.accessCall("PUT", "/api/v1/installation/access", owner, "", body); code != 200 {
		t.Fatalf("browser owner could not save a disabled policy: %d", code)
	}
}
