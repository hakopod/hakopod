package api

import (
	"github.com/hakopod/hakopod/internal/store"
	"testing"
)

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

func TestCloudOperatorRequiresTrustedRuntimeAndUnscopedBrowserOwner(t *testing.T) {
	owner := store.Principal{Owner: true, Admin: true, Email: "operator@example.test", CredentialType: "browser", Permissions: []string{"admin"}}
	for _, tc := range []struct {
		name    string
		enabled bool
		mode    string
		change  func(*store.Principal)
		allowed bool
	}{
		{"internal operator", true, "managed-cloud", func(*store.Principal) {}, true},
		{"customer runtime owner", false, "managed-cloud", func(*store.Principal) {}, false},
		{"ordinary admin", true, "managed-cloud", func(p *store.Principal) { p.Owner = false }, false},
		{"revoked admin", true, "managed-cloud", func(p *store.Principal) { p.Admin = false; p.Permissions = nil }, false},
		{"scoped owner", true, "managed-cloud", func(p *store.Principal) { p.Project = "customer" }, false},
		{"machine credential", true, "managed-cloud", func(p *store.Principal) { p.CredentialType = "api_key" }, false},
		{"self hosted", true, "self-hosted", func(*store.Principal) {}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := owner
			tc.change(&p)
			s := &Server{OperatorRuntime: tc.enabled, Auth: AuthConfig{DeploymentMode: tc.mode}}
			if s.cloudOperator(p) != tc.allowed {
				t.Fatal("incorrect operator access")
			}
		})
	}
}
