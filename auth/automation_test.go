package auth

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestAutomationKeysCannotBecomeBrowserOrLegacyCLI(t *testing.T) {
	dsn := os.Getenv("HAKOPOD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	name := "hakopod_automation_test_" + store.NewID()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	s, err := Open(ctx, parsed.String(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := store.NewID()
	if _, err = s.Pool().Exec(ctx, "INSERT INTO identities(id,name,email,email_verified) VALUES($1,'Automation fixture','automation@example.test',true)", id); err != nil {
		t.Fatal(err)
	}
	browser, err := s.store.NewSession(ctx, id, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.Verify(ctx, browser.Token)
	if err != nil {
		t.Fatal(err)
	}
	g := AutomationGrant{Workspace: store.NewID(), Binding: "node:fixture", Project: "customer", Environment: "production", Permissions: []string{"deployments:read", "deployments:write"}}
	expires := time.Now().Add(time.Hour)
	in := AutomationKeyInput{Name: "sdk-fixture", Project: g.Project, Environment: g.Environment, Permissions: g.Permissions, ExpiresAt: &expires}
	k, raw, err := s.CreateAutomationKey(ctx, u, g, in, "")
	if err != nil {
		t.Fatal(err)
	}
	account, bound, err := s.VerifyAutomation(ctx, raw)
	if err != nil || account.ID != u.ID || !account.Verified || bound.Workspace != g.Workspace || bound.Binding != g.Binding {
		t.Fatal("invalid automation identity", err)
	}
	if _, err = s.Verify(ctx, raw); err == nil {
		t.Fatal("machine accepted as browser")
	}
	if _, _, err = s.VerifyCLI(ctx, raw); err == nil {
		t.Fatal("machine accepted as CLI")
	}
	if _, _, err = s.VerifyAutomation(ctx, browser.Token); err == nil {
		t.Fatal("browser accepted as automation")
	}
	bad := in
	bad.Permissions = []string{"deployments:read", "applications:manage"}
	if _, _, err = s.CreateAutomationKey(ctx, u, g, bad, ""); err == nil {
		t.Fatal("grant exceeded")
	}
	never := in
	never.ExpiresAt = nil
	never.NeverExpires = true
	if _, _, err = s.CreateAutomationKey(ctx, u, g, never, ""); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("non-Cloud grant issued a never-expiring key", err)
	}
	g.AllowNeverExpires = true
	neverKey, neverRaw, err := s.CreateAutomationKey(ctx, u, g, never, "")
	if err != nil || neverKey.ExpiresAt != nil || !neverKey.NeverExpires {
		t.Fatal("trusted Cloud grant did not issue nullable never-expiring metadata", err, neverKey)
	}
	if _, _, err = s.VerifyAutomation(ctx, neverRaw); err != nil {
		t.Fatal("bound never-expiring key was not accepted", err)
	}
	if err = s.RevokeAutomationKey(ctx, u, g.Workspace, neverKey.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.VerifyAutomation(ctx, neverRaw); err == nil {
		t.Fatal("revoked never-expiring key accepted")
	}
	if _, _, err = s.CreateAutomationKey(ctx, account, g, in, ""); err == nil {
		t.Fatal("machine minted key")
	}
	if err = s.RevokeAutomationKey(ctx, u, g.Workspace, k.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.VerifyAutomation(ctx, raw); err == nil {
		t.Fatal("revoked key accepted")
	}
}
