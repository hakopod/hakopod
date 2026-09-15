package api

import (
	"context"
	"encoding/hex"
	"github.com/hakopod/hakopod/internal/store"
	"net/http"
)

func gitManager(w http.ResponseWriter, r *http.Request) bool {
	if !who(r).CanManageGit() {
		authFailure(w, store.ErrForbidden)
		return false
	}
	return true
}
func authorizeGitConnection(ctx context.Context, c gitConnection) error {
	p, ok := ctx.Value(principalKey{}).(store.Principal)
	// Background jobs and signature-verified hooks use immutable saved bindings.
	if !ok || p.IsAdmin() {
		return nil
	}
	if c.Project != "" {
		if p.Project != c.Project || p.Environment != c.Environment || !p.Allows("deployments:read", c.Project, c.Environment, p.Application) {
			return store.ErrForbidden
		}
		return nil
	}
	if scopedGitCredentials(p) {
		return store.ErrForbidden
	}
	return nil // Existing self-hosted deployers still need exact repository approval.
}
func gitInteractive(r *http.Request) bool {
	if !who(r).CanManageGit() {
		return false
	}
	if who(r).CredentialType == "browser" {
		return true
	}
	// A deliberately delegated management key may relay a Cloud browser session.
	// The gateway supplies its digest, so OAuth state cannot cross user sessions.
	actor, err := hex.DecodeString(r.Header.Get("X-Hakopod-Git-Session"))
	return who(r).CredentialType == "machine" && err == nil && len(actor) == 32
}
func gitSession(r *http.Request) string {
	if who(r).CredentialType == "machine" {
		return who(r).KeyID + ":" + r.Header.Get("X-Hakopod-Git-Session")
	}
	return who(r).KeyID
}

func (s *Server) gitWebhookURL(origin, id string) string {
	if s.Auth.GitWebhookPrefix != "" {
		return s.Auth.GitWebhookPrefix + "/git/" + id
	}
	return origin + "/api/v1/webhooks/git/" + id
}

func scopedGitCredentials(p store.Principal) bool {
	return p.RuntimeScoped || p.CredentialType == "machine" && p.CanManageGit() && !p.IsAdmin()
}
