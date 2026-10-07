package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/hakopod/hakopod/internal/store"
)

// MCPScope is current authority resolved by a trusted product embedding.
// Binding identifies the workspace and runtime destination. It is not a client hint.
type MCPScope struct {
	Identity, KeyID, Binding, Project, Environment, Application string
	Permissions                                                 []string
}

type mcpEmbeddingKey struct{}

// NewScopedMCPHandler reuses the canonical MCP transport without a second store.
// Resolve must authenticate a delegated credential and recheck its current scope.
// Routes must reauthorize each API request, including product approval requirements.
func NewScopedMCPHandler(routes http.Handler, publicURL string, resolve func(*http.Request) (MCPScope, error)) http.Handler {
	server := &Server{Auth: AuthConfig{PublicURL: publicURL}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if routes == nil || resolve == nil || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			problem(w, 403, "forbidden", "MCP requires a delegated bearer credential.")
			return
		}
		scope, err := resolve(r)
		if err != nil || scope.Identity == "" || scope.KeyID == "" || scope.Binding == "" || !validScope(scope.Project, scope.Environment) || slices.Contains(scope.Permissions, "admin") || slices.Contains(scope.Permissions, "agent:admin") {
			problem(w, 403, "forbidden", "The MCP credential does not authorize this workspace.")
			return
		}
		// The embedding supplies the intersection of credential and workspace grants.
		// Canonical dispatch still verifies the original bearer credential per call.
		p := store.Principal{ID: scope.Identity, KeyID: scope.KeyID, CredentialType: "machine", Project: scope.Project, Environment: scope.Environment, Application: scope.Application, Permissions: slices.Clone(scope.Permissions), IdentityPermissions: slices.Clone(scope.Permissions)}
		binding := scope.Identity + "\x00" + scope.KeyID + "\x00" + scope.Binding
		ctx := context.WithValue(r.Context(), principalKey{}, p)
		ctx = context.WithValue(ctx, mcpEmbeddingKey{}, binding)
		server.mcp(w, r.WithContext(ctx), routes)
	})
}

func mcpDispatchHeaders(original *http.Request) http.Header {
	headers := make(http.Header)
	if _, embedded := original.Context().Value(mcpEmbeddingKey{}).(string); embedded {
		for _, name := range []string{"Authorization", "X-Hakopod-Workspace"} {
			if value := original.Header.Get(name); value != "" {
				headers.Set(name, value)
			}
		}
	}
	return headers
}
