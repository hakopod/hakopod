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
	expires := time.Now().Add(time.Hour)
	in := BoundKeyInput{Name: "synthetic-sdk-fixture", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "deployments:write"}, ExpiresAt: &expires}
	k, raw, err := s.CreateBoundKey(ctx, p, in, "workspace-fixture", "hosted", "", false)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.Authenticate(ctx, raw)
	if err != nil || principal.IsAdmin() || principal.CredentialType != "machine" || !principal.Allows("deployments:write", "demo", "development", "") || principal.Allows("deployments:write", "other", "development", "") {
		t.Fatal("key authority", err)
	}
	if _, _, err := s.CreateBoundKey(ctx, principal, in, "workspace-fixture", "hosted", "", false); !errors.Is(err, ErrForbidden) {
		t.Fatal("machine minted another key", err)
	}
	if err := s.RevokeBoundKey(ctx, principal, "workspace-fixture", k.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("machine revoked a key", err)
	}
	bad := in
	bad.Permissions = []string{"admin"}
	if _, _, err := s.CreateBoundKey(ctx, p, bad, "workspace-fixture", "hosted", "", false); err == nil {
		t.Fatal("admin key issued")
	}
	if err := s.RevokeBoundKey(ctx, p, "different-workspace", k.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("cross-workspace revoke", err)
	}
	if _, _, err := s.CreateBoundKey(ctx, p, in, "workspace-fixture", "new-installation", k.ID, false); !errors.Is(err, ErrConflict) {
		t.Fatal("rotation changed installation", err)
	}
	replacement, nextRaw, err := s.CreateBoundKey(ctx, p, in, "workspace-fixture", "hosted", k.ID, false)
	if err != nil || replacement.ID == k.ID {
		t.Fatal("rotation failed", err)
	}
	var previousExpiry time.Time
	if err = s.Pool.QueryRow(ctx, "SELECT expires_at FROM api_keys WHERE id=$1", k.ID).Scan(&previousExpiry); err != nil || previousExpiry.After(time.Now().Add(16*time.Minute)) {
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

func TestNeverExpiringBoundKeysRequireTrustedIssuerAndRemainBound(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, "UPDATE identities SET email='automation-never@example.test',email_verified=true WHERE id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	session, err := s.NewSession(ctx, p.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p = session.User
	never := BoundKeyInput{Name: "cloud-ci", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "deployments:write"}, NeverExpires: true}
	if _, _, err = s.CreateKey(ctx, p, KeyInput{Name: never.Name, Project: never.Project, Environment: never.Environment, Permissions: never.Permissions}); err == nil {
		t.Fatal("ordinary key issuer accepted a missing expiry", err)
	}
	if _, _, err = s.CreateBoundKey(ctx, p, never, "workspace-never", "hosted", "", false); !errors.Is(err, ErrInput) {
		t.Fatal("untrusted issuer created a never-expiring key", err)
	}
	contradictory := never
	contradictoryExpiry := time.Now().Add(time.Hour)
	contradictory.ExpiresAt = &contradictoryExpiry
	if _, _, err = s.CreateBoundKey(ctx, p, contradictory, "workspace-never", "hosted", "", true); !errors.Is(err, ErrInput) {
		t.Fatal("expiry and never_expires were accepted together", err)
	}
	explicitZero := never
	zero := time.Time{}
	explicitZero.ExpiresAt = &zero
	if _, _, err = s.CreateBoundKey(ctx, p, explicitZero, "workspace-never", "hosted", "", true); !errors.Is(err, ErrInput) {
		t.Fatal("explicit zero expiry and never_expires were accepted together", err)
	}
	missing := never
	missing.NeverExpires = false
	if _, _, err = s.CreateBoundKey(ctx, p, missing, "workspace-never", "hosted", "", true); !errors.Is(err, ErrInput) {
		t.Fatal("missing expiry without opt-in was accepted", err)
	}
	k, raw, err := s.CreateBoundKey(ctx, p, never, "workspace-never", "hosted", "", true)
	if err != nil || k.ExpiresAt != nil || !k.NeverExpires {
		t.Fatal("trusted issuer did not create nullable never-expiring metadata", err, k)
	}
	if principal, err := s.Authenticate(ctx, raw); err != nil || principal.KeyID != k.ID || principal.CredentialType != "machine" {
		t.Fatal("valid bound never-expiring key was rejected", err, principal)
	}
	if principal, err := s.KeyPrincipal(ctx, k.ID); err != nil || principal.KeyID != k.ID || principal.CredentialType != "machine" {
		t.Fatal("valid bound never-expiring key principal was rejected", err, principal)
	}
	if _, _, err = s.RotateKey(ctx, p, k.ID, time.Now().Add(time.Hour)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("generic rotation accepted a Cloud-bound key", err)
	}
	replacement, replacementRaw, err := s.CreateBoundKey(ctx, p, never, "workspace-never", "hosted", k.ID, true)
	if err != nil || replacement.ExpiresAt != nil || !replacement.NeverExpires {
		t.Fatal("trusted rotation did not preserve never-expiring intent", err, replacement)
	}
	var previousExpiry time.Time
	var previousNever bool
	if err = s.Pool.QueryRow(ctx, "SELECT expires_at,never_expires FROM api_keys WHERE id=$1", k.ID).Scan(&previousExpiry, &previousNever); err != nil || previousNever || previousExpiry.After(time.Now().Add(16*time.Minute)) {
		t.Fatal("rotation did not atomically bound the previous never-expiring key", err, previousExpiry, previousNever)
	}
	unboundID, unboundRaw, unboundDigest := makeKey()
	if _, err = s.Pool.Exec(ctx, "INSERT INTO api_keys(id,identity_id,name,digest,prefix,project,environment,permissions,expires_at,never_expires) VALUES($1,$2,'unbound-never',$3,$4,'demo','development',ARRAY['deployments:read'],NULL,true)", unboundID, p.ID, unboundDigest, "hp_"+unboundID[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, unboundRaw); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("unbound never-expiring machine key authenticated", err)
	}
	if _, err = s.KeyPrincipal(ctx, unboundID); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("unbound never-expiring machine key produced a principal", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE identities SET disabled=true WHERE id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, replacementRaw); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("disabled owner retained never-expiring access", err)
	}
	if _, err = s.KeyPrincipal(ctx, replacement.ID); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("disabled owner retained a never-expiring key principal", err)
	}
}

func TestNeverExpiringBoundKeysCountTowardWorkspaceQuota(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, "UPDATE identities SET email='automation-quota@example.test',email_verified=true WHERE id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	session, err := s.NewSession(ctx, p.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p = session.User
	for i := 0; i < 50; i++ {
		id, _, digest := makeKey()
		if _, err = s.Pool.Exec(ctx, "INSERT INTO api_keys(id,identity_id,name,digest,prefix,project,environment,permissions,expires_at,never_expires) VALUES($1,$2,'quota-never',$3,$4,'demo','development',ARRAY['deployments:read'],NULL,true)", id, p.ID, digest, "hp_"+id[:8]); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Pool.Exec(ctx, "INSERT INTO automation_key_scopes(key_id,scope_id,binding) VALUES($1,'workspace-quota','hosted')", id); err != nil {
			t.Fatal(err)
		}
	}
	in := BoundKeyInput{Name: "over-quota", Project: "demo", Environment: "development", Permissions: []string{"deployments:read"}, NeverExpires: true}
	if _, _, err = s.CreateBoundKey(ctx, p, in, "workspace-quota", "hosted", "", true); !errors.Is(err, ErrBusy) {
		t.Fatal("never-expiring keys did not count toward the live-key quota", err)
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
