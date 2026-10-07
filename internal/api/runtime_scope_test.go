package api

import (
	"context"
	"github.com/hakopod/hakopod/internal/store"
	"testing"
)

func TestRuntimeMachineNeedsExplicitEmbeddingBindingAndKeepsKeyCeiling(t *testing.T) {
	p := store.Principal{ID: "alice", Email: "alice@example.test", CredentialType: "machine", Project: "one", Environment: "production", Permissions: []string{"deployments:read", "deployments:write"}, ProjectRoles: []store.ProjectRole{{Project: "one", Role: "admin"}}}
	scope := RuntimeScope{AllowMachine: true, Identity: p.ID, Project: p.Project, Environment: p.Environment, Permissions: []string{"deployments:read", "deployments:write", "applications:manage"}, Authorize: func(context.Context) error { return nil }}
	narrowed, err := scopedRuntimePrincipal(p, scope)
	if err != nil || !narrowed.Allows("deployments:write", "one", "production", "") || narrowed.CanManageApplication("one", "production", "app") || narrowed.IsAdmin() {
		t.Fatal("machine authority widened", err)
	}
	scope.Authorize = nil
	if _, err = scopedRuntimePrincipal(p, scope); err == nil {
		t.Fatal("machine without live product authorizer accepted")
	}
}

func TestRuntimeExecutionGrantsRequireBothKeyAndWorkspace(t *testing.T) {
	grants := []string{"pods:exec", "databases:query", "databases:write-query", "agent:credentials"}
	for _, permission := range grants {
		t.Run(permission, func(t *testing.T) {
			p := store.Principal{ID: "alice", CredentialType: "machine", Project: "one", Environment: "production", Permissions: []string{"deployments:read", permission}, IdentityPermissions: []string{"deployments:read", permission}}
			scope := RuntimeScope{AllowMachine: true, Identity: p.ID, Project: p.Project, Environment: p.Environment, Permissions: []string{"deployments:read", permission}, Authorize: func(context.Context) error { return nil }}
			narrowed, err := scopedRuntimePrincipal(p, scope)
			if err != nil || !narrowed.Allows(permission, p.Project, p.Environment, "") {
				t.Fatal("explicit execution grant lost", err)
			}
			scope.Permissions = []string{"deployments:read"}
			narrowed, err = scopedRuntimePrincipal(p, scope)
			if err != nil || narrowed.Allows(permission, p.Project, p.Environment, "") {
				t.Fatal("workspace ceiling bypassed", err)
			}
			scope.Permissions = []string{"deployments:read", permission}
			p.Permissions = []string{"deployments:read"}
			narrowed, err = scopedRuntimePrincipal(p, scope)
			if err != nil || narrowed.Allows(permission, p.Project, p.Environment, "") {
				t.Fatal("key ceiling bypassed", err)
			}
			p.Permissions = []string{"deployments:read", permission}
			p.IdentityPermissions = []string{"deployments:read"}
			narrowed, err = scopedRuntimePrincipal(p, scope)
			if err != nil || narrowed.Allows(permission, p.Project, p.Environment, "") {
				t.Fatal("identity ceiling bypassed", err)
			}
		})
	}
}

func TestRuntimeScopeCannotGrantOrCrossWorkspaceAuthority(t *testing.T) {
	p := store.Principal{ID: "alice", Email: "alice@example.test", CredentialType: "browser", Permissions: []string{"admin"}, ProjectRoles: []store.ProjectRole{{Project: "free-alice", Role: "developer"}, {Project: "other", Role: "developer"}}}
	narrowed, err := scopedRuntimePrincipal(p, RuntimeScope{Identity: "alice", Project: "free-alice", Environment: "production"})
	if err != nil || !narrowed.Allows("deployments:write", "free-alice", "production", "") {
		t.Fatal(err)
	}
	if narrowed.IsAdmin() || narrowed.Owner || narrowed.Allows("deployments:read", "other", "production", "") || narrowed.Allows("deployments:write", "free-alice", "development", "") {
		t.Fatal("scope widened")
	}
	for _, scope := range []RuntimeScope{{Identity: "bob", Project: "free-alice", Environment: "production"}, {Identity: "alice", Project: "unowned", Environment: "production"}, {Identity: "alice", Project: "free-alice"}} {
		if _, err := scopedRuntimePrincipal(p, scope); err == nil {
			t.Fatal("accepted invalid scope", scope)
		}
	}
	p.CredentialType = "machine"
	if _, err := scopedRuntimePrincipal(p, RuntimeScope{Identity: "alice", Project: "free-alice", Environment: "production"}); err == nil {
		t.Fatal("machine key accepted as customer browser")
	}
}

func TestRuntimeCustomPermissionsOnlyNarrowAuthority(t *testing.T) {
	p := store.Principal{ID: "alice", Email: "alice@example.test", CredentialType: "browser", Permissions: []string{"admin"}, ProjectRoles: []store.ProjectRole{{Project: "project", Role: "developer"}}}
	narrowed, err := scopedRuntimePrincipal(p, RuntimeScope{Identity: "alice", Project: "project", Environment: "production", Permissions: []string{"deployments:read", "deployments:approve", "admin"}})
	if err != nil || !narrowed.Allows("deployments:read", "project", "production", "") || narrowed.Allows("logs:read", "project", "production", "") || narrowed.Allows("deployments:write", "project", "production", "") || narrowed.IsAdmin() {
		t.Fatal("embedding widened or lost authority", err)
	}
}

func TestRuntimeCLIGrantCannotSwitchItsConsentScope(t *testing.T) {
	p := store.Principal{ID: "alice", Email: "alice@example.test", CredentialType: "cli", Project: "one", Environment: "production", Permissions: []string{"deployments:read", "deployments:write"}, ProjectRoles: []store.ProjectRole{{Project: "one", Role: "admin"}, {Project: "two", Role: "admin"}}}
	for _, scope := range []RuntimeScope{{Identity: "alice", Project: "two", Environment: "production"}, {Identity: "alice", Project: "one", Environment: "development"}} {
		if _, err := scopedRuntimePrincipal(p, scope); err == nil {
			t.Fatal("CLI scope widened")
		}
	}
	narrowed, err := scopedRuntimePrincipal(p, RuntimeScope{Identity: "alice", Project: "one", Environment: "production"})
	if err != nil || !narrowed.CanManageGit() || narrowed.CanManageProject("one") {
		t.Fatal("incorrect scoped CLI authority", err)
	}
}
