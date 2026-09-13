package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRevisionChangesDoNotInheritOlderReadiness(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	input := emptyTestSpec()
	accept := func(revision int64, key string) Deployment {
		t.Helper()
		d, err := db.Accept(ctx, p, "demo", "development", input, revision, key)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	claim := func() *Claim {
		t.Helper()
		c, err := db.Claim(ctx)
		if err != nil || c == nil {
			t.Fatalf("missing claim: %v", err)
		}
		t.Cleanup(c.Release)
		return c
	}
	first := accept(0, "observation-first")
	c := claim()
	if err := c.Finish(ctx, "succeeded", "", map[string]any{"status": "healthy", "revision": 1}); err != nil {
		t.Fatal(err)
	}
	c.Release()
	second := accept(1, "observation-second")
	app, err := db.Application(ctx, first.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	var observation map[string]any
	if err := json.Unmarshal(app.Observed, &observation); err != nil {
		t.Fatal(err)
	}
	if len(observation) != 0 {
		t.Fatal("new revision inherited previous readiness")
	}
	c = claim()
	third := accept(2, "observation-third")
	// A current observation can arrive before the older deployment finishes.
	if _, err := db.Pool.Exec(ctx, "UPDATE applications SET observed=$2 WHERE id=$1", first.ApplicationID, JSON(map[string]any{"status": "pending", "revision": 3})); err != nil {
		t.Fatal(err)
	}
	if err := c.Finish(ctx, "succeeded", "", map[string]any{"status": "healthy", "revision": 2}); err != nil {
		t.Fatal(err)
	}
	c.Release()
	app, err = db.Application(ctx, first.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	observation = nil
	if err := json.Unmarshal(app.Observed, &observation); err != nil {
		t.Fatal(err)
	}
	if app.Revision != third.Revision || observation["status"] != "pending" || observation["revision"] != float64(3) {
		t.Fatal("older completion replaced current runtime")
	}
	historical, err := db.Deployment(ctx, second.ID)
	if err != nil || historical.Status != "succeeded" {
		t.Fatalf("historical outcome was lost: %v", err)
	}
	c = claim()
	if err := c.Finish(ctx, "succeeded", "", map[string]any{"status": "healthy", "revision": 3}); err != nil {
		t.Fatal(err)
	}
	app, err = db.Application(ctx, first.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	observation = nil
	if err := json.Unmarshal(app.Observed, &observation); err != nil {
		t.Fatal(err)
	}
	if app.Status != "healthy" || observation["status"] != "healthy" || observation["revision"] != float64(3) {
		t.Fatal("current completion was not recorded")
	}
}
