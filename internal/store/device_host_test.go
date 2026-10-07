package store

import (
	"context"
	"errors"
	"testing"
)

func TestHostDeviceConsentAndRevocation(t *testing.T) {
	s := isolatedDatabase(t)
	ctx := context.Background()
	id := NewID()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO identities(id,name,email,permissions,email_verified) VALUES($1,'Host fixture','host@example.test',ARRAY['nodes:terminal'],true)", id); err != nil {
		t.Fatal(err)
	}
	browser, err := s.NewSession(ctx, id, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	request, err := s.StartDevice(ctx, "", "", []string{"nodes:terminal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeviceDetails(ctx, browser.User, request.UserCode); !errors.Is(err, ErrForbidden) {
		t.Fatal("ungranted consent", err)
	}
	if _, err := s.Pool.Exec(ctx, "INSERT INTO host_access(identity_id,granted_by,node,permission,expires_at) VALUES($1,$1,'fixture-node','nodes:terminal',now()+interval '1 hour')", id); err != nil {
		t.Fatal(err)
	}
	browser.User.HostPermissions = []HostPermission{{Node: "fixture-node", Permission: "nodes:terminal"}}
	details, err := s.DeviceDetails(ctx, browser.User, request.UserCode)
	if err != nil || len(details.Scopes) != 1 || details.Scopes[0].ID != "host" {
		t.Fatal("host consent", err)
	}
	if err := s.ApproveDevice(ctx, browser.User, request.UserCode, true, DeviceScope{ID: "installation"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("installation consent accepted", err)
	}
	if err := s.ApproveDevice(ctx, browser.User, request.UserCode, true, details.Scopes[0]); err != nil {
		t.Fatal(err)
	}
	session, err := s.PollDevice(ctx, request.DeviceCode)
	if err != nil || session.ScopeID != "host" || !session.User.CanUseHostCredential() || !session.User.CanHostTerminal("fixture-node") || session.User.CanHostTerminal("other") {
		t.Fatal("host session", err)
	}
	if _, err := s.Pool.Exec(ctx, "DELETE FROM host_access WHERE identity_id=$1", id); err != nil {
		t.Fatal(err)
	}
	current, err := s.Authenticate(ctx, session.Token)
	if err != nil || current.CanHostTerminal("fixture-node") {
		t.Fatal("revoked grant retained", err)
	}
}

func TestMachineHostGrantReload(t *testing.T) {
	s := isolatedDatabase(t)
	ctx := context.Background()
	id := NewID()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO identities(id,name,email,permissions,email_verified) VALUES($1,'Machine host fixture','machine-host@example.test',ARRAY['nodes:terminal'],true)", id); err != nil {
		t.Fatal(err)
	}
	keyID, raw, digest := makeKey()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO api_keys(id,identity_id,name,digest,prefix,kind,permissions,expires_at) VALUES($1,$2,'Host fixture',$3,'fixture','machine',ARRAY['nodes:terminal'],now()+interval '1 hour')", keyID, id, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "INSERT INTO host_access(identity_id,granted_by,node,permission,expires_at) VALUES($1,$1,'fixture-node','nodes:terminal',now()+interval '1 hour')", id); err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, raw)
	if err != nil || !p.CanUseHostCredential() || !p.CanHostTerminal("fixture-node") || p.CanHostTerminal("other") || p.IsHuman() || p.CanManageHostAccess() {
		t.Fatal("machine node grant", err)
	}
	if _, err := s.Pool.Exec(ctx, "DELETE FROM host_access WHERE identity_id=$1", id); err != nil {
		t.Fatal(err)
	}
	p, err = s.Authenticate(ctx, raw)
	if err != nil || p.CanHostTerminal("fixture-node") {
		t.Fatal("machine retained revoked grant", err)
	}
}
