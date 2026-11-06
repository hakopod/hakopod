package store

import (
	"context"
	"errors"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
)

func patchedTestSpec(t *testing.T, deployment string) spec.Application {
	t.Helper()
	app := emptyTestSpec()
	svc := app.Services["api"]
	svc.Kubernetes = &spec.KubernetesPatches{Deployment: deployment}
	app.Services["api"] = svc
	app, err := spec.Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestKubernetesPatchGate(t *testing.T) {
	plain := emptyTestSpec()
	patched := patchedTestSpec(t, "metadata:\n  labels:\n    team: shop\n")
	changed := patchedTestSpec(t, "metadata:\n  labels:\n    team: billing\n")
	browserAdmin := Principal{ID: "admin", Admin: true, CredentialType: "browser", Permissions: []string{"admin"}}
	cliAdmin := Principal{ID: "admin", Admin: true, CredentialType: "cli", Permissions: []string{"admin"}}
	cliGranted := Principal{ID: "admin", Admin: true, CredentialType: "cli", Permissions: []string{"deployments:read", "deployments:write", "kubernetes:patch"}}
	developer := Principal{ID: "dev", Email: "dev@example.test", CredentialType: "browser", Permissions: []string{"admin"}, ProjectRoles: []ProjectRole{{Project: "demo", Role: "developer"}}}
	projectAdmin := Principal{ID: "lead", Email: "lead@example.test", CredentialType: "browser", Permissions: []string{"admin"}, ProjectRoles: []ProjectRole{{Project: "demo", Role: "admin"}}}
	cases := []struct {
		name           string
		p              Principal
		before, after  spec.Application
		enabled        bool
		disabled, deny bool
	}{
		{"no patches", developer, plain, plain, false, false, false},
		{"unchanged patches redeploy while off", developer, patched, patched, false, false, false},
		{"removing patches while off", developer, patched, plain, false, false, false},
		{"adding while off", browserAdmin, plain, patched, false, true, false},
		{"adding as installation admin", browserAdmin, plain, patched, true, false, false},
		{"changing as project admin", projectAdmin, patched, changed, true, false, false},
		{"developer lacks the permission", developer, plain, patched, true, false, true},
		{"cli admin wildcard is not a grant", cliAdmin, plain, patched, true, false, true},
		{"cli explicit grant", cliGranted, patched, changed, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := kubernetesPatchGate(tc.p, "demo", "development", tc.before, tc.after, tc.enabled)
			switch {
			case tc.disabled:
				if !errors.Is(err, ErrKubernetesPatchesDisabled) {
					t.Fatalf("want the disabled error, got %v", err)
				}
			case tc.deny:
				if !errors.Is(err, ErrForbidden) {
					t.Fatalf("want forbidden, got %v", err)
				}
			case err != nil:
				t.Fatalf("want accepted, got %v", err)
			}
		})
	}
}

func TestKubernetesPatchSettingsAndAcceptance(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	if v, rev, err := db.KubernetesPatchSettings(ctx); err != nil || rev != 0 || v.Enabled {
		t.Fatal("patches must default to off", v, rev, err)
	}
	app := patchedTestSpec(t, "metadata:\n  labels:\n    team: shop\n")
	if _, err := db.Accept(ctx, p, "demo", "development", app, 0, "kubernetes-patch-off"); !errors.Is(err, ErrKubernetesPatchesDisabled) {
		t.Fatalf("setting off must refuse new patches: %v", err)
	}
	rev, err := db.PutKubernetesPatchSettings(ctx, KubernetesPatchSettings{Enabled: true}, 0)
	if err != nil || rev != 1 {
		t.Fatal("enable", rev, err)
	}
	if _, err := db.PutKubernetesPatchSettings(ctx, KubernetesPatchSettings{}, 0); !errors.Is(err, ErrConflict) {
		t.Fatal("stale create", err)
	}
	// The bootstrap key is a machine key holding only the admin wildcard.
	if _, err := db.Accept(ctx, p, "demo", "development", app, 0, "kubernetes-patch-wildcard"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a machine key needs an explicit kubernetes:patch grant: %v", err)
	}
	granted := p
	granted.Permissions = append([]string{"kubernetes:patch"}, p.Permissions...)
	if _, err := db.Accept(ctx, granted, "demo", "development", app, 0, "kubernetes-patch-granted"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutKubernetesPatchSettings(ctx, KubernetesPatchSettings{}, 1); err != nil {
		t.Fatal("disable", err)
	}
	// Turning the setting off blocks new or changed patches only.
	if _, err := db.Accept(ctx, p, "demo", "development", app, 1, "kubernetes-patch-redeploy"); err != nil {
		t.Fatalf("unchanged patches must keep deploying: %v", err)
	}
	changed := patchedTestSpec(t, "metadata:\n  labels:\n    team: billing\n")
	if _, err := db.Accept(ctx, granted, "demo", "development", changed, 2, "kubernetes-patch-changed"); !errors.Is(err, ErrKubernetesPatchesDisabled) {
		t.Fatalf("a changed patch must be refused while off: %v", err)
	}
}
