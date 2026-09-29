package store

import (
	"context"
	"errors"
	"testing"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

func TestEmptyApplicationDeletionKeepsCleanupButRemovesRetainedActionsHistory(t *testing.T) {
	db := isolatedDatabase(t)
	principal := bootstrapPrincipal(t, db)
	ctx := context.Background()
	initial := emptyTestSpec()
	deployment, err := db.Accept(ctx, principal, "demo", "development", initial, 0, "actions-history-initial")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", deployment.ID); err != nil {
		t.Fatal(err)
	}
	app, err := db.Application(ctx, deployment.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	// Development fixture: populate the durable runner lifecycle directly; no
	// provider registration or Kubernetes workload is created by this test.
	runners := spec.Application{SchemaVersion: 1, Name: initial.Name, Services: map[string]spec.Service{"runner": {Actions: &spec.Actions{Repository: "team/repo", Credential: "management", JobsCredential: "job-read"}}}}
	if err = db.SyncActions(ctx, app, runners, 1); err != nil {
		t.Fatal(err)
	}
	pools, err := db.ActionsPools(ctx, app.ID)
	if err != nil || len(pools) != 1 {
		t.Fatal("runner pool fixture", err)
	}
	slot, err := db.NewActionsSlot(ctx, pools[0])
	if err != nil {
		t.Fatal(err)
	}
	slot.RunnerID = 42
	if err = db.UpdateActionsSlot(ctx, slot.ID, 42, "cleanup"); err != nil {
		t.Fatal(err)
	}
	if err = db.RecordActionsJob(ctx, slot, actions.Observation{Repository: "team/repo", RunID: 9, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	empty, err := spec.Normalize(initial)
	if err != nil {
		t.Fatal(err)
	}
	empty.Services = map[string]spec.Service{}
	removal, err := db.Accept(ctx, principal, "demo", "development", empty, 1, "actions-history-empty")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", removal.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteEmptyApplication(ctx, principal, app.ID, 2, app.Name); !errors.Is(err, ErrConflict) {
		t.Fatal("desired runner pool did not block deletion", err)
	}
	if err = db.SyncActions(ctx, app, empty, 2); err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteEmptyApplication(ctx, principal, app.ID, 2, app.Name); !errors.Is(err, ErrConflict) {
		t.Fatal("pending provider cleanup did not block deletion", err)
	}
	if err = db.DeleteActionsSlot(ctx, slot.ID); err != nil {
		t.Fatal(err)
	}
	if jobs, err := db.ActionsJobs(ctx, app.ID, "runner"); err != nil || len(jobs) != 1 {
		t.Fatal("retained history fixture is missing", err)
	}
	if err = db.DeleteEmptyApplication(ctx, principal, app.ID, 2, app.Name); err != nil {
		t.Fatal("metadata-only runner history blocked confirmed application deletion", err)
	}
	if _, err = db.Application(ctx, app.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("deleted application remains", err)
	}
	for _, table := range []string{"actions_pools", "actions_slots", "actions_jobs"} {
		var count int
		if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE application_id=$1", app.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s retained deleted application metadata: %d, %v", table, count, err)
		}
	}
}
