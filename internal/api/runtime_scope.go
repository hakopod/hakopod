package api

import (
	"context"
	"github.com/hakopod/hakopod/internal/store"
	"net/http"
)

type RuntimeScope struct{ Identity, Project, Environment string }
type runtimeScopeKey struct{}

// WithRuntimeScope narrows an already authenticated human at the trusted product
// boundary. No header, cookie or request body can set this context value.
func WithRuntimeScope(r *http.Request, scope RuntimeScope) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), runtimeScopeKey{}, scope))
}
func scopedRuntimePrincipal(p store.Principal, scope RuntimeScope) (store.Principal, error) {
	if scope.Identity == "" || scope.Project == "" || scope.Environment == "" || p.ID != scope.Identity || p.CredentialType != "browser" || !p.Allows("deployments:read", scope.Project, scope.Environment, "") {
		return store.Principal{}, store.ErrForbidden
	}
	permissions := []string{}
	for _, value := range []string{"deployments:read", "deployments:write", "logs:read", "networks:write"} {
		if p.Allows(value, scope.Project, scope.Environment, "") {
			permissions = append(permissions, value)
		}
	}
	p.Admin = false
	p.Owner = false
	p.HostPermissions = nil
	p.Project = scope.Project
	p.Environment = scope.Environment
	p.IdentityProject = scope.Project
	p.IdentityEnvironment = scope.Environment
	p.Permissions = permissions
	p.IdentityPermissions = permissions
	roles := []store.ProjectRole{}
	for _, role := range p.ProjectRoles {
		if role.Project == scope.Project {
			roles = append(roles, role)
		}
	}
	p.ProjectRoles = roles
	return p, nil
}
