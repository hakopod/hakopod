package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPlatformRuntimeMutationFencesLeaseIntentAndClaim(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed-runtime-fixture"), review, 0, "runtime-mutation", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claim := PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: "deployment.neon-proxy", Kind: "runtime_component", ResourceID: "runtime-uid", ImmutableGeneration: 1, OwnerOperationID: op.ID}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO platform_component_resources(platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, claim.PlatformID, claim.PlatformRevision, claim.Component, claim.Kind, claim.ResourceID, claim.ImmutableGeneration, claim.OwnerOperationID); err != nil {
		t.Fatal(err)
	}
	mutation := PlatformRuntimeMutation{Component: claim.Component, ResourceID: claim.ResourceID, OldGeneration: 1, NewGeneration: 2, Token: NewID(), SpecSHA256: strings.Repeat("a", 64)}
	if err = s.PreparePlatformRuntimeMutation(ctx, op, claim, mutation); err != nil {
		t.Fatal(err)
	}
	if err = s.PreparePlatformRuntimeMutation(ctx, op, claim, mutation); err != nil {
		t.Fatal("same intent did not resume", err)
	}
	changed := mutation
	changed.SpecSHA256 = strings.Repeat("b", 64)
	if err = s.PreparePlatformRuntimeMutation(ctx, op, claim, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("pending intent changed", err)
	}
	stale := op
	stale.Lease = "stale"
	if err = s.CompletePlatformRuntimeMutation(ctx, stale, claim, mutation); !errors.Is(err, ErrConflict) {
		t.Fatal("stale lease completed mutation", err)
	}
	if err = s.CompletePlatformRuntimeMutation(ctx, op, claim, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed digest completed mutation", err)
	}
	if err = s.CompletePlatformRuntimeMutation(ctx, op, claim, mutation); err != nil {
		t.Fatal(err)
	}
	claims, err := s.ManagedPlatformRecoveryClaims(ctx, item.ID, op.Revision)
	if err != nil || claims[claim.Component].ImmutableGeneration != 2 {
		t.Fatal("recovery did not receive updated runtime claim", err)
	}
	if m, err := s.PlatformRuntimeMutation(ctx, op, claim.Component); err != nil || m.Token != "" {
		t.Fatal("completed mutation remained pending", err)
	}
	if err = s.PreparePlatformRuntimeMutation(ctx, op, claim, mutation); !errors.Is(err, ErrConflict) {
		t.Fatal("old claim was accepted after advancement", err)
	}
	claim.ImmutableGeneration = 2
	mutation.OldGeneration, mutation.NewGeneration, mutation.Token = 2, 3, NewID()
	if err = s.PreparePlatformRuntimeMutation(ctx, op, claim, mutation); err != nil {
		t.Fatal("second maintenance mutation was blocked", err)
	}
	if err = s.CompletePlatformRuntimeMutation(ctx, op, claim, mutation); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformRuntimeMutationBlocksSupersedingAdmission(t *testing.T) {
	for _, phase := range []string{"running", "pending", "waiting-ready", "failed"} {
		t.Run(phase, func(t *testing.T) {
			s, p, item, plan, destination := recoveryStoreFixture(t)
			ctx := context.Background()
			current, err := s.ManagedPlatform(ctx, p, item.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			enableManagedTLSFixture(&current)
			review := managedPlatformReview(t, s, p, current, plan, current.Revision, "update")
			accepted, err := s.AcceptManagedPlatform(ctx, p, current, plan, []byte("sealed-admission"), review, current.Revision, "mutation-admission-enable", "update")
			if err != nil {
				t.Fatal(err)
			}
			op, err := s.ClaimManagedPlatformOperation(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.RecordManagedPlatformStep(ctx, op, "succeeded", "ready", "", nil); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Pool.Exec(ctx, "UPDATE managed_platform_maintenance SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE platform_id=$1", item.ID); err != nil {
				t.Fatal(err)
			}
			maintenance, err := s.ClaimManagedPlatformMaintenance(ctx)
			if err != nil {
				t.Fatal(err)
			}
			latest, err := s.ManagedPlatform(ctx, p, item.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			deleteReview := managedPlatformReview(t, s, p, latest, plan, latest.Revision, "delete")
			intent := backupRecoveryIntent(latest, destination)
			intent.ExpectedSourceRevision = latest.Revision
			recoveryReview, err := s.SavePlatformRecoveryReview(ctx, p, intent)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "pending" {
				claim := PlatformResourceClaim{PlatformID: item.ID, PlatformRevision: latest.Revision, Component: "deployment.supabase-auth", Kind: "runtime_component", ResourceID: "runtime-uid", ImmutableGeneration: 1, OwnerOperationID: accepted.ID}
				if _, err = s.Pool.Exec(ctx, `INSERT INTO platform_component_resources(platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, claim.PlatformID, claim.PlatformRevision, claim.Component, claim.Kind, claim.ResourceID, claim.ImmutableGeneration, claim.OwnerOperationID); err != nil {
					t.Fatal(err)
				}
				mutation := PlatformRuntimeMutation{Component: claim.Component, ResourceID: claim.ResourceID, OldGeneration: 1, NewGeneration: 2, Token: NewID(), SpecSHA256: strings.Repeat("c", 64)}
				if err = s.PreparePlatformRuntimeMutation(ctx, maintenance, claim, mutation); err != nil {
					t.Fatal(err)
				}
				maintenanceClaims, loadErr := s.PlatformResourceClaims(ctx, maintenance, latest.Revision)
				if loadErr != nil || len(maintenanceClaims) != 1 || maintenanceClaims[0].ResourceID != claim.ResourceID {
					t.Fatal("maintenance did not load lifecycle-owned claims", loadErr)
				}

				if err = s.RecordManagedPlatformStep(ctx, maintenance, "queued", "retry", "", nil); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "waiting-ready" || phase == "failed" {
				status := "queued"
				if phase == "failed" {
					status = "failed"
				}
				if err = s.RecordManagedPlatformStep(ctx, maintenance, status, phase, "", nil); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err = s.AcceptManagedPlatform(ctx, p, latest, plan, []byte("sealed-delete"), deleteReview, latest.Revision, "mutation-admission-delete", "delete"); !errors.Is(err, ErrConflict) {
					t.Fatal("mutation superseded unfinished runtime maintenance", err)
				}
			}
			if _, err = s.AcceptPlatformRecovery(ctx, p, intent, recoveryReview, "mutation-admission-backup"); !errors.Is(err, ErrConflict) {
				t.Fatal("recovery stranded unfinished runtime maintenance", err)
			}
			if phase == "waiting-ready" || phase == "failed" {
				if _, err = s.Pool.Exec(ctx, "UPDATE managed_platform_maintenance SET phase='restore-isolated',attempt=0 WHERE platform_id=$1", item.ID); err != nil {
					t.Fatal(err)
				}
				recovery, err := s.AcceptPlatformRecovery(ctx, p, intent, recoveryReview, "mutation-admission-after-hold")
				if err != nil {
					t.Fatal("stable restore isolation blocked reviewed recovery", err)
				}
				if _, err = s.Pool.Exec(ctx, "UPDATE managed_platform_maintenance SET phase='waiting-ready',attempt=1 WHERE platform_id=$1", item.ID); err != nil {
					t.Fatal(err)
				}
				replay, err := s.AcceptPlatformRecovery(ctx, p, intent, recoveryReview, "mutation-admission-after-hold")
				if err != nil || replay.ID != recovery.ID {
					t.Fatal("unfinished maintenance broke recovery idempotent replay", err)
				}
			}

			replay, err := s.AcceptManagedPlatform(ctx, p, current, plan, []byte("sealed-admission"), review, current.Revision, "mutation-admission-enable", "update")
			if err != nil || replay.ID != accepted.ID {
				t.Fatal("runtime maintenance broke idempotent admission replay", err)
			}
		})
	}
}

func TestPlatformRecoveryRuntimeGenerationPreservesReplacementIdentity(t *testing.T) {
	s, p, item, _, destination := recoveryStoreFixture(t)
	ctx := context.Background()
	intent := backupRecoveryIntent(item, destination)
	review, err := s.SavePlatformRecoveryReview(ctx, p, intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptPlatformRecovery(ctx, p, intent, review, "replacement-generation-backup"); err != nil {
		t.Fatal(err)
	}
	recovery, err := s.ClaimPlatformRecovery(ctx, "replacement-generation-lease")
	if err != nil {
		t.Fatal(err)
	}
	var owner string
	if err = s.Pool.QueryRow(ctx, "SELECT id FROM managed_platform_operations WHERE platform_id=$1 AND revision=1", item.ID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	artifact := NewID()
	if _, err = s.Pool.Exec(ctx, `INSERT INTO managed_platform_recovery_artifacts(id,source_platform_id,source_revision,destination_id,object_key,encrypted_bytes,encrypted_sha256,manifest,manifest_sha256,published_at) VALUES($1,$2,1,$3,'fixture/replacement',1,$4,'{}',$4,now())`, artifact, item.ID, destination.ID, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO platform_component_resources(platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id) VALUES($1,1,'statefulset.replacement','runtime_component','prior-uid',1,$2)`, item.ID, owner); err != nil {
		t.Fatal(err)
	}
	// This persisted fixture isolates generation advancement from the separately
	// tested restore planner. The replacement identity must remain immutable.
	if _, err = s.Pool.Exec(ctx, `INSERT INTO platform_component_recovery_overrides(platform_id,platform_revision,component,resource_kind,recovery_operation_id,artifact_id,manifest_sha256,prior_resource_id,prior_generation,prior_owner_operation_id,replacement_external_key,replacement_resource_id,replacement_generation,transition_token,phase) VALUES($1,1,'statefulset.replacement','runtime_component',$2,$3,$4,'prior-uid',1,$5,'fixture/replacement','replacement-uid',2,$4,'confirmed')`, item.ID, recovery.ID, artifact, strings.Repeat("d", 64), owner); err != nil {
		t.Fatal(err)
	}
	mutation := PlatformRuntimeMutation{Component: "statefulset.replacement", ResourceID: "replacement-uid", OldGeneration: 2, NewGeneration: 3, Token: strings.Repeat("e", 64), SpecSHA256: strings.Repeat("f", 64)}
	if err = s.PreparePlatformRecoveryWorkloadMutation(ctx, recovery, item.ID, mutation, 1, 0, 2); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PlatformRecoveryWorkloadMutation(ctx, recovery, item.ID, "replacement")
	if err != nil || pending != mutation {
		t.Fatal("StatefulSet intent was not preserved", err)
	}
	if err = s.CompletePlatformRecoveryDeployment(ctx, recovery, "replacement", "replacement-uid", 2, 3); !errors.Is(err, ErrConflict) {
		t.Fatal("legacy completion bypassed StatefulSet digest fencing", err)
	}
	changed := mutation
	changed.SpecSHA256 = strings.Repeat("a", 64)
	if err = s.CompletePlatformRecoveryWorkloadMutation(ctx, recovery, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed StatefulSet spec was adopted", err)
	}
	if err = s.CompletePlatformRecoveryWorkloadMutation(ctx, recovery, mutation); err != nil {
		t.Fatal(err)
	}
	var initial, current int64
	if err = s.Pool.QueryRow(ctx, `SELECT replacement_generation,runtime_generation FROM platform_component_recovery_overrides WHERE platform_id=$1 AND component='statefulset.replacement'`, item.ID).Scan(&initial, &current); err != nil {
		t.Fatal(err)
	}
	if initial != 2 || current != 3 {
		t.Fatal("workload mutation rewrote immutable restore evidence", initial, current)
	}
	claims, err := s.ManagedPlatformRecoveryClaims(ctx, item.ID, 1)
	if err != nil || claims["statefulset.replacement"].ImmutableGeneration != 3 || claims["statefulset.replacement"].ResourceID != "replacement-uid" {
		t.Fatal("next recovery did not receive effective replacement generation", err)
	}
}
