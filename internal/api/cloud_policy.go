package api

import (
	"net/http"
	"strings"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
)

// Customer Cloud runtimes deny installation administration. The trusted internal
// runtime may serve its live, verified installation owner.
func (s *Server) cloudOperator(p store.Principal) bool {
	return s.OperatorRuntime && s.Auth.DeploymentMode == cluster.DeploymentManagedCloud && installationOwnerAuthority(p)
}

func cloudInstallationRequest(method, path string) bool {
	// Customers may discover the configured default; the handler filters its
	// metadata. Creating shared ClusterIssuers remains installation work.
	if method == http.MethodGet && path == "/api/v1/tls/issuers" {
		return false
	}
	return cloudInstallationPath(path)
}

func cloudInstallationPath(path string) bool {
	path = strings.TrimPrefix(path, "/api/v1/")
	for _, prefix := range []string{"host-access", "installation", "users", "tls/issuers", "settings/haproxy"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return strings.HasPrefix(path, "nodes/") && (strings.Contains(path, "/terminal") || strings.HasSuffix(path, "/cordon") || strings.HasSuffix(path, "/drain"))
}
