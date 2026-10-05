package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
)

func TestBackupWorkerReauthorizesScopedAdministratorKey(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "backup-authority")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := db.CreateKey(ctx, admin, KeyInput{
		Name:        "scoped-backup",
		Project:     "demo",
		Environment: "development",
		Permissions: []string{"deployments:write"},
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	authority := &backup.Authority{Project: "demo", Environment: "development"}

	principal, err := db.backupWorkerPrincipal(ctx, admin.ID, key.ID, authority)
	if err != nil || !principal.CanManageBackups() || principal.IsAdmin() {
		t.Fatal("a live scoped administrator key lost its bounded durable authority", err)
	}
	if _, err = db.backupWorkerPrincipal(ctx, admin.ID, key.ID, &backup.Authority{Project: "other", Environment: "development"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("a scoped key crossed its recorded project authority", err)
	}
	if _, err = db.backupWorkerPrincipal(ctx, admin.ID, "", authority); !errors.Is(err, ErrForbidden) {
		t.Fatal("a keyless schedule inherited global administrator authority", err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.backupWorkerPrincipal(ctx, admin.ID, key.ID, authority); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("a revoked scoped key retained durable authority", err)
	}
}

func TestBackupWorkerReauthorizesRuntimeScopedHumanSession(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Bootstrap(ctx, "backup-authority"); err != nil {
		t.Fatal(err)
	}
	identity := NewID()
	if _, err := db.Pool.Exec(ctx, "INSERT INTO identities(id,name,email) VALUES($1,'member','member@example.test')", identity); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "INSERT INTO personal_workspaces(identity_id,project) VALUES($1,'demo')", identity); err != nil {
		t.Fatal(err)
	}
	browser, err := db.NewSession(ctx, identity, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := db.NewSession(ctx, identity, "cli", "demo", "development", []string{"deployments:write"})
	if err != nil {
		t.Fatal(err)
	}
	authority := &backup.Authority{Project: "demo", Environment: "development"}
	for name, key := range map[string]string{"browser": browser.User.KeyID, "cli": cli.User.KeyID} {
		if principal, err := db.backupWorkerPrincipal(ctx, identity, key, authority); err != nil || !principal.CanManageBackups() || principal.Admin {
			t.Fatalf("a runtime-scoped %s project member lost durable authority: %v", name, err)
		}
	}
	if _, err = db.Pool.Exec(ctx, "DELETE FROM personal_workspaces WHERE project='demo' AND identity_id=$1", identity); err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]string{"browser": browser.User.KeyID, "cli": cli.User.KeyID} {
		if _, err = db.backupWorkerPrincipal(ctx, identity, key, authority); !errors.Is(err, ErrForbidden) {
			t.Fatalf("a %s session retained durable authority after membership revocation: %v", name, err)
		}
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE identities SET disabled=true WHERE id=$1", identity); err != nil {
		t.Fatal(err)
	}
	if _, err = db.backupWorkerPrincipal(ctx, identity, browser.User.KeyID, authority); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("a disabled human identity retained durable authority", err)
	}
}
