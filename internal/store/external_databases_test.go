package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/externaldatabase"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

func externalDatabaseFixture(t *testing.T) (*Store, Principal, externaldatabase.Resource) {
	t.Helper()
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	d := externaldatabase.Resource{ID: NewID(), Project: "demo", Environment: "development", Revision: 1, CredentialRevision: 1, Spec: externaldatabase.Spec{SchemaVersion: 1, Name: "external-development-fixture", Provider: "planetscale", Engine: "mysql", Host: "fixture.psdb.cloud", Port: 3306, Database: "app"}, Status: "ready", Observation: externaldatabase.Observation{ObservedAt: time.Now().UTC(), Revision: 1, Status: "ready", TLSVerified: true, QueryVerified: true, VerifiedIPs: []string{"8.8.8.8"}}, EncryptedCredentials: bytes.Repeat([]byte("x"), 40), CredentialDigest: bytes.Repeat([]byte("h"), 32)}
	return s, p, d
}

func seedLegacyExternalDatabase(t *testing.T, s *Store, d externaldatabase.Resource) {
	t.Helper()
	_, err := s.Pool.Exec(context.Background(), `INSERT INTO external_databases(id,project,environment,name,revision,credential_revision,spec,credentials,credential_digest,status,observation) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, d.ID, d.Project, d.Environment, d.Spec.Name, d.Revision, d.CredentialRevision, JSON(d.Spec), d.EncryptedCredentials, d.CredentialDigest, d.Status, JSON(d.Observation))
	if err != nil {
		t.Fatal(err)
	}
}

func assertExternalChangesUnavailable(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), externaldatabase.ErrNewChangesUnavailable.Error()) {
		t.Fatalf("expected the external connection unavailable error, got %v", err)
	}
}

func TestExternalDatabaseRejectsNewCreateUpdateAndBinding(t *testing.T) {
	s, p, d := externalDatabaseFixture(t)
	ctx := context.Background()

	_, err := s.AcceptExternalDatabase(ctx, p, d, 0, "external-create-disabled", "create", true)
	assertExternalChangesUnavailable(t, err)
	seedLegacyExternalDatabase(t, s, d)
	changed := d
	changed.Spec.Host = "other.psdb.cloud"
	_, err = s.AcceptExternalDatabase(ctx, p, changed, d.Revision, "external-update-disabled", "update", true)
	assertExternalChangesUnavailable(t, err)
	rotated := d
	rotated.EncryptedCredentials = bytes.Repeat([]byte("y"), 40)
	rotated.CredentialDigest = bytes.Repeat([]byte("z"), 32)
	if _, err = s.AcceptExternalDatabase(ctx, p, rotated, d.Revision, "external-rotation-allowed", "update", true); err != nil {
		t.Fatal("credential-only rotation was rejected", err)
	}

	app, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: "external-app-fixture", Services: map[string]spec.Service{"api": {Image: "nginx:alpine"}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, "external-app-create")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.PlanExternalDatabaseConnection(ctx, p, d.ID, first.ApplicationID, "api", "DATABASE_URL", false)
	assertExternalChangesUnavailable(t, err)

	app.Services["api"] = spec.Service{Image: "nginx:alpine", Bindings: map[string]spec.Binding{"DATABASE_URL": {ExternalDatabase: d.ID, ExternalDatabaseRevision: d.Revision, Protocol: "mysql"}}}
	_, err = s.Accept(ctx, p, d.Project, d.Environment, app, first.Revision, "external-direct-bind-disabled")
	assertExternalChangesUnavailable(t, err)
}

func TestHistoricalConnectReviewCannotBeConsumed(t *testing.T) {
	s, p, d := externalDatabaseFixture(t)
	ctx := context.Background()
	seedLegacyExternalDatabase(t, s, d)
	app, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: "historical-review-app", Services: map[string]spec.Service{"api": {Image: "nginx:alpine"}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, "historical-review-app-create")
	if err != nil {
		t.Fatal(err)
	}
	next := app
	next.Services["api"] = spec.Service{Image: "nginx:alpine", Bindings: map[string]spec.Binding{"DATABASE_URL": {ExternalDatabase: d.ID, ExternalDatabaseRevision: d.Revision, Protocol: "mysql"}}}
	plan := ExternalDatabaseConnectionPlan{ID: NewID(), Kind: "connect", DatabaseID: d.ID, DatabaseName: d.Spec.Name, DatabaseRevision: d.Revision, CredentialRevision: d.CredentialRevision, ApplicationID: first.ApplicationID, ApplicationName: app.Name, ApplicationRevision: first.Revision, Service: "api", Variable: "DATABASE_URL"}
	_, err = s.Pool.Exec(ctx, "INSERT INTO external_database_reviews(id,database_id,identity_id,revision,kind,payload,expires_at) VALUES($1,$2,$3,$4,$5,$6,now()+interval '10 minutes')", plan.ID, d.ID, p.ID, d.Revision, plan.Kind, JSON(externalDatabaseReview{Plan: plan, Spec: next}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AcceptExternalDatabaseConnection(ctx, p, d.ID, plan.ID, app.Name, "historical-connect-disabled")
	assertExternalChangesUnavailable(t, err)
}

func TestLegacyCredentialRefreshIsRevisionFenced(t *testing.T) {
	s, p, d := externalDatabaseFixture(t)
	ctx := context.Background()
	seedLegacyExternalDatabase(t, s, d)
	app, err := spec.Normalize(spec.Application{
		SchemaVersion: 1,
		Name:          "refresh-app",
		Env:           map[string]string{"APP_MODE": "legacy"},
		Services: map[string]spec.Service{
			"api":    {Image: "nginx:alpine", Args: []string{"-g", "daemon off;"}},
			"worker": {Image: "busybox:stable", Command: []string{"sh", "-c"}, Args: []string{"sleep 3600"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, "refresh-app-create")
	if err != nil {
		t.Fatal(err)
	}
	api := app.Services["api"]
	api.Bindings = map[string]spec.Binding{"DATABASE_URL": {ExternalDatabase: d.ID, ExternalDatabaseRevision: d.Revision, Protocol: "mysql"}}
	app.Services["api"] = api
	app, err = spec.Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE applications SET spec=$2 WHERE id=$1", first.ApplicationID, JSON(app)); err != nil {
		t.Fatal(err)
	}
	stale, err := s.PlanExternalDatabaseConnection(ctx, p, d.ID, first.ApplicationID, "api", "DATABASE_URL", false)
	if err != nil || stale.Kind != "refresh" || stale.Binding == nil || stale.Binding.ExternalDatabaseRevision != d.Revision {
		t.Fatal("existing binding could not be reviewed for credential refresh", err)
	}
	rotated := d
	rotated.EncryptedCredentials = bytes.Repeat([]byte("y"), 40)
	rotated.CredentialDigest = bytes.Repeat([]byte("z"), 32)
	rotation, err := s.AcceptExternalDatabase(ctx, p, rotated, d.Revision, "refresh-race-rotation", "update", true)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimExternalDatabaseOperation(ctx)
	if err != nil || claimed.ID != rotation.ID {
		t.Fatal("credential rotation operation was not claimable", err)
	}
	observation := externaldatabase.Observation{ObservedAt: time.Now().UTC(), Revision: claimed.Revision, Status: "ready", TLSVerified: true, QueryVerified: true, VerifiedIPs: []string{"8.8.8.8"}}
	if err = s.CompleteExternalDatabaseOperation(ctx, claimed, observation); err != nil {
		t.Fatal("credential rotation did not reach a verified ready state", err)
	}
	currentDatabase, err := s.ExternalDatabase(ctx, p, d.ID, true)
	if err != nil || currentDatabase.Revision != d.Revision+1 || currentDatabase.CredentialRevision != d.CredentialRevision+1 || currentDatabase.Observation.Revision != currentDatabase.Revision {
		t.Fatal("credential rotation did not publish the verified revision", err)
	}
	if _, err = s.AcceptExternalDatabaseConnection(ctx, p, d.ID, stale.ID, app.Name, "stale-refresh-review"); !errors.Is(err, ErrConflict) {
		t.Fatal("credential rotation did not invalidate an older refresh review", err)
	}
	var consumed bool
	if err = s.Pool.QueryRow(ctx, "SELECT consumed_at IS NOT NULL FROM external_database_reviews WHERE id=$1", stale.ID).Scan(&consumed); err != nil || consumed {
		t.Fatal("failed refresh review was consumed", err)
	}

	refresh, err := s.PlanExternalDatabaseConnection(ctx, p, d.ID, first.ApplicationID, "api", "DATABASE_URL", false)
	if err != nil || refresh.Binding == nil || refresh.Binding.ExternalDatabaseRevision != currentDatabase.Revision {
		t.Fatal("rotated credential revision could not be reviewed", err)
	}
	var expected spec.Application
	if err = json.Unmarshal(JSON(app), &expected); err != nil {
		t.Fatal(err)
	}
	expectedAPI := expected.Services["api"]
	expectedBinding := expectedAPI.Bindings["DATABASE_URL"]
	expectedBinding.ExternalDatabaseRevision = currentDatabase.Revision
	expectedAPI.Bindings["DATABASE_URL"] = expectedBinding
	expected.Services["api"] = expectedAPI
	expected, err = spec.Normalize(expected)
	if err != nil || !reflect.DeepEqual(*refresh.Binding, expectedBinding) {
		t.Fatal("refresh review did not preserve the saved binding", err)
	}

	var tampered spec.Application
	if err = json.Unmarshal(JSON(expected), &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.InjectEnv = !tampered.InjectEnv
	tamperedAPI := tampered.Services["api"]
	tamperedAPI.Bindings["SECOND_DATABASE_URL"] = expectedBinding
	tampered.Services["api"] = tamperedAPI
	tampered, err = spec.Normalize(tampered)
	if err != nil {
		t.Fatal(err)
	}
	tamperedPlan := refresh
	tamperedPlan.ID = NewID()
	if _, err = s.Pool.Exec(ctx, "INSERT INTO external_database_reviews(id,database_id,identity_id,revision,kind,payload,expires_at) VALUES($1,$2,$3,$4,$5,$6,now()+interval '10 minutes')", tamperedPlan.ID, d.ID, p.ID, currentDatabase.Revision, tamperedPlan.Kind, JSON(externalDatabaseReview{Plan: tamperedPlan, Spec: tampered})); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptExternalDatabaseConnection(ctx, p, d.ID, tamperedPlan.ID, app.Name, "tampered-refresh-review"); !errors.Is(err, ErrConflict) {
		t.Fatal("tampered historical refresh review changed the application", err)
	}
	beforeRefresh, err := s.Application(ctx, first.ApplicationID)
	if err != nil || !reflect.DeepEqual(beforeRefresh.Spec, app) {
		t.Fatal("rejected refresh review changed the saved application", err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT consumed_at IS NOT NULL FROM external_database_reviews WHERE id=$1", tamperedPlan.ID).Scan(&consumed); err != nil || consumed {
		t.Fatal("tampered refresh review was consumed", err)
	}

	deployed, err := s.AcceptExternalDatabaseConnection(ctx, p, d.ID, refresh.ID, app.Name, "verified-refresh-review")
	if err != nil {
		t.Fatal("verified credential refresh was rejected", err)
	}
	if !reflect.DeepEqual(deployed.Spec, expected) {
		t.Fatal("credential refresh changed fields beyond the saved binding revision")
	}
	refreshed, err := s.Application(ctx, first.ApplicationID)
	if err != nil || !reflect.DeepEqual(refreshed.Spec, expected) {
		t.Fatal("credential refresh did not preserve the application", err)
	}
	if _, exists := refreshed.Spec.Services["api"].Bindings["SECOND_DATABASE_URL"]; exists || refreshed.Spec.InjectEnv != app.InjectEnv {
		t.Fatal("credential refresh applied fields from the tampered review")
	}
	if err = s.Pool.QueryRow(ctx, "SELECT consumed_at IS NOT NULL FROM external_database_reviews WHERE id=$1", refresh.ID).Scan(&consumed); err != nil || !consumed {
		t.Fatal("successful refresh review was not consumed", err)
	}
}

func TestLegacyExternalDatabaseCanBeInspectedDisconnectedAndDeleted(t *testing.T) {
	s, p, d := externalDatabaseFixture(t)
	ctx := context.Background()
	seedLegacyExternalDatabase(t, s, d)
	if _, err := s.ExternalDatabase(ctx, p, d.ID, false); err != nil {
		t.Fatal("legacy connection was not inspectable", err)
	}

	app, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: "legacy-bound-app", Services: map[string]spec.Service{"api": {Image: "nginx:alpine"}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, "legacy-app-create")
	if err != nil {
		t.Fatal(err)
	}
	app.Services["api"] = spec.Service{Image: "nginx:alpine", Bindings: map[string]spec.Binding{"DATABASE_URL": {ExternalDatabase: d.ID, ExternalDatabaseRevision: d.Revision, Protocol: "mysql"}}}
	if _, err = s.Pool.Exec(ctx, "UPDATE applications SET spec=$2 WHERE id=$1", first.ApplicationID, JSON(app)); err != nil {
		t.Fatal(err)
	}
	disconnect, err := s.PlanExternalDatabaseConnection(ctx, p, d.ID, first.ApplicationID, "api", "DATABASE_URL", true)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := s.AcceptExternalDatabaseConnection(ctx, p, d.ID, disconnect.ID, app.Name, "legacy-reviewed-disconnect")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := removed.Spec.Services["api"].Bindings["DATABASE_URL"]; exists {
		t.Fatal("disconnect retained the legacy binding")
	}
	current, err := s.ExternalDatabaseInternal(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptExternalDatabase(ctx, p, current, current.Revision, "legacy-inflight-delete", "delete", false); !errors.Is(err, ErrConflict) {
		t.Fatal("in-flight disconnect did not block deletion", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", removed.ID); err != nil {
		t.Fatal(err)
	}
	stale := disconnect
	stale.ID = NewID()
	stale.ApplicationRevision = removed.Revision
	if _, err = s.Pool.Exec(ctx, "INSERT INTO external_database_reviews(id,database_id,identity_id,revision,kind,payload,expires_at) VALUES($1,$2,$3,$4,$5,$6,now()-interval '1 minute')", stale.ID, d.ID, p.ID, d.Revision, stale.Kind, JSON(externalDatabaseReview{Plan: stale, Spec: removed.Spec})); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Accept(ctx, p, d.Project, d.Environment, removed.Spec, removed.Revision, "legacy-unrelated-deploy"); err != nil {
		t.Fatal("an unrelated deployment after disconnect was rejected", err)
	}
	if _, err = s.AcceptExternalDatabase(ctx, p, current, current.Revision, "legacy-unbound-delete", "delete", false); err != nil {
		t.Fatal("completed disconnect or stale unconsumed review did not release deletion", err)
	}
}

func TestLegacyExternalDatabaseStillHonorsScope(t *testing.T) {
	s, p, d := externalDatabaseFixture(t)
	seedLegacyExternalDatabase(t, s, d)
	appKey := p
	appKey.Application = "app-only"
	if _, err := s.ExternalDatabase(context.Background(), appKey, d.ID, false); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("application key read an independent legacy connection", err)
	}
}
