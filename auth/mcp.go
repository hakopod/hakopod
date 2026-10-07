package auth

import (
	"net/http"

	"github.com/hakopod/hakopod/internal/api"
)

type MCPScope = api.MCPScope

// NewScopedMCPHandler exposes scoped MCP through a trusted product embedding.
// Resolve must recheck the delegated credential and workspace on every request.
// Routes must enforce canonical authorization and approvals for each tool call.
func NewScopedMCPHandler(routes http.Handler, publicURL string, resolve func(*http.Request) (MCPScope, error)) http.Handler {
	return api.NewScopedMCPHandler(routes, publicURL, resolve)
}
