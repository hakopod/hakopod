package store

import "testing"

func TestAgentCapabilityKeyScope(t *testing.T) {
	for _, tt := range []struct {
		name, project, environment, application string
		permissions                             []string
		ok                                      bool
	}{
		{"pod grant", "demo", "development", "api", []string{"pods:exec"}, true},
		{"SQL grant", "demo", "development", "", []string{"databases:query", "databases:write-query"}, true},
		{"unscoped admin exec", "", "", "", []string{"admin", "pods:exec"}, false},
		{"application SQL", "demo", "development", "api", []string{"databases:query"}, false},
		{"write without query", "demo", "development", "", []string{"databases:write-query"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validKeyFields("agent", tt.project, tt.environment, tt.application, tt.permissions); (err == nil) != tt.ok {
				t.Fatalf("permission validation = %v, want success %v", err, tt.ok)
			}
		})
	}
}

func TestAgentCapabilityIdentityBoundary(t *testing.T) {
	p := Principal{Email: "operator@example.test", CredentialType: "machine", Project: "demo", Environment: "development", Permissions: []string{"pods:exec"}, ProjectRoles: []ProjectRole{{Project: "demo", Role: "developer"}}}
	if p.Allows("pods:exec", "demo", "development", "api") {
		t.Fatal("developer role escalated through a key")
	}
	p.ProjectRoles[0].Role = "admin"
	if !p.Allows("pods:exec", "demo", "development", "api") {
		t.Fatal("project admin cannot delegate pod execution")
	}
	if p.Allows("pods:exec", "other", "development", "api") {
		t.Fatal("grant escaped project")
	}
	p.MFARequired = true
	if p.Allows("pods:exec", "demo", "development", "api") {
		t.Fatal("MFA requirement bypassed")
	}
}

func TestQueryRoleWriteRequiresReadGrant(t *testing.T) {
	if validRolePermissions([]string{"databases:write-query"}) {
		t.Fatal("SQL write role lacks query permission")
	}
	if !validRolePermissions([]string{"databases:query", "databases:write-query"}) {
		t.Fatal("query role rejected")
	}
}
