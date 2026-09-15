package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestScopedRenamesPreserveWorkloadIdentity(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "rename")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := db.CreateKey(ctx, admin, KeyInput{Name: "owner", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "deployments:write", "applications:manage"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Accept(ctx, admin, "demo", "development", emptyTestSpec(), 0, "rename-resource-test")
	if err != nil {
		t.Fatal(err)
	}
	a, err := db.RenameApplication(ctx, p, d.ApplicationID, "", "My API", 1)
	if err != nil {
		t.Fatal(err)
	}
	a, err = db.RenameApplication(ctx, p, a.ID, "api", "Public API", 2)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "store-test" || a.Spec.Name != "store-test" || a.Revision != 1 || a.ServiceDisplayNames["api"] != "Public API" || a.Spec.Services["api"].Image == "" {
		t.Fatal("rename changed workload identity")
	}
	if _, err = db.RenameApplication(ctx, p, a.ID, "", "Lost update", 1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale metadata accepted", err)
	}
	p.Permissions = []string{"deployments:read", "deployments:write"}
	if _, err = db.RenameApplication(ctx, p, a.ID, "", "Not owner", 3); !errors.Is(err, ErrForbidden) {
		t.Fatal("deployer renamed", err)
	}
	p, _ = db.Authenticate(ctx, key)
	p.Environment = "production"
	if _, err = db.RenameApplication(ctx, p, a.ID, "", "Wrong environment", 3); !errors.Is(err, ErrForbidden) {
		t.Fatal("cross-environment rename", err)
	}
	p, _ = db.Authenticate(ctx, key)
	if err = db.RenameProject(ctx, p, "demo", "My project", 1); err != nil {
		t.Fatal(err)
	}
	if err = db.RenameProject(ctx, p, "demo", "Stale", 1); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
