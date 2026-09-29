package store

import (
	"context"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestRemovedActionsHistorySleepsUntilExpiryAndDeploymentWakesIt(t *testing.T) {
	db := isolatedDatabase(t)
	principal := bootstrapPrincipal(t, db)
	ctx := context.Background()
	initial := emptyTestSpec()
	deployment, err := db.Accept(ctx, principal, "demo", "development", initial, 0, "actions-history-schedule")
	if err != nil {
		t.Fatal(err)
	}
	app, err := db.Application(ctx, deployment.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	// Development fixture for durable scheduling only; it creates no provider
	// runner and consumes no Kubernetes capacity.
	runners := spec.Application{SchemaVersion: 1, Name: app.Name, Services: map[string]spec.Service{"runner": {Actions: &spec.Actions{Repository: "team/repo", Credential: "management"}}}}
	if err = db.SyncActions(ctx, app, runners, 1); err != nil {
		t.Fatal(err)
	}
	pools, err := db.ActionsPools(ctx, app.ID)
	if err != nil || len(pools) != 1 {
		t.Fatal("pool fixture", err)
	}
	var slots []ActionsSlot
	for i := 0; i < 2; i++ {
		slot, err := db.NewActionsSlot(ctx, pools[0])
		if err != nil {
			t.Fatal(err)
		}
		slot.RunnerID = int64(41 + i)
		if err = db.RecordActionsJob(ctx, slot, actions.Observation{Repository: "team/repo", RunID: int64(21 + i), Attempt: 1}); err != nil {
			t.Fatal(err)
		}
		slots = append(slots, slot)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE actions_jobs SET created_at=now()-interval '2 days' WHERE slot_id=$1`, slots[0].ID); err != nil {
		t.Fatal(err)
	}
	empty := spec.Application{SchemaVersion: 1, Name: app.Name, Services: map[string]spec.Service{}}
	if err = db.SyncActions(ctx, app, empty, 2); err != nil {
		t.Fatal(err)
	}
	work, err := db.NextActionsPool(ctx)
	if err != nil || work == nil {
		t.Fatal("cleanup work missing", err)
	}
	next := time.Now().Add(10 * time.Second).Truncate(time.Microsecond)
	if err = db.CompleteActionsPool(ctx, *work, next, true); err != nil {
		t.Fatal(err)
	}
	var saved time.Time
	if err = db.Pool.QueryRow(ctx, `SELECT next_reconcile_at FROM actions_pools WHERE application_id=$1 AND service='runner'`, app.ID).Scan(&saved); err != nil || !saved.Equal(next) {
		t.Fatal("history delayed outstanding runner cleanup", saved, next, err)
	}
	for _, slot := range slots {
		if err = db.DeleteActionsSlot(ctx, slot.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Advance only this fixture's schedule without a wall-clock sleep.
	if _, err = db.Pool.Exec(ctx, `UPDATE actions_pools SET next_reconcile_at=now() WHERE application_id=$1 AND service='runner'`, app.ID); err != nil {
		t.Fatal(err)
	}
	work, err = db.NextActionsPool(ctx)
	if err != nil || work == nil {
		t.Fatal("history work missing", err)
	}
	if err = db.CompleteActionsPool(ctx, *work, next, true); err != nil {
		t.Fatal(err)
	}
	var earliestExpiry time.Time
	if err = db.Pool.QueryRow(ctx, `SELECT min(created_at)+interval '30 days' FROM actions_jobs WHERE application_id=$1 AND service='runner'`, app.ID).Scan(&earliestExpiry); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `SELECT next_reconcile_at FROM actions_pools WHERE application_id=$1 AND service='runner'`, app.ID).Scan(&saved); err != nil || !saved.Equal(earliestExpiry) || !saved.After(time.Now().Add(27*24*time.Hour)) {
		t.Fatal("metadata-only pool kept the active polling cadence", saved, earliestExpiry, err)
	}
	if duplicate, err := db.NextActionsPool(ctx); err != nil || duplicate != nil {
		t.Fatal("retained history competed with active work", err)
	}
	if err = db.SyncActions(ctx, app, runners, 3); err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteActionsPool(ctx, *work, earliestExpiry, true); err != nil {
		t.Fatal(err)
	}
	reused, err := db.NextActionsPool(ctx)
	if err != nil || reused == nil || reused.Pool.Removed {
		t.Fatal("old history lease suppressed a new deployment wakeup", err)
	}
	activeNext := time.Now().Add(time.Minute).Truncate(time.Microsecond)
	if err = db.CompleteActionsPool(ctx, *reused, activeNext, true); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `SELECT next_reconcile_at FROM actions_pools WHERE application_id=$1 AND service='runner'`, app.ID).Scan(&saved); err != nil || !saved.Equal(activeNext) {
		t.Fatal("reused pool retained its historical expiry schedule", saved, err)
	}
}
