package store

import (
	"context"
	"github.com/hakopod/hakopod/internal/database"
	"testing"
	"time"
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
	op, err = s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, op, observation, "queued", "replica-replacement", "updated"); err != nil {
		t.Fatal(err)
	}
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
