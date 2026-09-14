package api

import "strings"

// Installation administration is never exposed through the managed dashboard,
// including to the first Cloud account. Operator configuration is out of band.
func cloudInstallationPath(path string) bool {
	path = strings.TrimPrefix(path, "/api/v1/")
	for _, prefix := range []string{"host-access", "installation", "users", "backup-destinations", "backup-targets", "backups", "backup-artifacts", "backup-schedules", "registries", "tls/issuers", "settings/haproxy"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return strings.HasPrefix(path, "nodes/") && (strings.Contains(path, "/terminal") || strings.HasSuffix(path, "/cordon") || strings.HasSuffix(path, "/drain"))
}
