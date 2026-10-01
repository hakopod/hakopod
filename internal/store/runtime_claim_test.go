package store

import (
	"context"
	"errors"
	"testing"
)

func TestRuntimeClaimExcludesDeploymentsAndRechecksRevision(t *testing.T) {
	testRuntimeClaimFences(t, false)
}

func TestTLSIssuerClaimExcludesDeploymentsAndRechecksRevision(t *testing.T) {
	testRuntimeClaimFences(t, true)
}

func testRuntimeClaimFences(t *testing.T, issuer bool) {
	t.Helper()
	db := isolatedDatabase(t)
	claimRuntime := db.ClaimRuntime
	status := "succeeded"
	if issuer {
		claimRuntime = db.ClaimTLSIssuer
		status = "failed"
	}
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	app := emptyTestSpec()
	d, err := db.Accept(ctx, p, "demo", "development", app, 0, "runtime-initial")
	if err != nil {
		t.Fatal(err)
	}
	if c, err := claimRuntime(ctx, d.ApplicationID, 1); err != nil || c != nil {
		t.Fatal("queued revision allowed maintenance", err)
	}
	deployment, err := db.Claim(ctx)
	if err != nil || deployment == nil {
		t.Fatal(err)
	}
	if c, err := claimRuntime(ctx, d.ApplicationID, 1); err != nil || c != nil {
		t.Fatal("active rollout allowed maintenance", err)
	}
	if err = deployment.Finish(ctx, status, "", map[string]any{"status": "healthy"}); err != nil {
		t.Fatal(err)
	}
	deployment.Release()
	runtime, err := claimRuntime(ctx, d.ApplicationID, 1)
	if err != nil || runtime == nil {
		t.Fatal("eligible revision denied maintenance", err)
	}
	defer runtime.Release()
	if runtime.OperationID != d.ID {
		t.Fatal("wrong event operation")
	}
	if c, err := claimRuntime(ctx, d.ApplicationID, 1); err != nil || c != nil {
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

func TestTLSIssuerClaimTerminalStates(t *testing.T) {
	for _, status := range []string{"queued", "running", "succeeded", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			db := isolatedDatabase(t)
			p := bootstrapPrincipal(t, db)
			ctx := context.Background()
			d, err := db.Accept(ctx, p, "demo", "development", emptyTestSpec(), 0, "issuer-state")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status=$2 WHERE id=$1", d.ID, status); err != nil {
				t.Fatal(err)
			}
			ordinary, err := db.ClaimRuntime(ctx, d.ApplicationID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if ordinary != nil {
				defer ordinary.Release()
			}
			if (ordinary != nil) != (status == "succeeded") {
				t.Fatal("ordinary runtime eligibility changed")
			}
			if ordinary != nil {
				ordinary.Release()
			}
			claim, err := db.ClaimTLSIssuer(ctx, d.ApplicationID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if claim != nil {
				defer claim.Release()
			}
			terminal := status == "succeeded" || status == "failed" || status == "cancelled"
			if (claim != nil) != terminal {
				t.Fatal("incorrect issuer eligibility")
			}
			if claim != nil {
				if claim.OperationID != d.ID {
					t.Fatal("wrong issuer operation")
				}
				if err = claim.Check(ctx); err != nil {
					t.Fatal(err)
				}
				claim.Release()
			}
			if stale, err := db.ClaimTLSIssuer(ctx, d.ApplicationID, 2); err != nil || stale != nil {
				t.Fatal("stale revision accepted", err)
			}
			if missing, err := db.ClaimTLSIssuer(ctx, NewID(), 1); err != nil || missing != nil {
				t.Fatal("missing application accepted", err)
			}
		})
	}
}
