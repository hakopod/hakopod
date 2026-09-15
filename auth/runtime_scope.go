package auth

import (
	"github.com/hakopod/hakopod/internal/api"
	"net/http"
)

// ServeWorkspaceRuntime additionally narrows the canonical browser identity to
// a product-authorized workspace scope. It never grants missing project rights.
func ServeWorkspaceRuntime(handler http.Handler, w http.ResponseWriter, r *http.Request, identity, project, environment string) {
	handler.ServeHTTP(w, api.WithRuntimeScope(r, api.RuntimeScope{Identity: identity, Project: project, Environment: environment}))
}
