package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
)

func TestDatabaseOperationProgressPersistsAndReplacesCurrentStage(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "progress-create", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observation := database.Observation{Status: "pending", Revision: 1, ObservedAt: time.Now().UTC()}
	if err = s.RecordDatabaseStep(ctx, op, observation, "queued", "replica-replacement", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimDatabaseOperation(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("a queued stage bypassed its retry delay: %v", err)
	}
	makeDue := func() {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, "UPDATE managed_database_operations SET next_attempt_at=now()-interval '1 second' WHERE id=$1", op.ID); err != nil {
			t.Fatal(err)
		}
	}
	makeDue()
	op, err = s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, op, observation, "queued", "replica-replacement", "updated"); err != nil {
		t.Fatal(err)
	}
	makeDue()
	op, err = s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observation.Status = "ready"
	if err = s.RecordDatabaseStep(ctx, op, observation, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	stored, err := s.DatabaseOperation(ctx, p, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Progress) != 2 || stored.Progress[0].ID != "replica-replacement" || stored.Progress[0].State != "succeeded" || stored.Progress[0].Message != "updated" || stored.Progress[1].ID != "ready" || stored.Progress[1].State != "succeeded" {
		t.Fatal(stored.Progress)
	}
}

func TestDatabaseOperationFailureDoesNotCompletePendingStage(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "progress-failure", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observation := database.Observation{Status: "pending", Revision: 1, ObservedAt: time.Now().UTC()}
	if err = s.RecordDatabaseStep(ctx, op, observation, "queued", "replica-replacement", "Waiting for the replacement member."); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_database_operations SET next_attempt_at=now()-interval '1 second' WHERE id=$1", op.ID); err != nil {
		t.Fatal(err)
	}
	op, err = s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, op, observation, "failed", "authorization", "Authority changed before replacement completed."); err != nil {
		t.Fatal(err)
	}
	stored, err := s.DatabaseOperation(ctx, p, op.ID)
	if err != nil || len(stored.Progress) != 2 {
		t.Fatal("failure progress was not retained", err)
	}
	if stored.Progress[0].State != "failed" || stored.Progress[1].State != "failed" {
		t.Fatal("an unfinished stage was reported as completed")
	}
}
