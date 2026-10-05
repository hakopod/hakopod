package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/database"
)

type backupMaintenanceFixture struct {
	store       *Store
	principal   Principal
	database    database.Resource
	destination backup.Destination
}

func newBackupMaintenanceFixture(t *testing.T) backupMaintenanceFixture {
	t.Helper()
	s, p, item := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, item, 0, "backup-maintenance-database-"+item.ID, "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, op, database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now().UTC()}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	item, err = s.Database(ctx, p, item.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := s.PutBackupDestination(ctx, p, backup.Destination{ID: NewID(), Name: "maintenance-wait-fixture", EncryptedCredentials: []byte("sealed-test-fixture")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return backupMaintenanceFixture{store: s, principal: p, database: item, destination: destination}
}

func (f backupMaintenanceFixture) backupJob() backup.Job {
	return backup.Job{
		Kind:          "backup",
		DestinationID: f.destination.ID,
		Source: backup.Source{
			Kind:              "managed_database",
			ManagedDatabaseID: f.database.ID,
			Engine:            f.database.Spec.Engine,
		},
	}
}

func (f backupMaintenanceFixture) restoreArtifact(t *testing.T) backup.Artifact {
	t.Helper()
	ctx := context.Background()
	captured := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	spec := backup.ImportSpec{
		DestinationID: f.destination.ID,
		SourceName:    "maintenance_restore_source",
		Engine:        f.database.Spec.Engine,
		SourceVersion: f.database.Spec.Version,
		CapturedAt:    captured,
		Bytes:         200,
		SHA256:        strings.Repeat("a", 64),
	}
	item, err := f.store.PrepareBackupImport(ctx, f.principal, spec, "backup-maintenance-import-"+f.database.ID)
	if err != nil {
		t.Fatal(err)
	}
	item, err = f.store.ClaimBackupImport(ctx, f.principal, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	verified := time.Now().UTC()
	artifact := backup.Artifact{
		ID:            item.ID,
		JobID:         item.ID,
		DestinationID: f.destination.ID,
		Source:        backup.Source{Kind: "docker_import", Engine: f.database.Spec.Engine, ExternalName: spec.SourceName},
		SourceVersion: f.database.Spec.Version,
		Bytes:         300,
		SHA256:        strings.Repeat("b", 64),
		ObjectKey:     "import/" + item.ID + ".age",
		Format:        "age-v1+postgresql-custom",
		CapturedAt:    &captured,
		VerifiedAt:    &verified,
	}
	if err = f.store.FinishBackupImport(ctx, f.principal, item, artifact); err != nil {
		t.Fatal(err)
	}
	return artifact
}

func (f backupMaintenanceFixture) restorePlan(artifact backup.Artifact) backup.RestorePlan {
	target := backup.Target{
		Source: backup.Source{
			Kind:              "managed_database",
			ManagedDatabaseID: f.database.ID,
			Engine:            f.database.Spec.Engine,
		},
		ManagedDatabaseName: f.database.Spec.Name,
		Revision:            f.database.Revision,
		Available:           true,
	}
	return backup.RestorePlan{
		ID:           NewID(),
		ArtifactID:   artifact.ID,
		Target:       target,
		Confirmation: backup.RestoreConfirmation(target),
		ExpiresAt:    time.Now().Add(10 * time.Minute).UTC(),
	}
}

func assertStillWaiting(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatal("request returned while database maintenance was active", err)
	case <-time.After(350 * time.Millisecond):
	}
}

func TestBackupAdmissionsWaitForDatabaseMaintenanceWithoutDuplicates(t *testing.T) {
	t.Run("enqueue backup", func(t *testing.T) {
		f := newBackupMaintenanceFixture(t)
		claim, err := f.store.ClaimDatabaseMaintenance(context.Background(), f.database.ID, f.database.Revision)
		if err != nil || claim == nil {
			t.Fatal("maintenance claim failed", err)
		}
		defer claim.Release()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var accepted backup.Job
		done := make(chan error, 1)
		go func() {
			var callErr error
			accepted, callErr = f.store.EnqueueBackup(ctx, f.principal, f.backupJob(), "maintenance-wait-enqueue")
			done <- callErr
		}()
		assertStillWaiting(t, done)
		var count int
		if err = f.store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM backup_jobs WHERE identity_id=$1 AND idempotency_key=$2", f.principal.ID, "maintenance-wait-enqueue").Scan(&count); err != nil || count != 0 {
			t.Fatal("blocked enqueue left a job", count, err)
		}
		claim.Release()
		if err = <-done; err != nil {
			t.Fatal("enqueue did not recover after maintenance", err)
		}
		replay, err := f.store.EnqueueBackup(context.Background(), f.principal, f.backupJob(), "maintenance-wait-enqueue")
		if err != nil || replay.ID != accepted.ID {
			t.Fatal("enqueue retry did not return the accepted request", replay.ID, accepted.ID, err)
		}
		if err = f.store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM backup_jobs WHERE identity_id=$1 AND idempotency_key=$2", f.principal.ID, "maintenance-wait-enqueue").Scan(&count); err != nil || count != 1 {
			t.Fatal("enqueue produced duplicate jobs", count, err)
		}
		// This store fixture completes the job without invoking a database runtime.
		if _, err = f.store.Pool.Exec(context.Background(), "UPDATE backup_jobs SET status='succeeded',finished_at=now() WHERE id=$1", accepted.ID); err != nil {
			t.Fatal(err)
		}
		nextClaim, err := f.store.ClaimDatabaseMaintenance(context.Background(), f.database.ID, f.database.Revision)
		if err != nil || nextClaim == nil {
			t.Fatal("later maintenance claim failed", err)
		}
		defer nextClaim.Release()
		replayCtx, cancelReplay := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelReplay()
		replay, err = f.store.EnqueueBackup(replayCtx, f.principal, f.backupJob(), "maintenance-wait-enqueue")
		if err != nil || replay.ID != accepted.ID {
			t.Fatal("later maintenance hid the committed backup receipt", err)
		}
		if err = f.store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM backup_jobs WHERE identity_id=$1 AND idempotency_key=$2", f.principal.ID, "maintenance-wait-enqueue").Scan(&count); err != nil || count != 1 {
			t.Fatal("receipt replay during maintenance duplicated the job", count, err)
		}
	})

	t.Run("save restore plan", func(t *testing.T) {
		f := newBackupMaintenanceFixture(t)
		artifact := f.restoreArtifact(t)
		plan := f.restorePlan(artifact)
		claim, err := f.store.ClaimDatabaseMaintenance(context.Background(), f.database.ID, f.database.Revision)
		if err != nil || claim == nil {
			t.Fatal("maintenance claim failed", err)
		}
		defer claim.Release()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- f.store.SaveBackupRestorePlan(ctx, f.principal, plan) }()
		assertStillWaiting(t, done)
		var count int
		if err = f.store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM backup_restore_plans WHERE id=$1", plan.ID).Scan(&count); err != nil || count != 0 {
			t.Fatal("blocked review left a restore plan", count, err)
		}
		claim.Release()
		if err = <-done; err != nil {
			t.Fatal("restore review did not recover after maintenance", err)
		}
		if err = f.store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM backup_restore_plans WHERE id=$1", plan.ID).Scan(&count); err != nil || count != 1 {
			t.Fatal("restore review was not saved exactly once", count, err)
		}
	})

	t.Run("accept restore", func(t *testing.T) {
		f := newBackupMaintenanceFixture(t)
		artifact := f.restoreArtifact(t)
		plan := f.restorePlan(artifact)
		if err := f.store.SaveBackupRestorePlan(context.Background(), f.principal, plan); err != nil {
			t.Fatal(err)
		}
		claim, err := f.store.ClaimDatabaseMaintenance(context.Background(), f.database.ID, f.database.Revision)
		if err != nil || claim == nil {
			t.Fatal("maintenance claim failed", err)
		}
		defer claim.Release()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var accepted backup.Job
		done := make(chan error, 1)
		go func() {
			var callErr error
			accepted, callErr = f.store.AcceptBackupRestore(ctx, f.principal, artifact.ID, plan.ID, plan.Confirmation, "maintenance-wait-restore")
			done <- callErr
		}()
		assertStillWaiting(t, done)
		var jobs int
		var used bool
		if err = f.store.Pool.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM backup_jobs WHERE identity_id=$1 AND idempotency_key=$2),used_at IS NOT NULL FROM backup_restore_plans WHERE id=$3", f.principal.ID, "maintenance-wait-restore", plan.ID).Scan(&jobs, &used); err != nil || jobs != 0 || used {
			t.Fatal("blocked restore left acceptance side effects", jobs, used, err)
		}
		claim.Release()
		if err = <-done; err != nil {
			t.Fatal("restore did not recover after maintenance", err)
		}
		replay, err := f.store.AcceptBackupRestore(context.Background(), f.principal, artifact.ID, plan.ID, plan.Confirmation, "maintenance-wait-restore")
		if err != nil || replay.ID != accepted.ID {
			t.Fatal("restore retry did not return the accepted request", replay.ID, accepted.ID, err)
		}
		if err = f.store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM backup_jobs WHERE identity_id=$1 AND idempotency_key=$2", f.principal.ID, "maintenance-wait-restore").Scan(&jobs); err != nil || jobs != 1 {
			t.Fatal("restore produced duplicate jobs", jobs, err)
		}
	})
}

func TestBackupMaintenanceWaitCancellationHasNoSideEffects(t *testing.T) {
	t.Run("enqueue backup", func(t *testing.T) {
		f := newBackupMaintenanceFixture(t)
		claim, err := f.store.ClaimDatabaseMaintenance(context.Background(), f.database.ID, f.database.Revision)
		if err != nil || claim == nil {
			t.Fatal("maintenance claim failed", err)
		}
		defer claim.Release()
		ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
		defer cancel()
		if _, err = f.store.EnqueueBackup(ctx, f.principal, f.backupJob(), "maintenance-cancel-enqueue"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("enqueue cancellation was not returned", err)
		}
		var count int
		if err = f.store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM backup_jobs WHERE identity_id=$1 AND idempotency_key=$2", f.principal.ID, "maintenance-cancel-enqueue").Scan(&count); err != nil || count != 0 {
			t.Fatal("cancelled enqueue left a job", count, err)
		}
	})

	t.Run("save restore plan", func(t *testing.T) {
		f := newBackupMaintenanceFixture(t)
		plan := f.restorePlan(f.restoreArtifact(t))
		claim, err := f.store.ClaimDatabaseMaintenance(context.Background(), f.database.ID, f.database.Revision)
		if err != nil || claim == nil {
			t.Fatal("maintenance claim failed", err)
		}
		defer claim.Release()
		ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
		defer cancel()
		if err = f.store.SaveBackupRestorePlan(ctx, f.principal, plan); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("review cancellation was not returned", err)
		}
		var count int
		if err = f.store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM backup_restore_plans WHERE id=$1", plan.ID).Scan(&count); err != nil || count != 0 {
			t.Fatal("cancelled review left a plan", count, err)
		}
	})

	t.Run("accept restore", func(t *testing.T) {
		f := newBackupMaintenanceFixture(t)
		artifact := f.restoreArtifact(t)
		plan := f.restorePlan(artifact)
		if err := f.store.SaveBackupRestorePlan(context.Background(), f.principal, plan); err != nil {
			t.Fatal(err)
		}
		claim, err := f.store.ClaimDatabaseMaintenance(context.Background(), f.database.ID, f.database.Revision)
		if err != nil || claim == nil {
			t.Fatal("maintenance claim failed", err)
		}
		defer claim.Release()
		ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
		defer cancel()
		if _, err = f.store.AcceptBackupRestore(ctx, f.principal, artifact.ID, plan.ID, plan.Confirmation, "maintenance-cancel-restore"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("restore cancellation was not returned", err)
		}
		var jobs int
		var used bool
		if err = f.store.Pool.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM backup_jobs WHERE identity_id=$1 AND idempotency_key=$2),used_at IS NOT NULL FROM backup_restore_plans WHERE id=$3", f.principal.ID, "maintenance-cancel-restore", plan.ID).Scan(&jobs, &used); err != nil || jobs != 0 || used {
			t.Fatal("cancelled restore left acceptance side effects", jobs, used, err)
		}
	})
}

func TestBackupMaintenanceWaitReturnsRealConflictsPromptly(t *testing.T) {
	t.Run("enqueue status conflict", func(t *testing.T) {
		f := newBackupMaintenanceFixture(t)
		claim, err := f.store.ClaimDatabaseMaintenance(context.Background(), f.database.ID, f.database.Revision)
		if err != nil || claim == nil {
			t.Fatal("maintenance claim failed", err)
		}
		defer claim.Release()
		if _, err = f.store.Pool.Exec(context.Background(), "UPDATE managed_databases SET status='failed' WHERE id=$1", f.database.ID); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err = f.store.EnqueueBackup(ctx, f.principal, f.backupJob(), "maintenance-stale-enqueue"); !errors.Is(err, backup.ErrConflict) || errors.Is(err, errDatabaseMaintenanceActive) {
			t.Fatal("stale backup source did not return its conflict", err)
		}
	})

	t.Run("restore review revision conflict", func(t *testing.T) {
		f := newBackupMaintenanceFixture(t)
		plan := f.restorePlan(f.restoreArtifact(t))
		plan.Target.Revision++
		claim, err := f.store.ClaimDatabaseMaintenance(context.Background(), f.database.ID, f.database.Revision)
		if err != nil || claim == nil {
			t.Fatal("maintenance claim failed", err)
		}
		defer claim.Release()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err = f.store.SaveBackupRestorePlan(ctx, f.principal, plan); !errors.Is(err, backup.ErrConflict) || errors.Is(err, errDatabaseMaintenanceActive) {
			t.Fatal("stale restore review did not return its conflict", err)
		}
	})

	t.Run("restore acceptance status conflict", func(t *testing.T) {
		f := newBackupMaintenanceFixture(t)
		artifact := f.restoreArtifact(t)
		plan := f.restorePlan(artifact)
		if err := f.store.SaveBackupRestorePlan(context.Background(), f.principal, plan); err != nil {
			t.Fatal(err)
		}
		claim, err := f.store.ClaimDatabaseMaintenance(context.Background(), f.database.ID, f.database.Revision)
		if err != nil || claim == nil {
			t.Fatal("maintenance claim failed", err)
		}
		defer claim.Release()
		if _, err = f.store.Pool.Exec(context.Background(), "UPDATE managed_databases SET status='failed' WHERE id=$1", f.database.ID); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err = f.store.AcceptBackupRestore(ctx, f.principal, artifact.ID, plan.ID, plan.Confirmation, "maintenance-stale-restore"); !errors.Is(err, backup.ErrConflict) || errors.Is(err, errDatabaseMaintenanceActive) {
			t.Fatal("stale restore target did not return its conflict", err)
		}
	})
}
