package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInstallationHostAdministrationRequiresOwner(t *testing.T) {
	base := Principal{Owner: true, Admin: true, CredentialType: "machine", Permissions: []string{"admin", "agent:admin"}}
	for _, c := range []struct {
		name string
		edit func(*Principal)
		want bool
	}{
		{"owner machine", func(*Principal) {}, true},
		{"owner CLI", func(p *Principal) { p.CredentialType = "cli" }, true},
		{"administrator without owner", func(p *Principal) { p.Owner = false }, false},
		{"owner without administrator", func(p *Principal) { p.Admin = false }, false},
		{"missing consent", func(p *Principal) { p.Permissions = []string{"admin"} }, false},
		{"project scope", func(p *Principal) { p.Project = "other" }, false},
		{"pending MFA", func(p *Principal) { p.MFARequired = true }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := base
			c.edit(&p)
			if p.CanManageHostAccess() != c.want {
				t.Fatal("incorrect host administration authority")
			}
		})
	}
}

func TestInstallationHostAdministrationRechecksCurrentOwner(t *testing.T) {
	s := isolatedDatabase(t)
	ctx := context.Background()
	owner, member := NewID(), NewID()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO identities(id,name,email,owner,admin,permissions) VALUES($1,'Owner fixture','owner@host.test',true,true,ARRAY['admin']),($2,'Member fixture','member@host.test',false,false,ARRAY[]::text[])`, owner, member); err != nil {
		t.Fatal(err)
	}
	browser, err := s.NewSession(ctx, owner, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := s.CreateKey(ctx, browser.User, KeyInput{Name: "host administration fixture", Permissions: []string{"admin", "agent:admin"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	g := HostGrant{IdentityID: member, Node: "*", Permission: "nodes:terminal", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err = s.SetHostGrant(ctx, p, g); err != nil {
		t.Fatal(err)
	}
	if grants, err := s.HostGrants(ctx, p); err != nil || len(grants) != 1 {
		t.Fatal("delegated owner cannot list grants", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE identities SET owner=false WHERE id=$1", owner); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetHostGrant(ctx, p, g); !errors.Is(err, ErrForbidden) {
		t.Fatal("stale owner created a host grant", err)
	}
	if err = s.RevokeHostGrant(ctx, p, member, "*"); !errors.Is(err, ErrForbidden) {
		t.Fatal("stale owner removed a host grant", err)
	}
}
