package api

import (
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"strings"
)

// Customer Cloud runtimes deny installation administration. The trusted internal
// runtime may serve its live, verified installation owner.
func (s *Server) cloudOperator(p store.Principal) bool {
	return s.OperatorRuntime && s.Auth.DeploymentMode == cluster.DeploymentManagedCloud && p.IsSuperAdmin()
}

func cloudInstallationPath(path string) bool {
	path = strings.TrimPrefix(path, "/api/v1/")
	for _, prefix := range []string{"host-access", "installation", "users", "backup-destinations", "backup-targets", "backups", "backup-artifacts", "backup-schedules", "tls/issuers", "settings/haproxy"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return strings.HasPrefix(path, "nodes/") && (strings.Contains(path, "/terminal") || strings.HasSuffix(path, "/cordon") || strings.HasSuffix(path, "/drain"))
}
