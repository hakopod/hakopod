package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestManagedRestoreReviewsRejectSavedAndDeployedConnections(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "restore-target-fixture", "create"); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, claim, database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now().UTC()}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	destination, err := s.PutBackupDestination(ctx, p, backup.Destination{ID: NewID(), Name: "restore-review-fixture", EncryptedCredentials: []byte("sealed-test-fixture"), EncryptionRecipient: "age-fixture"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	capture := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	source := backup.ImportSpec{DestinationID: destination.ID, SourceName: "docker-restore-fixture", Engine: "postgresql", SourceVersion: "17", CapturedAt: capture, Bytes: 200, SHA256: strings.Repeat("a", 64)}
	imported, err := s.PrepareBackupImport(ctx, p, source, "restore-import-fixture")
	if err != nil {
		t.Fatal(err)
	}
	imported, err = s.ClaimBackupImport(ctx, p, imported.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	artifact := backup.Artifact{ID: imported.ID, JobID: imported.ID, DestinationID: destination.ID, Source: backup.Source{Kind: "docker_import", Engine: "postgresql", ExternalName: source.SourceName}, SourceVersion: "17", Bytes: 300, SHA256: strings.Repeat("b", 64), ObjectKey: "import/" + imported.ID + ".age", Format: "age-v1+postgresql-custom", CapturedAt: &capture, VerifiedAt: &now}
	if err = s.FinishBackupImport(ctx, p, imported, artifact); err != nil {
		t.Fatal(err)
	}
	target := backup.Target{Source: backup.Source{Kind: "managed_database", ManagedDatabaseID: d.ID, Engine: d.Spec.Engine}, ManagedDatabaseName: d.Spec.Name, Revision: 1, Available: true}
	plan := backup.RestorePlan{ID: NewID(), ArtifactID: artifact.ID, Target: target, Confirmation: d.Spec.Name, ExpiresAt: now.Add(10 * time.Minute)}
	if err = s.SaveBackupRestorePlan(ctx, p, plan); err != nil {
		t.Fatal("unused target rejected", err)
	}
	app, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: "restore-bound-fixture", Services: map[string]spec.Service{"api": {Image: "nginx:alpine", Bindings: map[string]spec.Binding{"DATABASE_URL": {ManagedDatabase: d.ID, Protocol: "postgres", Endpoint: "read_write"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, "restore-bound-app")
	if err != nil {
		t.Fatal(err)
	}
	reject := func(label string) {
		t.Helper()
		next := plan
		next.ID = NewID()
		if err = s.SaveBackupRestorePlan(ctx, p, next); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s review: %v", label, err)
		}
		if _, err = s.AcceptBackupRestore(ctx, p, artifact.ID, plan.ID, plan.Confirmation, "restore-race-fixture"); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s acceptance: %v", label, err)
		}
	}
	reject("saved connection")
	if _, err = s.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	svc := app.Services["api"]
	svc.Bindings = nil
	app.Services["api"] = svc
	second, err := s.Accept(ctx, p, d.Project, d.Environment, app, 1, "restore-unbound-app")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE deployments SET status='failed' WHERE id=$1", second.ID); err != nil {
		t.Fatal(err)
	}
	reject("previous deployment after failed replacement")
	if _, err = s.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", second.ID); err != nil {
		t.Fatal(err)
	}
	fresh := plan
	fresh.ID = NewID()
	if err = s.SaveBackupRestorePlan(ctx, p, fresh); err != nil {
		t.Fatal("released target rejected", err)
	}
	if _, err = s.AcceptBackupRestore(ctx, p, artifact.ID, fresh.ID, fresh.Confirmation, "restore-unused-fixture"); err != nil {
		t.Fatal("unused target recovery rejected", err)
	}
}
