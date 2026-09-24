package api

import (
	"context"
	"github.com/hakopod/hakopod/internal/store"
	"net/http"
	"slices"
)

type RuntimeScope struct {
	Identity, Project, Environment string
	Permissions                    []string
	Authorize                      func(context.Context) error
}
type runtimeScopeKey struct{}

// WithRuntimeScope narrows an already authenticated human at the trusted product
// boundary. No header, cookie or request body can set this context value.
func WithRuntimeScope(r *http.Request, scope RuntimeScope) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), runtimeScopeKey{}, scope))
}
func scopedRuntimePrincipal(p store.Principal, scope RuntimeScope) (store.Principal, error) {
	if scope.Identity == "" || scope.Project == "" || scope.Environment == "" || p.ID != scope.Identity || (p.CredentialType != "browser" && p.CredentialType != "cli") || !p.Allows("deployments:read", scope.Project, scope.Environment, "") {
		return store.Principal{}, store.ErrForbidden
	}
	permissions := []string{}
	for _, value := range []string{"deployments:read", "deployments:write", "logs:read", "networks:write"} {
		if p.Allows(value, scope.Project, scope.Environment, "") && (scope.Permissions == nil || slices.Contains(scope.Permissions, value)) {
			permissions = append(permissions, value)
		}
	}
	p.RuntimeScoped = true
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

func (s *Server) freshRuntimePrincipal(r *http.Request) (store.Principal, error) {
	p, err := s.Store.KeyPrincipal(r.Context(), who(r).KeyID)
	if err == nil {
		if scope, ok := r.Context().Value(runtimeScopeKey{}).(RuntimeScope); ok {
			if scope.Authorize != nil {
				if err := scope.Authorize(r.Context()); err != nil {
					return store.Principal{}, err
				}
			}
			return scopedRuntimePrincipal(p, scope)
		}
	}
	return p, err
}
