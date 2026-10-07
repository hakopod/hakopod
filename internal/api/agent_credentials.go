package api

import (
	"net/http"
	"slices"

	"github.com/hakopod/hakopod/internal/store"
)

// Delegated callers need an explicit credential grant within their current scope.
// Resource handlers must also check the operation's ordinary authority.
func agentScopedCredentials(w http.ResponseWriter, r *http.Request, project, environment string) bool {
	p := who(r)
	if p.CredentialType != "machine" && p.CredentialType != "cli" {
		return true
	}
	if !slices.Contains(p.Permissions, "agent:credentials") || !p.Allows("agent:credentials", project, environment, "") {
		failure(w, store.ErrForbidden)
		return false
	}
	return true
}
