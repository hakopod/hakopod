package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestManagedPlatformScopeRequiresExistingAuthorizedEnvironment(t *testing.T) {
	db := isolatedDatabase(t)
	principal := bootstrapPrincipal(t, db)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('catalog-scope'); INSERT INTO environments(project,name) VALUES('catalog-scope','development')"); err != nil {
		t.Fatal(err)
	}
	if err := db.ValidateManagedPlatformScope(ctx, principal, "catalog-scope", "development"); err != nil {
		t.Fatal(err)
	}
	if err := db.ValidateManagedPlatformScope(ctx, principal, "catalog-scope", "missing"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("missing environment accepted", err)
	}
	principal.Project = "foreign"
	if err := db.ValidateManagedPlatformScope(ctx, principal, "catalog-scope", "development"); !errors.Is(err, ErrForbidden) {
		t.Fatal("foreign scope accepted", err)
	}
}
