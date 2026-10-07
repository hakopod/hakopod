package api

import (
	"github.com/hakopod/hakopod/internal/store"
	"testing"
)

func TestDelegatedInstallationActorAuthority(t *testing.T) {
	p := store.Principal{Admin: true, Owner: true, CredentialType: "machine", Permissions: []string{"admin", "agent:admin"}}
	if !installationAdministrator(p) || !installationOwnerAuthority(p) {
		t.Fatal("delegated owner rejected")
	}
	p.Owner = false
	if !installationAdministrator(p) || installationOwnerAuthority(p) {
		t.Fatal("admin acquired owner authority")
	}
	p.MFARequired = true
	if installationAdministrator(p) || installationOwnerAuthority(p) {
		t.Fatal("pending MFA acquired administration")
	}
	p.MFARequired = false
	p.Project = "p"
	if installationAdministrator(p) {
		t.Fatal("project credential acquired installation authority")
	}
	p.Project = ""
	p.Permissions = []string{"admin"}
	if installationAdministrator(p) {
		t.Fatal("administration opt-in missing")
	}
}
