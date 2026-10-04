package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestTerminalPlatformRuntimeMutationRepairUsesNewReviewedAuthority(t *testing.T) {
	for _, terminal := range []string{"attempts-exhausted", "authorization-revoked"} {
		for _, kind := range []string{"update", "delete"} {
			for _, applied := range []bool{false, true} {
				label := "unapplied"
				if applied {
					label = "applied"
				}
				t.Run(terminal+"/"+kind+"/"+label, func(t *testing.T) {
					s, admin, item, plan := managedPlatformFixture(t)
					ctx := context.Background()
					_, raw, err := s.CreateKey(ctx, admin, KeyInput{Name: "terminal-runtime-owner", Project: item.Project, Environment: item.Environment, Permissions: []string{"deployments:read", "deployments:write"}, ExpiresAt: time.Now().Add(time.Hour)})
					if err != nil {
						t.Fatal(err)
					}
					owner, err := s.Authenticate(ctx, raw)
					if err != nil {
						t.Fatal(err)
					}
					review := managedPlatformReview(t, s, owner, item, plan, 0, "create")
					if _, err = s.AcceptManagedPlatform(ctx, owner, item, plan, []byte("sealed-terminal-owner"), review, 0, "terminal-runtime-create", "create"); err != nil {
						t.Fatal(err)
					}
					original, err := s.ClaimManagedPlatformOperation(ctx)
					if err != nil {
						t.Fatal(err)
					}
					claim := PlatformResourceClaim{PlatformID: item.ID, PlatformRevision: 1, Component: "deployment.neon-proxy", Kind: "runtime_component", ResourceID: "terminal-workload-uid", ImmutableGeneration: 1, OwnerOperationID: original.ID}
					if _, err = s.Pool.Exec(ctx, `INSERT INTO platform_component_resources(platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, claim.PlatformID, claim.PlatformRevision, claim.Component, claim.Kind, claim.ResourceID, claim.ImmutableGeneration, claim.OwnerOperationID); err != nil {
						t.Fatal(err)
					}
					mutation := PlatformRuntimeMutation{Component: claim.Component, ResourceID: claim.ResourceID, OldGeneration: 1, NewGeneration: 2, Token: NewID(), SpecSHA256: strings.Repeat("a", 64)}
					if err = s.PreparePlatformRuntimeMutation(ctx, original, claim, mutation); err != nil {
						t.Fatal(err)
					}
					if err = s.RecordManagedPlatformStep(ctx, original, "queued", "retry", "", nil); err != nil {
						t.Fatal(err)
					}
					if _, err = s.Pool.Exec(ctx, `UPDATE managed_platform_operations SET next_attempt_at=clock_timestamp()-interval '1 second',attempt=CASE WHEN $2='attempts-exhausted' THEN 240 ELSE attempt END WHERE id=$1`, original.ID, terminal); err != nil {
						t.Fatal(err)
					}
					if terminal == "authorization-revoked" {
						if _, err = s.Pool.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1`, owner.KeyID); err != nil {
							t.Fatal(err)
						}
					}
					if _, err = s.ClaimManagedPlatformOperation(ctx); !errors.Is(err, pgx.ErrNoRows) {
						t.Fatal("original operation did not become terminal", err)
					}
					var phase, status string
					if err = s.Pool.QueryRow(ctx, `SELECT status,phase FROM managed_platform_operations WHERE id=$1`, original.ID).Scan(&status, &phase); err != nil {
						t.Fatal(err)
					}
					if status != "failed" || phase != terminal {
						t.Fatal("terminal transition was not committed", status, phase)
					}
					current, err := s.ManagedPlatform(ctx, admin, item.ID, true)
					if err != nil {
						t.Fatal(err)
					}
					nextReview := managedPlatformReview(t, s, admin, current, plan, 1, kind)
					if _, err = s.AcceptManagedPlatform(ctx, admin, current, plan, []byte("sealed-new-authority"), nextReview, 1, "terminal-runtime-repair", kind); err != nil {
						t.Fatal("newly reviewed repair remained blocked", err)
					}
					repairOp, err := s.ClaimManagedPlatformOperation(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if err = s.AdvancePlatformResourceClaim(ctx, repairOp, claim); !errors.Is(err, ErrConflict) {
						t.Fatal("unresolved intent was bypassed by claim advancement", err)
					}
					repair, err := s.PlatformRuntimeMutationForRepair(ctx, repairOp, claim.Component)
					if err != nil || repair.Mutation != mutation || repair.Claim.OwnerOperationID != original.ID {
						t.Fatal("original evidence was lost", err)
					}
					stale := repairOp
					stale.Lease = "stale"
					if err = s.ResolvePlatformRuntimeMutationRepair(ctx, stale, repair, applied); !errors.Is(err, ErrConflict) {
						t.Fatal("stale repair lease was accepted", err)
					}
					changed := repair
					changed.Mutation.Token = NewID()
					if err = s.ResolvePlatformRuntimeMutationRepair(ctx, repairOp, changed, applied); !errors.Is(err, ErrConflict) {
						t.Fatal("changed repair evidence was accepted", err)
					}
					if err = s.ResolvePlatformRuntimeMutationRepair(ctx, repairOp, repair, applied); err != nil {
						t.Fatal(err)
					}
					if applied {
						claim.ImmutableGeneration = 2
					}
					if err = s.AdvancePlatformResourceClaim(ctx, repairOp, claim); err != nil {
						t.Fatal("resolved claim did not move to new authority", err)
					}
					claims, err := s.PlatformResourceClaims(ctx, repairOp, repairOp.Revision)
					if err != nil || len(claims) != 1 || claims[0].OwnerOperationID != repairOp.ID || claims[0].ImmutableGeneration != claim.ImmutableGeneration {
						t.Fatal("new operation did not receive exact resolved ownership", err)
					}
					if pending, err := s.PlatformRuntimeMutationForRepair(ctx, repairOp, claim.Component); err != nil || pending.Mutation.Token != "" {
						t.Fatal("resolved journal remained pending", err)
					}
					if err = s.CompletePlatformRuntimeMutation(ctx, original, repair.Claim, mutation); !errors.Is(err, ErrConflict) {
						t.Fatal("terminal original authority returned", err)
					}
				})
			}
		}
	}
}

func TestTerminalPlatformRuntimeRepairSurvivesAnotherFailedRevision(t *testing.T) {
	s, principal, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-create"), review, 0, "repair-chain-create", "create"); err != nil {
		t.Fatal(err)
	}
	original, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claim := PlatformResourceClaim{PlatformID: item.ID, PlatformRevision: 1, Component: "deployment.neon-proxy", Kind: "runtime_component", ResourceID: "chain-workload-uid", ImmutableGeneration: 1, OwnerOperationID: original.ID}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO platform_component_resources(platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, claim.PlatformID, claim.PlatformRevision, claim.Component, claim.Kind, claim.ResourceID, claim.ImmutableGeneration, claim.OwnerOperationID); err != nil {
		t.Fatal(err)
	}
	mutation := PlatformRuntimeMutation{Component: claim.Component, ResourceID: claim.ResourceID, OldGeneration: 1, NewGeneration: 2, Token: NewID(), SpecSHA256: strings.Repeat("c", 64)}
	if err = s.PreparePlatformRuntimeMutation(ctx, original, claim, mutation); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, original, "failed", "fixture-terminal", "", nil); err != nil {
		t.Fatal(err)
	}
	var latest ManagedPlatformOperation
	for revision := int64(1); revision <= 2; revision++ {
		current, err := s.ManagedPlatform(ctx, principal, item.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		review := managedPlatformReview(t, s, principal, current, plan, revision, "update")
		if _, err = s.AcceptManagedPlatform(ctx, principal, current, plan, []byte("sealed-chain-repair"), review, revision, "repair-chain-"+NewID(), "update"); err != nil {
			t.Fatal(err)
		}
		latest, err = s.ClaimManagedPlatformOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if revision == 1 {
			if err = s.RecordManagedPlatformStep(ctx, latest, "failed", "fixture-repair-crash", "", nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	prior, err := s.PlatformResourceClaims(ctx, latest, latest.Revision-1)
	if err != nil || len(prior) != 1 || prior[0].PlatformRevision != 1 {
		t.Fatal("older untransferred claim was stranded", err)
	}
	repair, err := s.PlatformRuntimeMutationForRepair(ctx, latest, claim.Component)
	if err != nil || repair.Mutation.Token == "" {
		t.Fatal("original intent was stranded", err)
	}
	if err = s.ResolvePlatformRuntimeMutationRepair(ctx, latest, repair, false); err != nil {
		t.Fatal(err)
	}
	if err = s.AdvancePlatformResourceClaim(ctx, latest, prior[0]); err != nil {
		t.Fatal("resolved old claim could not transfer across failed revision", err)
	}
}
