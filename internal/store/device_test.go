package store

import (
	"context"
	"errors"
	"testing"
)

func TestDeviceConsentSelectsAndRechecksScope(t *testing.T) {
	s := isolatedDatabase(t)
	ctx := context.Background()
	// Explicit test identity; no password, email or production credentials.
	id := NewID()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('demo'); INSERT INTO environments(project,name) VALUES('demo','development')"); err != nil {
		t.Fatal(err)
	}
	_, err := s.Pool.Exec(ctx, "INSERT INTO identities(id,name,email,admin,permissions,email_verified) VALUES($1,'CLI fixture','cli@example.test',true,ARRAY['admin'],true)", id)
	if err != nil {
		t.Fatal(err)
	}
	browser, err := s.NewSession(ctx, id, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	request, err := s.StartDevice(ctx, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	details, err := s.DeviceDetails(ctx, browser.User, request.UserCode)
	if err != nil || len(details.Scopes) == 0 {
		t.Fatal(details, err)
	}
	scope := details.Scopes[0]
	if err = s.ApproveDevice(ctx, browser.User, request.UserCode, true, DeviceScope{Project: "missing", Environment: "missing"}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err = s.ApproveDevice(ctx, browser.User, request.UserCode, true, scope); err != nil {
		t.Fatal(err)
	}
	session, err := s.PollDevice(ctx, request.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	if session.User.CredentialType != "cli" || session.User.Project != scope.Project || session.User.Environment != scope.Environment || session.User.IsAdmin() {
		t.Fatal("unscoped session")
	}
	if session.User.CanManageProject(scope.Project) {
		t.Fatal("CLI acquired membership administration")
	}
	if !session.User.CanManageGit() {
		t.Fatal("scoped administrator cannot set up builds")
	}
	if _, err = s.PollDevice(ctx, request.DeviceCode); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("code reused", err)
	}
}

func TestExternalDeviceScopeRevocationBeforeIssuance(t *testing.T) {
	s := isolatedDatabase(t)
	ctx := context.Background()
	id := NewID()
	_, err := s.Pool.Exec(ctx, "INSERT INTO identities(id,name,email,permissions,email_verified) VALUES($1,'Cloud fixture','cloud@example.test',ARRAY['admin'],true)", id)
	if err != nil {
		t.Fatal(err)
	}
	browser, err := s.NewSession(ctx, id, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := DeviceScope{ID: NewID(), Label: "Workspace", Project: "remote-project", Environment: "production"}
	enabled := true
	s.DeviceScopes = func(context.Context, Principal) ([]DeviceScope, error) {
		if enabled {
			return []DeviceScope{scope}, nil
		}
		return nil, nil
	}
	request, err := s.StartDevice(ctx, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ApproveDevice(ctx, browser.User, request.UserCode, true, scope); err != nil {
		t.Fatal(err)
	}
	enabled = false
	if _, err = s.PollDevice(ctx, request.DeviceCode); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked access issued token", err)
	}
	enabled = true
	request, err = s.StartDevice(ctx, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ApproveDevice(ctx, browser.User, request.UserCode, true, scope); err != nil {
		t.Fatal(err)
	}
	session, err := s.PollDevice(ctx, request.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	var binding string
	if err = s.Pool.QueryRow(ctx, "SELECT scope_id FROM device_session_scopes WHERE key_id=$1", session.User.KeyID).Scan(&binding); err != nil || binding != scope.ID || session.ScopeID != scope.ID {
		t.Fatal("missing binding", err)
	}
}
func TestDevicePermissionsRequireExplicitExecutionConsent(t *testing.T) {
	defaults, err := devicePermissions(nil)
	if err != nil || len(defaults) != 3 || !contains(defaults, "deployments:read") || !contains(defaults, "deployments:write") || !contains(defaults, "logs:read") {
		t.Fatal(defaults, err)
	}
	if contains(defaults, "pods:exec") || contains(defaults, "databases:query") {
		t.Fatal("default execution privilege expanded")
	}
	all := []string{"deployments:read", "deployments:write", "logs:read", "pods:exec", "databases:query", "databases:write-query"}
	got, err := devicePermissions(all)
	if err != nil || len(got) != 6 {
		t.Fatal(got, err)
	}
	got[0] = "changed"
	if all[0] != "deployments:read" {
		t.Fatal("permission slice aliased")
	}
	for _, in := range [][]string{{"databases:write-query"}, {"pods:exec", "pods:exec"}, {"admin"}, {"deployments:read", "deployments:write", "logs:read", "pods:exec", "databases:query", "databases:write-query", "pods:exec"}} {
		if _, err := devicePermissions(in); err == nil {
			t.Fatal("invalid permissions accepted", in)
		}
	}
}

func TestExternalDevicePermissionsRecheckedBeforeIssuance(t *testing.T) {
	s := isolatedDatabase(t)
	ctx := context.Background()
	id := NewID()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO identities(id,name,email,permissions,email_verified) VALUES($1,'Cloud permission fixture','permission@example.test',ARRAY['admin'],true)", id); err != nil {
		t.Fatal(err)
	}
	browser, err := s.NewSession(ctx, id, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := DeviceScope{ID: NewID(), Label: "Workspace", Project: "remote-project", Environment: "production"}
	s.DeviceScopes = func(context.Context, Principal) ([]DeviceScope, error) { return []DeviceScope{scope}, nil }
	grants := []string{"deployments:read", "pods:exec"}
	request, err := s.StartDevice(ctx, "", "", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ApproveDevice(ctx, browser.User, request.UserCode, true, scope); !errors.Is(err, ErrForbidden) {
		t.Fatal("legacy external scope granted execution", err)
	}
	scope.Permissions = grants
	if err = s.ApproveDevice(ctx, browser.User, request.UserCode, true, scope); err != nil {
		t.Fatal(err)
	}
	scope.Permissions = []string{"deployments:read"}
	if _, err = s.PollDevice(ctx, request.DeviceCode); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked permission issued token", err)
	}
	request, err = s.StartDevice(ctx, "", "", grants)
	if err != nil {
		t.Fatal(err)
	}
	scope.Permissions = grants
	if err = s.ApproveDevice(ctx, browser.User, request.UserCode, true, scope); err != nil {
		t.Fatal(err)
	}
	if session, err := s.PollDevice(ctx, request.DeviceCode); err != nil || !contains(session.User.Permissions, "pods:exec") {
		t.Fatal("explicit external execution grant lost", err)
	}
}
func TestDeviceConsentIssuesOnlyRequestedExecutionPermissions(t *testing.T) {
	s := isolatedDatabase(t)
	ctx := context.Background()
	id := NewID()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('demo'); INSERT INTO environments(project,name) VALUES('demo','development')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "INSERT INTO identities(id,name,email,admin,permissions,email_verified) VALUES($1,'Execution fixture','execution@example.test',true,ARRAY['admin'],true)", id); err != nil {
		t.Fatal(err)
	}
	browser, err := s.NewSession(ctx, id, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	permissions := []string{"deployments:read", "pods:exec", "databases:query", "databases:write-query"}
	request, err := s.StartDevice(ctx, "demo", "development", permissions)
	if err != nil {
		t.Fatal(err)
	}
	details, err := s.DeviceDetails(ctx, browser.User, request.UserCode)
	if err != nil || len(details.Permissions) != len(permissions) {
		t.Fatal(details, err)
	}
	if err := s.ApproveDevice(ctx, browser.User, request.UserCode, true, DeviceScope{Project: "demo", Environment: "development"}); err != nil {
		t.Fatal(err)
	}
	session, err := s.PollDevice(ctx, request.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	for _, permission := range permissions {
		if !session.User.Allows(permission, "demo", "development", "") {
			t.Fatal("requested permission omitted", permission)
		}
	}
	if session.User.Allows("deployments:write", "demo", "development", "") || session.User.Allows("pods:exec", "demo", "other", "") {
		t.Fatal("consent acquired unrequested scope or permission")
	}
}
