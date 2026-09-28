package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestBoundKeysAreExplicitRevocableAndRotatedAtomically(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, "UPDATE identities SET email='automation@example.test',email_verified=true WHERE id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	session, err := s.NewSession(ctx, p.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p = session.User
	in := KeyInput{Name: "synthetic-sdk-fixture", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "deployments:write"}, ExpiresAt: time.Now().Add(time.Hour)}
	k, raw, err := s.CreateBoundKey(ctx, p, in, "workspace-fixture", "hosted", "")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.Authenticate(ctx, raw)
	if err != nil || principal.IsAdmin() || principal.CredentialType != "machine" || !principal.Allows("deployments:write", "demo", "development", "") || principal.Allows("deployments:write", "other", "development", "") {
		t.Fatal("key authority", err)
	}
	if _, _, err := s.CreateBoundKey(ctx, principal, in, "workspace-fixture", "hosted", ""); !errors.Is(err, ErrForbidden) {
		t.Fatal("machine minted another key", err)
	}
	if err := s.RevokeBoundKey(ctx, principal, "workspace-fixture", k.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("machine revoked a key", err)
	}
	bad := in
	bad.Permissions = []string{"admin"}
	if _, _, err := s.CreateBoundKey(ctx, p, bad, "workspace-fixture", "hosted", ""); err == nil {
		t.Fatal("admin key issued")
	}
	if err := s.RevokeBoundKey(ctx, p, "different-workspace", k.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("cross-workspace revoke", err)
	}
	if _, _, err := s.CreateBoundKey(ctx, p, in, "workspace-fixture", "new-installation", k.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("rotation changed installation", err)
	}
	replacement, nextRaw, err := s.CreateBoundKey(ctx, p, in, "workspace-fixture", "hosted", k.ID)
	if err != nil || replacement.ID == k.ID {
		t.Fatal("rotation failed", err)
	}
	var expires time.Time
	if err = s.Pool.QueryRow(ctx, "SELECT expires_at FROM api_keys WHERE id=$1", k.ID).Scan(&expires); err != nil || expires.After(time.Now().Add(16*time.Minute)) {
		t.Fatal("rotation overlap unbounded", err)
	}
	if err = s.RevokeBoundKey(ctx, p, "workspace-fixture", replacement.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, nextRaw); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked key accepted", err)
	}
	list, err := s.BoundKeys(ctx, "different-workspace")
	if err != nil || len(list) != 0 {
		t.Fatal("keys crossed workspace", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE identities SET disabled=true WHERE id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, raw); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("disabled identity retained access", err)
	}
}

func TestMachineManagementRequiresBothKeyGrantAndCurrentProjectAdmin(t *testing.T) {
	p := Principal{ID: "owner", Email: "owner@example.test", CredentialType: "machine", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "deployments:write", "networks:write", "applications:manage", "git:manage"}, ProjectRoles: []ProjectRole{{Project: "demo", Role: "admin"}}}
	if !p.CanManageVirtualNetworks("demo", "development") || !p.CanManageApplication("demo", "development", "app") || !p.CanManageGit() || p.IsAdmin() {
		t.Fatal("incorrect project management authority")
	}
	p.ProjectRoles[0].Role = "developer"
	if p.CanManageVirtualNetworks("demo", "development") || p.CanManageApplication("demo", "development", "app") || p.CanManageGit() {
		t.Fatal("role downgrade retained management authority")
	}
}
