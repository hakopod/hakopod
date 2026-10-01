package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/database"
)

func TestDatabaseMaintenanceFencesLifecycleAndExpiredWorkers(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "maintenance-create-fixture", "create"); err != nil {
		t.Fatal(err)
	}
	if claim, err := s.ClaimDatabaseMaintenance(ctx, d.ID, 1); err != nil || claim != nil {
		t.Fatal("pending database maintained", err)
	}
	op, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, op, database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now()}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "SELECT id FROM managed_databases WHERE id=$1 FOR UPDATE", d.ID); err != nil {
		t.Fatal(err)
	}
	if claim, err := s.ClaimDatabaseMaintenance(ctx, d.ID, 1); err != nil || claim != nil {
		t.Fatal("maintenance raced a locked acceptance", err)
	}
	_ = tx.Rollback(ctx)
	claim, err := s.ClaimDatabaseMaintenance(ctx, d.ID, 1)
	if err != nil || claim == nil {
		t.Fatal("ready database claim failed", err)
	}
	if err = claim.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if second, err := s.ClaimDatabaseMaintenance(ctx, d.ID, 1); err != nil || second != nil {
		t.Fatal("concurrent maintenance accepted", err)
	}
	d.Revision = 1
	if _, err = s.AcceptDatabase(ctx, p, d, 1, "maintenance-delete-fixture", "delete"); !errors.Is(err, ErrConflict) {
		t.Fatal("delete raced maintenance", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_databases SET maintenance_lease_until=now()-interval '1 second' WHERE id=$1", d.ID); err != nil {
		t.Fatal(err)
	}
	if err = claim.Check(ctx); !errors.Is(err, ErrClaimLost) {
		t.Fatal("expired worker still authorized", err)
	}
	fresh, err := s.ClaimDatabaseMaintenance(ctx, d.ID, 1)
	if err != nil || fresh == nil {
		t.Fatal("expired claim did not recover", err)
	}
	claim.Release()
	if err = fresh.Check(ctx); err != nil {
		t.Fatal("old worker released new claim", err)
	}
	fresh.Release()
	if _, err = s.AcceptDatabase(ctx, p, d, 1, "maintenance-delete-fixture", "delete"); err != nil {
		t.Fatal("released database remained blocked", err)
	}
}

func TestVitessNativeRevocationContinuesDuringArchiveAndAfterFailure(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "native-maintenance-fixture", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, op, database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now()}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	if claim, err := s.ClaimDatabaseNativeStorageMaintenance(ctx, d.ID, 1); err != nil || claim != nil {
		t.Fatal("native storage claim applied to another engine", err)
	}
	// Store-only fixture: exercise lease authority without claiming native readiness.
	if _, err = s.Pool.Exec(ctx, `UPDATE managed_databases SET spec=jsonb_set(spec,'{engine}','"vitess"'),observation='{}' WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	jobID := NewID()
	queuedBackupJob(t, s, jobID)
	if _, err = s.Pool.Exec(ctx, "UPDATE backup_jobs SET source=$2 WHERE id=$1", jobID, JSON(backup.Source{Kind: "managed_database", ManagedDatabaseID: d.ID, Engine: "vitess"})); err != nil {
		t.Fatal(err)
	}
	if claim, err := s.ClaimDatabaseMaintenance(ctx, d.ID, 1); err != nil || claim != nil {
		t.Fatal("identity maintenance crossed an active archive", err)
	}
	claim, err := s.ClaimDatabaseNativeStorageMaintenance(ctx, d.ID, 1)
	if err != nil || claim == nil {
		t.Fatal("archive starved native credential revocation", err)
	}
	if err = claim.Check(ctx); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_databases SET status='failed' WHERE id=$1", d.ID); err != nil {
		t.Fatal(err)
	}
	failed, lease, err := s.ClaimDatabaseObservation(ctx)
	if err != nil || failed.ID != d.ID {
		t.Fatal("failed Vitess disappeared from authority reconciliation", err)
	}
	defer s.ReleaseDatabaseObservation(d.ID, lease)
	claim, err = s.ClaimDatabaseNativeStorageMaintenance(ctx, d.ID, 1)
	if err != nil || claim == nil {
		t.Fatal("failed creation prevented credential cleanup", err)
	}
	if err = claim.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.ObserveClaimedDatabase(ctx, d.ID, 1, lease, database.Observation{Status: "unknown", Revision: 1, ObservedAt: time.Now()}); err != nil {
		t.Fatal("failed resource observation lost its lease", err)
	}
	claim.Release()
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_database_operations SET status='queued' WHERE id=$1", op.ID); err != nil {
		t.Fatal(err)
	}
	if claim, err := s.ClaimDatabaseNativeStorageMaintenance(ctx, d.ID, 1); err != nil || claim != nil {
		t.Fatal("native cleanup crossed an active lifecycle operation", err)
	}
}
