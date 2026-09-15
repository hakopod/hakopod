package store

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/spec"
	"testing"
	"time"
)

func TestScopedOwnerDeletesOnlyItsEmptyApplication(t *testing.T) {
	db := isolatedDatabase(t)
	admin := bootstrapPrincipal(t, db)
	ctx := context.Background()
	_, key, err := db.CreateKey(ctx, admin, KeyInput{Name: "owner", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "deployments:write", "applications:manage"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.Authenticate(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Accept(ctx, admin, "demo", "development", emptyTestSpec(), 0, "scoped-delete")
	if err != nil {
		t.Fatal(err)
	}
	empty := d.Spec
	empty.Services = map[string]spec.Service{}
	if _, err = db.Pool.Exec(ctx, "UPDATE applications SET spec=$2 WHERE id=$1", d.ApplicationID, JSON(empty)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET spec=$2,status='succeeded' WHERE id=$1", d.ID, JSON(empty)); err != nil {
		t.Fatal(err)
	}
	foreign := owner
	foreign.Environment = "production"
	if err = db.DeleteEmptyApplication(ctx, foreign, d.ApplicationID, 1, empty.Name); !errors.Is(err, ErrForbidden) {
		t.Fatal("cross-environment deletion", err)
	}
	if err = db.DeleteEmptyApplication(ctx, owner, d.ApplicationID, 1, empty.Name); err != nil {
		t.Fatal("scoped owner cannot delete empty app", err)
	}
}
