package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

func TestSelfHostedAdministratorCanCreateANeverExpiringKey(t *testing.T) {
	h := newAuthHarness(t, nil)
	ctx := context.Background()
	owner := h.owner()
	created := h.call("POST", "/keys", owner, map[string]any{"name": "ci", "permissions": []string{"deployments:read"}, "never_expires": true}, 201)
	metadata := created["metadata"].(map[string]any)
	if metadata["never_expires"] != true || metadata["expires_at"] != nil {
		t.Fatalf("metadata = %v", metadata)
	}
	key := created["key"].(string)
	// It authenticates, also long after any bounded key would have expired.
	if _, err := h.db.Pool.Exec(ctx, "UPDATE api_keys SET created_at=now()-interval '400 days' WHERE id=$1", metadata["id"]); err != nil {
		t.Fatal(err)
	}
	h.call("GET", "/me", key, nil, 200)
	// Rotation keeps the choice and gives the old key a short overlap.
	rotated := h.call("POST", "/keys/"+metadata["id"].(string)+"/rotate", owner, map[string]any{"never_expires": true}, 201)
	if rotated["metadata"].(map[string]any)["never_expires"] != true {
		t.Fatalf("rotated = %v", rotated)
	}
	var oldExpiry time.Time
	if err := h.db.Pool.QueryRow(ctx, "SELECT expires_at FROM api_keys WHERE id=$1", metadata["id"]).Scan(&oldExpiry); err != nil || time.Until(oldExpiry) > 16*time.Minute {
		t.Fatalf("old key expiry = %v %v", oldExpiry, err)
	}

	// A CLI session or an API key cannot mint one, even an administrator's.
	p, err := h.db.Authenticate(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := h.db.NewSession(ctx, p.ID, "cli", "", "", []string{"admin", "agent:admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = h.db.CreateKey(ctx, store.Principal{ID: p.ID, Admin: true, CredentialType: "cli"}, store.KeyInput{Name: "cli", Permissions: []string{"deployments:read"}, NeverExpires: true}); err == nil {
		t.Fatal("a CLI principal created a never-expiring key")
	}
	h.call("POST", "/keys", cli.Token, map[string]any{"name": "cli", "permissions": []string{"deployments:read"}, "never_expires": true}, 403)
	// Both modes at once are refused.
	h.call("POST", "/keys", owner, map[string]any{"name": "both", "permissions": []string{"deployments:read"}, "never_expires": true, "expires_at": time.Now().Add(time.Hour)}, 400)

	// Managed Cloud keeps never-expiring keys to its automation issuer.
	h.db.ManagedCloud = true
	defer func() { h.db.ManagedCloud = false }()
	if _, _, err = h.db.CreateKey(ctx, p, store.KeyInput{Name: "cloud", Permissions: []string{"deployments:read"}, NeverExpires: true}); err == nil {
		t.Fatal("a Cloud installation created a never-expiring key outside the issuer")
	}
}
