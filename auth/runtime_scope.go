package auth

import (
	"context"
	"github.com/hakopod/hakopod/internal/api"
	"net/http"
)

// ServeWorkspaceRuntime additionally narrows the canonical browser identity to
// a product-authorized workspace scope. It never grants missing project rights.
func ServeWorkspaceRuntime(handler http.Handler, w http.ResponseWriter, r *http.Request, identity, project, environment string) {
	handler.ServeHTTP(w, api.WithRuntimeScope(r, api.RuntimeScope{Identity: identity, Project: project, Environment: environment}))
}

// ServeAuthorizedWorkspaceRuntime also applies current product capabilities on
// initial authentication and each engine stream authorization refresh.
func ServeAuthorizedWorkspaceRuntime(handler http.Handler, w http.ResponseWriter, r *http.Request, identity, project, environment string, permissions []string, authorize func(context.Context) error) {
	handler.ServeHTTP(w, api.WithRuntimeScope(r, api.RuntimeScope{Identity: identity, Project: project, Environment: environment, Permissions: permissions, Authorize: authorize}))
}
