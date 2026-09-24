package api

import (
	"github.com/hakopod/hakopod/internal/store"
	"testing"
)

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
