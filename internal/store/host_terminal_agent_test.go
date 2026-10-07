package store

import "testing"

func TestDelegatedHostTerminalGrant(t *testing.T) {
	p := Principal{Admin: true, Owner: true, CredentialType: "machine", Permissions: []string{"admin", "agent:admin", "nodes:terminal"}}
	if !p.CanHostTerminal("node") {
		t.Fatal("delegated owner rejected")
	}
	p.Owner = false
	if p.CanHostTerminal("node") {
		t.Fatal("administrator acquired host terminal without node grant")
	}
	p.HostPermissions = []HostPermission{{Node: "node", Permission: "nodes:terminal"}}
	if !p.CanHostTerminal("node") || p.CanHostTerminal("other") {
		t.Fatal("node grant scope bypass")
	}
	p.Permissions = []string{"admin", "agent:admin"}
	if p.CanHostTerminal("node") {
		t.Fatal("host terminal opt-in missing")
	}
}

func TestHostOnlyCredentialUsesStoredGrant(t *testing.T) {
	p := Principal{Email: "host@example.test", CredentialType: "cli", Permissions: []string{"nodes:terminal"}, HostPermissions: []HostPermission{{Node: "node", Permission: "nodes:terminal"}}}
	if !p.CanUseHostCredential() || !p.CanHostTerminal("node") || p.CanHostTerminal("other") {
		t.Fatal("host credential node boundary")
	}
	p.Project = "other"
	if p.CanUseHostCredential() || p.CanHostTerminal("node") {
		t.Fatal("project credential acquired host access")
	}
	p.Project = ""
	p.MFARequired = true
	if p.CanUseHostCredential() || p.CanHostTerminal("node") {
		t.Fatal("pending MFA acquired host access")
	}
}
