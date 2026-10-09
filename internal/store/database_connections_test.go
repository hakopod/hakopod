package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestDatabaseConnectionReviewAndGrantLifecycle(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "create-connection-fixture", "create"); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, claim, database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now().UTC()}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: "connection-fixture", Services: map[string]spec.Service{"api": {Image: "nginx:alpine", Env: map[string]string{"DATABASE_URL": "previous-connection"}, DatabaseClientProfiles: map[string]string{"DATABASE_URL": spec.DatabaseClientLibpqURLV1}}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, "create-app-fixture")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanDatabaseConnection(ctx, p, d.ID, first.ApplicationID, "api", "DATABASE_URL", "read_write", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.PreviousKind != "environment value" {
		t.Fatal("replacement not described")
	}
	appKey := p
	appKey.Application = app.Name
	if _, err = s.AcceptDatabaseConnection(ctx, appKey, d.ID, plan.ID, app.Name, "app-key-cutover"); err == nil {
		t.Fatal("application key acquired database grant")
	}
	if _, err = s.AcceptDatabaseConnection(ctx, p, d.ID, plan.ID, "wrong-name", "wrong-confirmation"); err == nil {
		t.Fatal("wrong confirmation accepted")
	}
	release, err := s.AcceptDatabaseConnection(ctx, p, d.ID, plan.ID, app.Name, "reviewed-cutover")
	if err != nil {
		t.Fatal(err)
	}
	if release.Revision != 2 || release.Spec.Services["api"].Bindings["DATABASE_URL"].ManagedDatabase != d.ID {
		t.Fatal("cutover not stored")
	}
	if _, ok := release.Spec.Services["api"].Env["DATABASE_URL"]; ok {
		t.Fatal("old environment value retained")
	}
	if release.Spec.Services["api"].DatabaseClientProfiles["DATABASE_URL"] != spec.DatabaseClientLibpqURLV1 {
		t.Fatal("database client profile was not retained")
	}
	replay, err := s.AcceptDatabaseConnection(ctx, p, d.ID, plan.ID, app.Name, "reviewed-cutover")
	if err != nil || replay.ID != release.ID {
		t.Fatal("idempotent cutover", err)
	}
	if _, err = s.AcceptDatabaseConnection(ctx, p, d.ID, plan.ID, app.Name, "reused-cutover"); !errors.Is(err, ErrConflict) {
		t.Fatal("review reused", err)
	}
	if _, err = s.AcceptDatabase(ctx, p, d, 1, "delete-bound-database", "delete"); !errors.Is(err, ErrConflict) {
		t.Fatal("bound database deleted", err)
	}
	if _, err = s.Accept(ctx, appKey, d.Project, d.Environment, release.Spec, 2, "reuse-approved-binding"); err != nil {
		t.Fatal("application key could not reuse grant", err)
	}
	// A changed endpoint is a changed grant even if it names the same database.
	changed := release.Spec
	svc := changed.Services["api"]
	b := svc.Bindings["DATABASE_URL"]
	b.Endpoint = "read_only"
	svc.Bindings["DATABASE_URL"] = b
	changed.Services["api"] = svc
	if _, err = s.Accept(ctx, appKey, d.Project, d.Environment, changed, 3, "change-approved-binding"); !errors.Is(err, ErrForbidden) {
		t.Fatal("application key changed grant", err)
	}
}

func TestDatabaseConnectionReviewRejectsUnsupportedManagedPrivateCAClient(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	d.Spec.Engine = "redis"
	d.Spec.Version = "8"
	d.Spec = d.Spec.WithSecureDefaults()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "create-redis-profile-fixture", "create"); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, claim, database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now().UTC()}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: "glitchtip-profile-fixture", Services: map[string]spec.Service{"main": {
		Image:                  "glitchtip/glitchtip:6.1.0",
		Env:                    map[string]string{"VALKEY_URL": "redis://valkey:6379"},
		DatabaseClientProfiles: map[string]string{"VALKEY_URL": spec.DatabaseClientGlitchTipValkeyV21},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, "create-glitchtip-profile-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PlanDatabaseConnection(ctx, p, d.ID, accepted.ApplicationID, "main", "VALKEY_URL", "read_write", false); !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), "cannot load the private CA") {
		t.Fatalf("unsupported managed private-CA client was not rejected: %v", err)
	}
}

func TestDatabaseRecoveryInspectionAndStaleConnectionReview(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "create-inspection-fixture", "create"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	recovery := database.Recovery{JobID: NewID(), ArtifactID: NewID(), CapturedAt: &now, RestoredAt: &now}
	_, err := s.Pool.Exec(ctx, "UPDATE managed_databases SET status='ready',observation=$2,recovery=$3 WHERE id=$1", d.ID, JSON(database.Observation{Status: "ready", Revision: 1}), JSON(recovery))
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: "inspect-fixture", Services: map[string]spec.Service{"api": {Image: "nginx:alpine"}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, "create-inspection-app")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PlanDatabaseConnection(ctx, p, d.ID, first.ApplicationID, "api", "DATABASE_URL", "read_write", false); !errors.Is(err, ErrConflict) {
		t.Fatal("uninspected recovery connected", err)
	}
	if _, err = s.InspectDatabaseRecovery(ctx, p, d.ID, "wrong-job", d.Spec.Name, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong recovery attested", err)
	}
	inspected, err := s.InspectDatabaseRecovery(ctx, p, d.ID, recovery.JobID, d.Spec.Name, 1)
	if err != nil || inspected.Recovery.InspectedAt == nil {
		t.Fatal("inspection", err)
	}
	plan, err := s.PlanDatabaseConnection(ctx, p, d.ID, first.ApplicationID, "api", "DATABASE_URL", "read_write", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_databases SET revision=2 WHERE id=$1", d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptDatabaseConnection(ctx, p, d.ID, plan.ID, app.Name, "stale-database-cutover"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale database accepted", err)
	}
	current, err := s.Application(ctx, first.ApplicationID)
	if err != nil || current.Revision != 1 {
		t.Fatal("failed review changed application", err)
	}
	var consumed bool
	if err = s.Pool.QueryRow(ctx, "SELECT consumed_at IS NOT NULL FROM managed_database_reviews WHERE id=$1", plan.ID).Scan(&consumed); err != nil || consumed {
		t.Fatal("failed review consumed", err)
	}
}
