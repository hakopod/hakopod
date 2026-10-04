package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/platformbackup"
)

func TestNeonTenantPlacementCentralClaimsAndRevisionAdoption(t *testing.T) {
	s, principal, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-runtime"), review, 0, "placement-create", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tenant, token := strings.Repeat("a", 32), strings.Repeat("b", 32)
	claim := PlatformResourceClaim{PlatformID: item.ID, PlatformRevision: 1, Component: "tenant", Kind: "neon_tenant", ResourceID: tenant + "@1:" + token, ImmutableGeneration: 4, OwnerOperationID: op.ID}
	if err = s.ClaimPlatformResource(ctx, op, claim); err != nil {
		t.Fatal(err)
	}
	moved := claim
	moved.ResourceID = tenant + "@2:" + token
	moved.ImmutableGeneration = 5
	apply := func(prior PlatformResourceClaim, next PlatformResourceClaim) error {
		_, err := s.WithNeonControllerState(ctx, item.ID, 1, func(input NeonControllerContext) (managedplatform.NeonControllerState, bool, error) {
			return input.State, true, input.ObserveTenantPlacement(prior, next.ResourceID, next.ImmutableGeneration)
		})
		return err
	}
	if err = apply(claim, moved); err != nil {
		t.Fatal(err)
	}
	if err = apply(claim, moved); !errors.Is(err, ErrConflict) {
		t.Fatal("stale placement CAS accepted", err)
	}
	if err = apply(moved, moved); err != nil {
		t.Fatal("same observed state was not idempotent", err)
	}
	if err = s.VerifyPlatformResourceClaim(ctx, op, claim); !errors.Is(err, ErrConflict) {
		t.Fatal("old placement still verifies", err)
	}
	if err = s.VerifyPlatformResourceClaim(ctx, op, moved); err != nil {
		t.Fatal(err)
	}
	claims, err := s.PlatformResourceClaims(ctx, op, 1)
	if err != nil || len(claims) != 1 || claims[0].ResourceID != moved.ResourceID {
		t.Fatal("lifecycle did not read observed placement", err)
	}
	backup, err := s.ManagedPlatformRecoveryClaims(ctx, item.ID, 1)
	if err != nil || backup["tenant"].ResourceID != moved.ResourceID {
		t.Fatal("backup did not read observed placement", err)
	}
	current, err := s.ManagedPlatformRecoveryCurrentClaim(ctx, item.ID, "tenant")
	if err != nil || current.ResourceID != moved.ResourceID {
		t.Fatal("recovery current claim missed placement", err)
	}
	var immutable string
	if err = s.Pool.QueryRow(ctx, `SELECT resource_id FROM platform_component_resources WHERE platform_id=$1 AND platform_revision=1`, item.ID).Scan(&immutable); err != nil || immutable != claim.ResourceID {
		t.Fatal("accepted claim was rewritten", err)
	}
	if err = s.RecordManagedPlatformStep(ctx, op, "succeeded", "ready", "", nil); err != nil {
		t.Fatal(err)
	}
	review = managedPlatformReview(t, s, principal, item, plan, 1, "update")
	if _, err = s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-runtime"), review, 1, "placement-update", "update"); err != nil {
		t.Fatal(err)
	}
	if err = apply(moved, moved); !errors.Is(err, ErrConflict) {
		t.Fatal("old revision retained callback authority", err)
	}
	next, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AdvancePlatformResourceClaim(ctx, next, moved); err != nil {
		t.Fatal("observed placement could not advance revision", err)
	}
	moved.PlatformRevision = 2
	moved.OwnerOperationID = next.ID
	if err = s.VerifyPlatformResourceClaim(ctx, next, moved); err != nil {
		t.Fatal(err)
	}
	if err = s.ReleasePlatformResourceClaim(ctx, next, moved); err != nil {
		t.Fatal(err)
	}
	claims, err = s.PlatformResourceClaims(ctx, next, 2)
	if err != nil || len(claims) != 0 {
		t.Fatal("released claim inherited stale placement", err)
	}
}

// Reuse a live restore fixture so source matching is tested against the real
// immutable recovery journal and its normal transitions.
func assertNeonTenantPlacementRestoreBoundary(t *testing.T, s *Store, owner ManagedPlatformOperation, recovery platformbackup.Operation, binding NeonRecoveryBinding) {
	t.Helper()
	ctx := context.Background()
	tenant, token := strings.Repeat("a", 32), strings.Repeat("b", 32)
	claim := PlatformResourceClaim{PlatformID: owner.PlatformID, PlatformRevision: 1, Component: "tenant", Kind: "neon_tenant", ResourceID: tenant + "@1:" + token, ImmutableGeneration: 4, OwnerOperationID: owner.ID}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO platform_component_resources(platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, claim.PlatformID, claim.PlatformRevision, claim.Component, claim.Kind, claim.ResourceID, claim.ImmutableGeneration, claim.OwnerOperationID); err != nil {
		t.Fatal(err)
	}
	observe := func(prior PlatformResourceClaim, nextID string, generation int64) error {
		_, err := s.WithNeonControllerState(ctx, owner.PlatformID, 1, func(input NeonControllerContext) (managedplatform.NeonControllerState, bool, error) {
			return input.State, true, input.ObserveTenantPlacement(prior, nextID, generation)
		})
		return err
	}
	moved := claim
	moved.ResourceID = tenant + "@2:" + token
	moved.ImmutableGeneration = 5
	if err := observe(claim, moved.ResourceID, moved.ImmutableGeneration); err != nil {
		t.Fatal(err)
	}
	transition := strings.Repeat("f", 64)
	if _, err := s.PlanNeonRecoveryResource(ctx, recovery, binding, moved, "restored-tenant", transition); err != nil {
		t.Fatal("restore rejected effective prior placement", err)
	}
	if _, err := s.MarkNeonRecoveryPriorReleased(ctx, recovery, "tenant", moved.ResourceID, moved.ImmutableGeneration, transition); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveNeonRecoveryReplacement(ctx, recovery, "tenant", "neon_tenant", "restored-tenant", transition); err != nil {
		t.Fatal(err)
	}
	restored := claim
	restored.ResourceID = binding.TenantID + "@1:" + strings.Repeat("e", 32)
	restored.ImmutableGeneration = 6
	if _, err := s.ConfirmNeonRecoveryReplacement(ctx, recovery, "tenant", restored.ResourceID, restored.ImmutableGeneration, transition); err != nil {
		t.Fatal(err)
	}
	claims, err := s.EffectiveNeonRecoveryClaims(ctx, recovery)
	if err != nil || claims["tenant"].ResourceID != restored.ResourceID {
		t.Fatal("old placement contaminated restored tenant", err)
	}
	if err = observe(moved, moved.ResourceID, moved.ImmutableGeneration); !errors.Is(err, ErrConflict) {
		t.Fatal("old source retained placement authority", err)
	}
	movedRestore := restored
	movedRestore.ResourceID = binding.TenantID + "@2:" + strings.Repeat("e", 32)
	movedRestore.ImmutableGeneration = 7
	if err = observe(restored, movedRestore.ResourceID, movedRestore.ImmutableGeneration); err != nil {
		t.Fatal(err)
	}
	if err = s.VerifyNeonRecoveryResource(ctx, recovery, movedRestore); err != nil {
		t.Fatal("restored placement did not verify centrally", err)
	}
	claims, err = s.ManagedPlatformRecoveryClaims(ctx, owner.PlatformID, 1)
	if err != nil || claims["tenant"].ResourceID != movedRestore.ResourceID {
		t.Fatal("backup missed restored placement", err)
	}
	if _, err = s.MarkNeonRecoveryReplacementReleased(ctx, recovery, "tenant", restored.ResourceID, restored.ImmutableGeneration, transition); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteNeonRecoveryResource(ctx, recovery, "tenant", transition); err != nil {
		t.Fatal(err)
	}
	claims, err = s.EffectiveNeonRecoveryClaims(ctx, recovery)
	if err != nil || claims["tenant"].ResourceID != "" {
		t.Fatal("released restored claim inherited placement", err)
	}
}
