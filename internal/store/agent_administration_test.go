package store

import (
	"context"
	"errors"
	"testing"
)

func TestInstallationAgentGrantBoundary(t *testing.T) {
	base := Principal{Admin: true, CredentialType: "machine", Permissions: []string{"admin", "agent:admin"}}
	for _, tt := range []struct {
		name string
		edit func(*Principal)
		want bool
	}{
		{"explicit machine", func(*Principal) {}, true},
		{"explicit CLI", func(p *Principal) { p.CredentialType = "cli" }, true},
		{"admin alone", func(p *Principal) { p.Permissions = []string{"admin"} }, false},
		{"grant without authority", func(p *Principal) { p.Admin = false }, false},
		{"grant without admin permission", func(p *Principal) { p.Permissions = []string{"agent:admin"} }, false},
		{"project restriction", func(p *Principal) { p.Project = "demo" }, false},
		{"identity restriction", func(p *Principal) { p.IdentityProject = "demo" }, false},
		{"pending MFA", func(p *Principal) { p.MFARequired = true }, false},
		{"browser is not an agent", func(p *Principal) { p.CredentialType = "browser" }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := base
			tt.edit(&p)
			if got := p.CanUseInstallationAgentAdministration(); got != tt.want {
				t.Fatalf("administration=%v, want %v", got, tt.want)
			}
		})
	}
	for _, tt := range []struct {
		project, environment string
		permissions          []string
		valid                bool
	}{
		{"", "", []string{"admin", "agent:admin"}, true},
		{"", "", []string{"agent:admin"}, false},
		{"demo", "development", []string{"admin", "agent:admin"}, false},
		{"demo", "development", []string{"deployments:read", "agent:credentials"}, true},
	} {
		if err := validKeyFields("agent", tt.project, tt.environment, "", tt.permissions); (err == nil) != tt.valid {
			t.Fatalf("key grants %v: %v", tt.permissions, err)
		}
	}
}

func TestInstallationDeviceConsentAndRevocation(t *testing.T) {
	s := isolatedDatabase(t)
	ctx := context.Background()
	id := NewID()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO identities(id,name,email,admin,permissions,email_verified) VALUES($1,'Installation fixture','installation@example.test',true,ARRAY['admin'],true)", id); err != nil {
		t.Fatal(err)
	}
	browser, err := s.NewSession(ctx, id, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	permissions := []string{"admin", "agent:admin"}
	if _, err = s.StartDevice(ctx, "demo", "development", permissions); !errors.Is(err, ErrForbidden) {
		t.Fatal("installation request accepted project scope", err)
	}
	request, err := s.StartDevice(ctx, "", "", permissions)
	if err != nil {
		t.Fatal(err)
	}
	details, err := s.DeviceDetails(ctx, browser.User, request.UserCode)
	if err != nil || len(details.Scopes) != 1 || details.Scopes[0].ID != "installation" || details.Scopes[0].Project != "" {
		t.Fatal("installation scope was not explicit", details, err)
	}
	if err = s.ApproveDevice(ctx, browser.User, request.UserCode, true); !errors.Is(err, ErrForbidden) {
		t.Fatal("installation selected implicitly", err)
	}
	if err = s.ApproveDevice(ctx, browser.User, request.UserCode, true, details.Scopes[0]); err != nil {
		t.Fatal(err)
	}
	session, err := s.PollDevice(ctx, request.DeviceCode)
	if err != nil || session.ScopeID != "installation" || !session.User.CanUseInstallationAgentAdministration() || contains(session.User.Permissions, "agent:credentials") {
		t.Fatal("installation consent changed requested grants", err)
	}
	if _, err = s.Teams(ctx, session.User); err != nil {
		t.Fatal("agent directory access", err)
	}
	if _, err = s.CustomRoles(ctx, session.User); err != nil {
		t.Fatal("agent custom role listing", err)
	}
	if _, err = s.OrganizationSecurity(ctx, session.User); err != nil {
		t.Fatal("agent organization read", err)
	}
	if _, err = s.SetOrganizationSecurity(ctx, session.User, true, 1); !errors.Is(err, ErrMFARequired) {
		t.Fatal("agent bypassed MFA verification", err)
	}
	if _, err = s.Profile(ctx, Principal{CredentialType: "machine", Admin: true, Permissions: permissions}); !errors.Is(err, ErrForbidden) {
		t.Fatal("machine acquired a personal profile", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE identities SET admin=false WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	current, err := s.KeyPrincipal(ctx, session.User.KeyID)
	if err != nil || current.CanUseInstallationAgentAdministration() {
		t.Fatal("revoked administrator retained access", err)
	}
	if _, err = s.OrganizationSecurity(ctx, current); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked organization access", err)
	}
	if _, err = s.NewSession(ctx, id, "cli", "", "", permissions); !errors.Is(err, ErrForbidden) {
		t.Fatal("nonadministrator received installation session", err)
	}
}

func TestInstallationDeviceCannotUseExternalScopeProvider(t *testing.T) {
	s := &Store{DeviceScopes: func(context.Context, Principal) ([]DeviceScope, error) { return nil, nil }}
	if _, err := s.StartDevice(context.Background(), "", "", []string{"admin", "agent:admin"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("external scope provider accepted installation authority", err)
	}
}
