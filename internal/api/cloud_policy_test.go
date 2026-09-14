package api

import "testing"

func TestCloudInstallationPaths(t *testing.T) {
	for _, path := range []string{"host-access", "host-access/owner", "installation/smtp", "installation/login-providers/google", "installation/logs/query", "installation/upgrade", "nodes/node-a/terminal/session/output", "nodes/node-a/drain", "settings/haproxy"} {
		if !cloudInstallationPath("/api/v1/" + path) {
			t.Errorf("Cloud allowed installation path %s", path)
		}
	}
	for _, path := range []string{"applications/id/services/web/terminal", "applications/id/domains", "projects", "virtual-networks", "auth/security", "settings/appearance"} {
		if cloudInstallationPath("/api/v1/" + path) {
			t.Errorf("blocked workload/account path %s", path)
		}
	}
}
