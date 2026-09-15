package store

import (
	"context"
	"encoding/json"
	"github.com/hakopod/hakopod/internal/spec"
	"testing"
)

func TestRecoveryJournalSurvivesReclaim(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	app := emptyTestSpec()
	svc := app.Services["api"]
	svc.Image = "python@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	app.Services["api"] = svc
	d, err := db.Accept(ctx, p, "demo", "development", app, 0, "recover-first", app)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if err = claim.Finish(ctx, "succeeded", "", map[string]any{"status": "healthy"}); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	prior, err := db.LastHealthyRelease(ctx, d.ApplicationID, 2)
	if err != nil || prior == nil {
		t.Fatal(err)
	}
	next, _ := spec.Normalize(app)
	svc = next.Services["api"]
	svc.Image = "python@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	next.Services["api"] = svc
	failed, err := db.Accept(ctx, p, "demo", "development", next, 1, "recover-second", next)
	if err != nil {
		t.Fatal(err)
	}
	claim, err = db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if err = claim.BeginRecovery(ctx, prior, "readiness failed", ""); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	claim, err = db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if claim.Deployment.RecoveryState != "running" || claim.Deployment.RecoveryRevision != 1 || claim.Deployment.RecoverySpec.Services["api"].Image != app.Services["api"].Image {
		t.Fatal("recovery target lost after reclaim")
	}
	result := map[string]any{"status": "healthy", "revision": 2}
	if err = claim.FinishRecovery(ctx, "succeeded", "", result); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	claim, err = db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if claim.Deployment.RecoveryState != "succeeded" {
		t.Fatal("completed recovery lost")
	}
	if err = claim.Finish(ctx, "failed", "previous restored", json.RawMessage(JSON(result))); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	stored, err := db.Deployment(ctx, failed.ID)
	if err != nil || stored.Status != "failed" || stored.RecoveryState != "succeeded" {
		t.Fatal(stored, err)
	}
	a, err := db.Application(ctx, d.ApplicationID)
	if err != nil || a.Status != "recovered" {
		t.Fatal(a, err)
	}
	effective, err := db.ObservedSpec(ctx, a)
	if err != nil || effective.Services["api"].Image != app.Services["api"].Image {
		t.Fatal("observation uses failed desired image", err)
	}
}
