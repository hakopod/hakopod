package store

import (
	"context"
	"errors"
	"testing"
)

func TestRuntimeClaimExcludesDeploymentsAndRechecksRevision(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	app := emptyTestSpec()
	d, err := db.Accept(ctx, p, "demo", "development", app, 0, "runtime-initial")
	if err != nil {
		t.Fatal(err)
	}
	if c, err := db.ClaimRuntime(ctx, d.ApplicationID, 1); err != nil || c != nil {
		t.Fatal("queued revision allowed maintenance", err)
	}
	deployment, err := db.Claim(ctx)
	if err != nil || deployment == nil {
		t.Fatal(err)
	}
	if c, err := db.ClaimRuntime(ctx, d.ApplicationID, 1); err != nil || c != nil {
		t.Fatal("active rollout allowed maintenance", err)
	}
	if err = deployment.Finish(ctx, "succeeded", "", map[string]any{"status": "healthy"}); err != nil {
		t.Fatal(err)
	}
	deployment.Release()
	runtime, err := db.ClaimRuntime(ctx, d.ApplicationID, 1)
	if err != nil || runtime == nil {
		t.Fatal("healthy revision denied maintenance", err)
	}
	defer runtime.Release()
	if runtime.OperationID != d.ID {
		t.Fatal("wrong event operation")
	}
	if c, err := db.ClaimRuntime(ctx, d.ApplicationID, 1); err != nil || c != nil {
		t.Fatal("duplicate maintenance claim", err)
	}
	_, err = db.Accept(ctx, p, "demo", "development", app, 1, "runtime-next-revision")
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(runtime.Check(ctx), ErrClaimLost) {
		t.Fatal("new revision did not revoke maintenance")
	}
	if c, err := db.Claim(ctx); err != nil || c != nil {
		t.Fatal("deployment overtook maintenance lock", err)
	}
	runtime.Release()
	if !errors.Is(runtime.Check(ctx), ErrClaimLost) {
		t.Fatal("released maintenance retained authority")
	}
	next, err := db.Claim(ctx)
	if err != nil || next == nil {
		t.Fatal("release did not unblock rollout", err)
	}
	next.Release()
}
