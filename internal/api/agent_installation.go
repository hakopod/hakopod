package api

import (
	"github.com/hakopod/hakopod/internal/store"
	"net/http"
	"slices"
)

func installationAdministrator(p store.Principal) bool {
	return p.IsAdmin() && p.IsHuman() && p.CredentialType == "browser" || p.CanUseInstallationAgentAdministration()
}
func installationOwnerAuthority(p store.Principal) bool {
	return p.IsSuperAdmin() || p.Owner && p.CanUseInstallationAgentAdministration()
}
func agentInstallationCredentials(w http.ResponseWriter, r *http.Request, sensitive bool) bool {
	p := who(r)
	if sensitive && (p.CredentialType == "machine" || p.CredentialType == "cli") && (!p.CanUseInstallationAgentAdministration() || !slices.Contains(p.Permissions, "agent:credentials")) {
		failure(w, store.ErrForbidden)
		return false
	}
	return true
}
