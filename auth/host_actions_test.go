package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

// This development fixture uses its own PostgreSQL database, never a Cloud
// workspace, Kubernetes cluster, user session, or live credential.
func hostActionsFixture(t *testing.T) (*Service, HostActionsGrant, spec.Application) {
	t.Helper()
	dsn := os.Getenv("HAKOPOD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "hakopod_host_actions_test_" + store.NewID()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	s, err := Open(ctx, parsed.String(), Config{DeploymentMode: cluster.DeploymentManagedCloud})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		admin.Close(cleanup)
	})
	grant := HostActionsGrant{OwnerIdentity: store.NewID(), Workspace: store.NewID(), Project: "demo", Environment: "development", ApplicationID: store.NewID(), Service: "runner", HostID: store.NewID()}
	if _, err = s.Pool().Exec(ctx, "INSERT INTO identities(id,name,email,email_verified,admin,owner) VALUES($1,'Host recovery fixture','host-recovery@example.test',true,true,true)", grant.OwnerIdentity); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool().Exec(ctx, "INSERT INTO projects(name) VALUES('demo'); INSERT INTO environments(project,name) VALUES('demo','development')"); err != nil {
		t.Fatal(err)
	}
	application, err := spec.Normalize(spec.Application{Name: "recovery-runners", Services: map[string]spec.Service{"runner": {Actions: &spec.Actions{Repository: "fixture/repo", Credential: "fixture-credential"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool().Exec(ctx, "INSERT INTO applications(id,name,project,environment,revision,status,spec) VALUES($1,$2,$3,$4,1,'ready',$5)", grant.ApplicationID, application.Name, grant.Project, grant.Environment, store.JSON(application)); err != nil {
		t.Fatal(err)
	}
	return s, grant, application
}

func TestHostActionsKeyIsDurableScopedAndNeverBrowserAuthority(t *testing.T) {
	s, grant, application := hostActionsFixture(t)
	ctx := context.Background()
	key, raw, err := s.EnsureHostActionsKey(ctx, grant, "")
	if err != nil {
		t.Fatal(err)
	}
	if key.Application != application.Name || key.IdentityID != grant.OwnerIdentity || key.ExpiresAt == nil || key.NeverExpires || key.ExpiresAt.After(time.Now().Add(90*24*time.Hour)) {
		t.Fatal("host key metadata exceeds its scope")
	}
	p, err := s.store.Authenticate(ctx, raw)
	if err != nil || p.IsAdmin() || p.IsSuperAdmin() || p.CredentialType != "machine" || !p.Allows("deployments:write", grant.Project, grant.Environment, application.Name) || p.Allows("deployments:write", grant.Project, grant.Environment, "other") || p.Allows("logs:read", grant.Project, grant.Environment, application.Name) {
		t.Fatal("host key authority is not limited to its application", err)
	}
	if err = s.store.Reauthorize(ctx, key.ID, grant.Project, grant.Environment, application.Name); err != nil {
		t.Fatal("worker could not reauthorize persisted key", err)
	}
	if _, err = s.Verify(ctx, raw); err == nil {
		t.Fatal("host key became a browser identity")
	}
	if _, _, err = s.VerifyAutomation(ctx, raw); err == nil {
		t.Fatal("host key became a customer automation identity")
	}
	if _, _, err = s.EnsureHostActionsKey(ctx, grant, ""); !errors.Is(err, store.ErrConflict) {
		t.Fatal("lost private key silently created another credential", err)
	}
	for _, field := range []string{"workspace", "host", "service", "owner", "environment"} {
		t.Run(field, func(t *testing.T) {
			changed := grant
			switch field {
			case "workspace":
				changed.Workspace = store.NewID()
			case "host":
				changed.HostID = store.NewID()
			case "service":
				changed.Service = "other"
			case "owner":
				changed.OwnerIdentity = store.NewID()
			case "environment":
				changed.Environment = "production"
			}
			if _, _, err := s.EnsureHostActionsKey(ctx, changed, raw); err == nil {
				t.Fatal("existing key rebound to changed authority")
			}
		})
	}
	var keys, audit int
	if err = s.Pool().QueryRow(ctx, "SELECT (SELECT count(*) FROM api_keys),(SELECT count(*) FROM audit_events WHERE action='host-actions-key.create')").Scan(&keys, &audit); err != nil || keys != 1 || audit != 1 {
		t.Fatal("unexpected credential or audit count", keys, audit, err)
	}
}

func TestHostActionsRenewalNeverRevivesRevocationOrRemovedOwnership(t *testing.T) {
	s, grant, _ := hostActionsFixture(t)
	ctx := context.Background()
	key, raw, err := s.EnsureHostActionsKey(ctx, grant, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool().Exec(ctx, "UPDATE api_keys SET expires_at=now()-interval '1 hour' WHERE id=$1", key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.VerifyHostActionsKey(ctx, grant, raw); err == nil {
		t.Fatal("read-only verification revived an expired key")
	}
	renewed, sameRaw, err := s.EnsureHostActionsKey(ctx, grant, raw)
	if err != nil || sameRaw != raw || renewed.ID != key.ID || renewed.ExpiresAt == nil || renewed.ExpiresAt.Before(time.Now().Add(89*24*time.Hour)) {
		t.Fatal("trusted recovery could not renew the retained key", err)
	}
	for _, change := range []string{"admin=false", "owner=false", "disabled=true", "email_verified=false"} {
		if _, err = s.Pool().Exec(ctx, "UPDATE identities SET "+change+" WHERE id=$1", grant.OwnerIdentity); err != nil {
			t.Fatal(err)
		}
		if _, err = s.VerifyHostActionsKey(ctx, grant, raw); err == nil {
			t.Fatal("removed owner authority still verified", change)
		}
		if _, _, err = s.EnsureHostActionsKey(ctx, grant, raw); err == nil {
			t.Fatal("removed owner authority renewed key", change)
		}
		if _, err = s.Pool().Exec(ctx, "UPDATE identities SET admin=true,owner=true,disabled=false,email_verified=true WHERE id=$1", grant.OwnerIdentity); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.Pool().Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", key.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.EnsureHostActionsKey(ctx, grant, raw); err == nil {
		t.Fatal("revoked host key was revived")
	}
	if err = s.store.Reauthorize(ctx, key.ID, grant.Project, grant.Environment, "recovery-runners"); err == nil {
		t.Fatal("worker reauthorized a revoked host key")
	}
}

func TestHostActionsRuntimeChecksExactResourceAndCanonicalAuthentication(t *testing.T) {
	s, grant, application := hostActionsFixture(t)
	ctx := context.Background()
	key, raw, err := s.EnsureHostActionsKey(ctx, grant, "")
	if err != nil {
		t.Fatal(err)
	}
	otherApp := store.NewID()
	other := application
	other.Name = "another-runners"
	if _, err = s.Pool().Exec(ctx, "INSERT INTO applications(id,name,project,environment,revision,status,spec) VALUES($1,$2,$3,$4,1,'ready',$5)", otherApp, other.Name, grant.Project, grant.Environment, store.JSON(other)); err != nil {
		t.Fatal(err)
	}
	operation := "recovery:" + store.NewID() + ":0:stop"
	foreignOperation := "recovery:" + store.NewID() + ":0:stop"
	ownedDeployment, foreignDeployment := store.NewID(), store.NewID()
	for _, fixture := range []struct{ id, app, operation string }{{ownedDeployment, grant.ApplicationID, operation}, {foreignDeployment, otherApp, foreignOperation}} {
		if _, err = s.Pool().Exec(ctx, "INSERT INTO deployments(id,application_id,identity_id,key_id,idempotency_key,request_hash,revision,status,spec) VALUES($1,$2,$3,$4,$5,$6,1,'queued',$7)", fixture.id, fixture.app, grant.OwnerIdentity, key.ID, fixture.operation, []byte("fixture"), store.JSON(application)); err != nil {
			t.Fatal(err)
		}
	}
	handler := (&api.Server{Store: s.store, Auth: s.config, OperatorRuntime: true, CloudControlPlane: true}).Handler()
	request := func(method, path string, headers bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, nil)
		if headers {
			r.Header.Set("X-Hakopod-Workspace", grant.Workspace)
			r.Header.Set("X-Hakopod-Recovery-Application", grant.ApplicationID)
		}
		r.Header.Set("Idempotency-Key", "recovery:"+strings.Repeat("a", 32)+":0:cancel")
		r.Header.Set("Cookie", "hakopod_session=must-not-be-forwarded")
		r.Header.Set("Authorization", "Bearer must-not-be-forwarded")
		w := httptest.NewRecorder()
		s.ServeHostActionsRuntime(handler, w, r, grant, raw)
		return w
	}
	for _, test := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/applications/" + grant.ApplicationID, 200},
		{"GET", "/deployments/" + ownedDeployment, 200},
		{"GET", "/idempotency/" + operation, 200},
		{"GET", "/idempotency/recovery:" + strings.Repeat("b", 32) + ":0:stop", 404},
		{"GET", "/applications/" + otherApp, 403},
		{"GET", "/deployments/" + foreignDeployment, 403},
		{"POST", "/deployments/" + foreignDeployment + "/cancel", 403},
		{"GET", "/idempotency/" + foreignOperation, 403},
		{"DELETE", "/applications/" + grant.ApplicationID, 403},
		{"POST", "/keys", 403},
		{"GET", "/applications/" + grant.ApplicationID + "?include=secrets", 403},
		{"POST", "/deployments/" + ownedDeployment + "/cancel", 202},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			w := request(test.method, test.path, true)
			if w.Code != test.status {
				t.Fatalf("status=%d, want=%d, body=%s", w.Code, test.status, w.Body.String())
			}
		})
	}
	if w := request("GET", "/applications/"+grant.ApplicationID, false); w.Code != 403 {
		t.Fatal("missing scope headers passed host authorization")
	}
	var state struct {
		CancelRequested bool `json:"cancel_requested"`
	}
	w := request("GET", "/deployments/"+ownedDeployment, true)
	if err = json.Unmarshal(w.Body.Bytes(), &state); err != nil || !state.CancelRequested {
		t.Fatal("canonical cancellation did not persist through the scoped key", err)
	}
	service := application.Services[grant.Service]
	service.Actions = nil
	application.Services[grant.Service] = service
	if _, err = s.Pool().Exec(ctx, "UPDATE applications SET spec=$2 WHERE id=$1", grant.ApplicationID, store.JSON(application)); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", "/applications/"+grant.ApplicationID, true); w.Code != http.StatusForbidden {
		t.Fatal("changed application type retained host maintenance authority")
	}
}

func TestHostActionsAuthorityIsUnavailableOutsideManagedCloud(t *testing.T) {
	s := &Service{}
	if _, _, err := s.EnsureHostActionsKey(context.Background(), HostActionsGrant{}, ""); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("self-hosted auth accepted a host issuer", err)
	}
}

func TestHostActionsStopRollbackReplayAndRevisionConflict(t *testing.T) {
	s, grant, application := hostActionsFixture(t)
	ctx := context.Background()
	s.store.ManagedCloud = true
	s.store.ActionsAccess = func(context.Context, string, string) error { return nil }
	key, raw, err := s.EnsureHostActionsKey(ctx, grant, "")
	if err != nil {
		t.Fatal(err)
	}
	initial := store.NewID()
	if _, err = s.Pool().Exec(ctx, "INSERT INTO deployments(id,application_id,identity_id,key_id,idempotency_key,request_hash,revision,status,spec,resolved_spec) VALUES($1,$2,$3,$4,'fixture-initial',$5,1,'succeeded',$6,$6)", initial, grant.ApplicationID, grant.OwnerIdentity, key.ID, []byte("fixture"), store.JSON(application)); err != nil {
		t.Fatal(err)
	}
	handler := (&api.Server{Store: s.store, Auth: s.config, OperatorRuntime: true, CloudControlPlane: true}).Handler()
	request := func(path, operation string, body any, status int) store.Deployment {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/v1/applications/"+grant.ApplicationID+path, bytes.NewReader(store.JSON(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Hakopod-Workspace", grant.Workspace)
		r.Header.Set("X-Hakopod-Recovery-Application", grant.ApplicationID)
		r.Header.Set("Idempotency-Key", operation)
		w := httptest.NewRecorder()
		s.ServeHostActionsRuntime(handler, w, r, grant, raw)
		if w.Code != status {
			t.Fatalf("%s returned %d, want %d: %s", path, w.Code, status, w.Body.String())
		}
		var deployment store.Deployment
		if status == http.StatusAccepted {
			if err := json.Unmarshal(w.Body.Bytes(), &deployment); err != nil {
				t.Fatal(err)
			}
			var persistedKey string
			if err := s.Pool().QueryRow(ctx, "SELECT key_id FROM deployments WHERE id=$1", deployment.ID).Scan(&persistedKey); err != nil || persistedKey != key.ID {
				t.Fatal("canonical operation lost its real reauthorization key", err)
			}
			if err := s.store.Reauthorize(ctx, persistedKey, grant.Project, grant.Environment, application.Name); err != nil {
				t.Fatal("worker cannot reauthorize accepted lifecycle operation", err)
			}
		}
		return deployment
	}
	prefix := "recovery:" + store.NewID() + ":0:"
	stopBody := map[string]int64{"expected_revision": 1}
	stopped := request("/services/runner/stop", prefix+"stop", stopBody, 202)
	if stopped.ApplicationID != grant.ApplicationID || stopped.Revision != 2 || !stopped.Spec.Services[grant.Service].Suspended {
		t.Fatal("canonical stop did not create the suspended revision")
	}
	if replay := request("/services/runner/stop", prefix+"stop", stopBody, 202); replay.ID != stopped.ID {
		t.Fatal("lost stop reply created a duplicate deployment")
	}
	request("/services/runner/stop", "recovery:"+store.NewID()+":0:stop", stopBody, 409)
	// The fixture completes only the durable operation; it makes no claim about
	// Kubernetes or GitHub runner drain acceptance, which is tested separately.
	if _, err = s.Pool().Exec(ctx, "UPDATE deployments SET status='succeeded',finished_at=now() WHERE id=$1", stopped.ID); err != nil {
		t.Fatal(err)
	}
	rollbackBody := map[string]int64{"revision": 1, "expected_revision": 2}
	restored := request("/rollback", prefix+"rollback", rollbackBody, 202)
	if restored.Revision != 3 || restored.Spec.Services[grant.Service].Suspended {
		t.Fatal("canonical rollback did not restore the original active specification")
	}
	if replay := request("/rollback", prefix+"rollback", rollbackBody, 202); replay.ID != restored.ID {
		t.Fatal("lost rollback reply created a duplicate deployment")
	}
	request("/rollback", "recovery:"+store.NewID()+":0:rollback", rollbackBody, 409)
	request("/rollback", prefix+"rollback", map[string]int64{"revision": 1, "expected_revision": 3}, 409)
	var count int
	if err = s.Pool().QueryRow(ctx, "SELECT count(*) FROM deployments WHERE application_id=$1", grant.ApplicationID).Scan(&count); err != nil || count != 3 {
		t.Fatal("replay or revision conflict changed durable operation count", count, err)
	}
}
