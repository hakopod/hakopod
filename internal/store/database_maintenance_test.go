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
	acceptCtx, cancelAccept := context.WithTimeout(ctx, 5*time.Second)
	defer cancelAccept()
	var accepted database.Operation
	acceptedResult := make(chan error, 1)
	go func() {
		var acceptErr error
		accepted, acceptErr = s.AcceptDatabase(acceptCtx, p, d, 1, "maintenance-delete-fixture", "delete")
		acceptedResult <- acceptErr
	}()
	select {
	case err = <-acceptedResult:
		t.Fatal("delete returned while maintenance was active", err)
	case <-time.After(350 * time.Millisecond):
	}
	var acceptedCount int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM managed_database_operations WHERE identity_id=$1 AND idempotency_key=$2", p.ID, "maintenance-delete-fixture").Scan(&acceptedCount); err != nil || acceptedCount != 0 {
		t.Fatal("blocked delete left an operation", acceptedCount, err)
	}
	fresh.Release()
	if err = <-acceptedResult; err != nil {
		t.Fatal("released database remained blocked", err)
	}
	if accepted.Kind != "delete" || accepted.Revision != 2 {
		t.Fatal("released database accepted an unexpected operation", accepted.Kind, accepted.Revision)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM managed_database_operations WHERE identity_id=$1 AND idempotency_key=$2", p.ID, "maintenance-delete-fixture").Scan(&acceptedCount); err != nil || acceptedCount != 1 {
		t.Fatal("released delete was not accepted exactly once", acceptedCount, err)
	}
}

func TestDatabaseLifecycleMaintenanceWaitStopsOnRevisionChangeAndCancellation(t *testing.T) {
	t.Run("revision change", func(t *testing.T) {
		s, p, d := databaseFixture(t)
		ctx := context.Background()
		if _, err := s.AcceptDatabase(ctx, p, d, 0, "maintenance-revision-create", "create"); err != nil {
			t.Fatal(err)
		}
		op, err := s.ClaimDatabaseOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.RecordDatabaseStep(ctx, op, database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now()}, "succeeded", "ready", ""); err != nil {
			t.Fatal(err)
		}
		claim, err := s.ClaimDatabaseMaintenance(ctx, d.ID, 1)
		if err != nil || claim == nil {
			t.Fatal("maintenance claim failed", err)
		}
		defer claim.Release()

		d.Revision = 1
		done := make(chan error, 1)
		go func() {
			_, acceptErr := s.AcceptDatabase(ctx, p, d, 1, "maintenance-revision-delete", "delete")
			done <- acceptErr
		}()
		select {
		case err = <-done:
			t.Fatal("delete returned while maintenance was active", err)
		case <-time.After(350 * time.Millisecond):
		}
		if _, err = s.Pool.Exec(ctx, "UPDATE managed_databases SET revision=revision+1 WHERE id=$1", d.ID); err != nil {
			t.Fatal(err)
		}
		if err = <-done; !errors.Is(err, ErrConflict) || errors.Is(err, errDatabaseMaintenanceActive) {
			t.Fatal("revision change did not stop maintenance retries", err)
		}
	})

	t.Run("caller cancellation", func(t *testing.T) {
		s, p, d := databaseFixture(t)
		ctx := context.Background()
		if _, err := s.AcceptDatabase(ctx, p, d, 0, "maintenance-cancel-create", "create"); err != nil {
			t.Fatal(err)
		}
		op, err := s.ClaimDatabaseOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.RecordDatabaseStep(ctx, op, database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now()}, "succeeded", "ready", ""); err != nil {
			t.Fatal(err)
		}
		claim, err := s.ClaimDatabaseMaintenance(ctx, d.ID, 1)
		if err != nil || claim == nil {
			t.Fatal("maintenance claim failed", err)
		}
		defer claim.Release()

		d.Revision = 1
		acceptCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		if _, err = s.AcceptDatabase(acceptCtx, p, d, 1, "maintenance-cancel-delete", "delete"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("caller deadline was masked", err)
		}
	})
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

func TestDueManagedBackupConflictDoesNotBlockOtherSchedules(t *testing.T) {
	s, p, blocked := databaseFixture(t)
	ctx := context.Background()
	readyBefore := blocked
	readyBefore.ID = NewID()
	readyBefore.Spec.Name = "ready-before-backup-schedule-fixture"
	readyAfter := blocked
	readyAfter.ID = NewID()
	readyAfter.Spec.Name = "ready-after-backup-schedule-fixture"
	for index, item := range []database.Resource{blocked, readyBefore, readyAfter} {
		if _, err := s.AcceptDatabase(ctx, p, item, 0, "scheduled-backup-create-"+item.ID, "create"); err != nil {
			t.Fatal(err)
		}
		op, err := s.ClaimDatabaseOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.RecordDatabaseStep(ctx, op, database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now()}, "succeeded", "ready", ""); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			if _, err = s.Pool.Exec(ctx, "UPDATE managed_databases SET maintenance_lease=$2,maintenance_lease_until=now()+interval '10 minutes' WHERE id=$1", item.ID, NewID()); err != nil {
				t.Fatal(err)
			}
		}
	}
	destinationID := NewID()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO backup_destinations(id,name,revision,config,credentials) VALUES($1,'scheduler isolation',1,$2,$3)", destinationID, JSON(backup.Destination{ID: destinationID, Name: "scheduler isolation"}), []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	scheduleIDs := map[string]string{}
	for index, item := range []database.Resource{readyBefore, blocked, readyAfter} {
		scheduleID := NewID()
		source := backup.Source{Kind: "managed_database", ManagedDatabaseID: item.ID, Engine: item.Spec.Engine}
		if _, err := s.Pool.Exec(ctx, "INSERT INTO backup_schedules(id,name,destination_id,source,interval_hours,retention_count,enabled,revision,next_run_at,identity_id) VALUES($1,$2,$3,$4,24,2,true,1,$5,$6)", scheduleID, item.Spec.Name, destinationID, JSON(source), now.Add(-time.Duration(3-index)*time.Hour), p.ID); err != nil {
			t.Fatal(err)
		}
		scheduleIDs[item.Spec.Name] = scheduleID
	}
	if err := s.QueueDueBackups(ctx); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	var next time.Time
	if err := s.Pool.QueryRow(ctx, "SELECT enabled,next_run_at FROM backup_schedules WHERE id=$1", scheduleIDs[blocked.Spec.Name]).Scan(&enabled, &next); err != nil || !enabled || !next.After(now) || next.After(time.Now().Add(2*time.Minute)) {
		t.Fatal("temporarily blocked schedule was not deferred", enabled, next, err)
	}
	var jobs int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM backup_jobs WHERE schedule_id IN ($1,$2)", scheduleIDs[readyBefore.Spec.Name], scheduleIDs[readyAfter.Spec.Name]).Scan(&jobs); err != nil || jobs != 2 {
		t.Fatal("ready schedules around the blocked schedule did not both enqueue", jobs, err)
	}
}
