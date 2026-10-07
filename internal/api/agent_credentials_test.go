package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/hakopod/hakopod/internal/store"
)

func TestScopedCredentialGrantBoundary(t *testing.T) {
	base := store.Principal{CredentialType: "machine", Project: "demo", Environment: "development", Permissions: []string{"deployments:write", "agent:credentials"}, IdentityPermissions: []string{"deployments:write", "agent:credentials"}}
	for _, c := range []struct {
		name string
		edit func(*store.Principal)
		want bool
	}{
		{"explicit scope", func(*store.Principal) {}, true},
		{"CLI scope", func(p *store.Principal) { p.CredentialType = "cli" }, true},
		{"missing key grant", func(p *store.Principal) { p.Permissions = []string{"deployments:write"} }, false},
		{"missing identity grant", func(p *store.Principal) { p.IdentityPermissions = []string{"deployments:write"} }, false},
		{"different environment", func(p *store.Principal) { p.Environment = "production" }, false},
		{"application only", func(p *store.Principal) { p.Application = "restricted" }, false},
		{"MFA required", func(p *store.Principal) { p.MFARequired = true }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := base
			c.edit(&p)
			r := httptest.NewRequest("POST", "/api/v1/databases/id/credentials", nil)
			r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
			w := httptest.NewRecorder()
			if agentScopedCredentials(w, r, "demo", "development") != c.want {
				t.Fatal("incorrect delegated credential authority")
			}
		})
	}
}
