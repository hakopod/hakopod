package store

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestEffectiveAdministrationIncludesOwnerScope(t *testing.T) {
	global := Principal{Admin: true, Permissions: []string{"admin"}}
	if !global.IsAdmin() {
		t.Fatal("unrestricted owner/admin key lost administrative access")
	}
	for _, scope := range []struct{ project, environment string }{{"demo", ""}, {"", "production"}, {"demo", "production"}} {
		p := global
		p.IdentityProject = scope.project
		p.IdentityEnvironment = scope.environment
		if p.IsAdmin() {
			t.Fatalf("owner scope %q/%q did not restrict an existing admin key", scope.project, scope.environment)
		}
	}
	keyScoped := global
	keyScoped.Project = "demo"
	if keyScoped.IsAdmin() {
		t.Fatal("a scoped key gained global administration")
	}
	identityReduced := global
	identityReduced.Admin = false
	identityReduced.IdentityPermissions = []string{"deployments:read"}
	if identityReduced.IsAdmin() || identityReduced.Allows("deployments:write", "demo", "development", "shop") {
		t.Fatal("key retained privileges removed from owning identity")
	}
}

func TestReleasedClaimCannotPerformEffects(t *testing.T) {
	claim := &Claim{}
	ctx := context.Background()
	if !errors.Is(claim.Check(ctx), ErrClaimLost) {
		t.Fatal("released claim passed lease check")
	}
	if _, err := claim.SetResolved(ctx, emptyTestSpec()); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("released claim persisted an artifact: %v", err)
	}
	if err := claim.Finish(ctx, "succeeded", "", map[string]any{}); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("released claim finalized operation: %v", err)
	}
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for j := 0; j < 20; j++ {
				claim.Release()
				if !errors.Is(claim.Check(ctx), ErrClaimLost) {
					t.Error("released claim became usable")
				}
			}
		}()
	}
	group.Wait()
}
