package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestManagedPlatformReviewIsBoundToCurrentAuthority(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")
	if _, err := s.Pool.Exec(ctx, "UPDATE api_keys SET permissions=ARRAY['deployments:read'] WHERE id=$1", p.KeyID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed"), review, 0, "authority-review", "create"); !errors.Is(err, ErrForbidden) {
		t.Fatal("review survived authority revocation", err)
	}
}

func TestManagedPlatformDeletePreservesSpecAfterQualificationWithdrawal(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	createReview := managedPlatformReview(t, s, p, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed"), createReview, 0, "delete-create", "create"); err != nil {
		t.Fatal(err)
	}
	createOperation, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, createOperation, "succeeded", "ready", "", map[string]any{"status": "ready"}); err != nil {
		t.Fatal(err)
	}
	current, err := s.ManagedPlatform(ctx, p, item.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	plan.Capability.Available = false
	plan.Capability.ClusterQualified = false
	deleteReview := managedPlatformReview(t, s, p, current, plan, current.Revision, "delete")
	operation, err := s.AcceptManagedPlatform(ctx, p, current, plan, []byte("sealed-delete"), deleteReview, current.Revision, "delete-platform", "delete")
	if err != nil {
		t.Fatal(err)
	}
	if operation.Kind != "delete" || !bytes.Equal(JSON(operation.Spec), JSON(current.Spec)) {
		t.Fatal("delete changed the durable desired specification")
	}
}

func TestManagedPlatformClaimTransferAndObservationBounds(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	createReview := managedPlatformReview(t, s, p, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed"), createReview, 0, "transfer-create", "create"); err != nil {
		t.Fatal(err)
	}
	createOperation, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claim := PlatformResourceClaim{PlatformID: item.ID, PlatformRevision: 1, Component: "timeline", Kind: "neon_timeline", ResourceID: "22222222222222222222222222222222", ImmutableGeneration: 1, OwnerOperationID: createOperation.ID}
	if err = s.ClaimPlatformResource(ctx, createOperation, claim); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, createOperation, "succeeded", "ready", "", map[string]any{"status": "ready"}); err != nil {
		t.Fatal(err)
	}
	current, err := s.ManagedPlatform(ctx, p, item.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	current.Spec.Neon.ComputeReplicas++
	updateReview := managedPlatformReview(t, s, p, current, plan, current.Revision, "update")
	if _, err = s.AcceptManagedPlatform(ctx, p, current, plan, []byte("sealed-update"), updateReview, current.Revision, "transfer-update", "update"); err != nil {
		t.Fatal(err)
	}
	updateOperation, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := s.PlatformResourceClaims(ctx, updateOperation, updateOperation.Revision-1)
	if err != nil || len(prior) != 1 {
		t.Fatal("prior claim lookup failed", err)
	}
	if err = s.AdvancePlatformResourceClaim(ctx, updateOperation, prior[0]); err != nil {
		t.Fatal(err)
	}
	currentClaims, err := s.PlatformResourceClaims(ctx, updateOperation, updateOperation.Revision)
	if err != nil || len(currentClaims) != 1 || currentClaims[0].OwnerOperationID != updateOperation.ID {
		t.Fatal("claim transfer was not exact", err)
	}
	if err = s.RecordManagedPlatformStep(ctx, updateOperation, "queued", "retry", "", map[string]any{"oversized": strings.Repeat("x", MaxManagedPlatformObservationBytes)}); !errors.Is(err, ErrInput) {
		t.Fatal("oversized observation accepted", err)
	}
}

func TestManagedPlatformOperationFenceRejectsForgedImmutableFieldsAndUnplannedClaims(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed"), review, 0, "immutable-fence", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	forgedKind := op
	forgedKind.Kind = "delete"
	if err = s.RecordManagedPlatformStep(ctx, forgedKind, "succeeded", "deleted", "", map[string]any{}); !errors.Is(err, ErrConflict) {
		t.Fatal("forged operation kind crossed the lease fence", err)
	}
	forgedPlan := op
	forgedPlan.Plan.Namespace = "managed-platform-forged"
	if err = s.HeartbeatManagedPlatformOperation(ctx, forgedPlan); !errors.Is(err, ErrConflict) {
		t.Fatal("forged operation plan crossed the lease fence", err)
	}
	unplanned := PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: "unplanned", Kind: "runtime_component", ResourceID: "foreign", ImmutableGeneration: 1, OwnerOperationID: op.ID}
	if err = s.ClaimPlatformResource(ctx, op, unplanned); !errors.Is(err, ErrInput) {
		t.Fatal("resource outside the reviewed plan was claimed", err)
	}
}

func TestManagedPlatformOperationReplayUsesCurrentAuthorityAndExactRequest(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")
	accepted, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed"), review, 0, "replay-before-review", "create")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.ManagedPlatformOperationReplay(ctx, p, item, plan, 0, "replay-before-review", "create")
	if err != nil || replayed.ID != accepted.ID || len(replayed.EncryptedSnapshot) != 0 || replayed.Lease != "" {
		t.Fatal("authorized exact replay was not returned redacted", err)
	}
	changed := item
	changed.Spec.Neon.ComputeReplicas++
	if _, err = s.ManagedPlatformOperationReplay(ctx, p, changed, plan, 0, "replay-before-review", "create"); !errors.Is(err, ErrConflict) {
		t.Fatal("changed request replay was accepted", err)
	}
}

func TestManagedPlatformResourceIntentRequiresConfirmationForOwnership(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed"), review, 0, "resource-intent", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	requested := PlatformResourceIntent{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: "tenant", Kind: "neon_tenant", ExternalKey: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", OwnerOperationID: op.ID}
	intent, err := s.ReservePlatformResourceIntent(ctx, op, requested)
	if err != nil || intent.ID == "" || intent.ConfirmedAt != nil {
		t.Fatal("pending intent was not reserved", err)
	}
	if claims, claimsErr := s.PlatformResourceClaims(ctx, op, op.Revision); claimsErr != nil || len(claims) != 0 {
		t.Fatal("pending intent was treated as ownership", claimsErr)
	}
	if err = s.CancelPlatformResourceIntent(ctx, op, intent); err != nil {
		t.Fatal("pending intent could not be cancelled", err)
	}
	replacement, err := s.ReservePlatformResourceIntent(ctx, op, requested)
	if err != nil || replacement.ID == "" || replacement.ID == intent.ID || replacement.ConfirmedAt != nil {
		t.Fatal("cancelled intent did not permit a fresh reservation", err)
	}
	intent = replacement
	claim := PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: "tenant", Kind: "neon_tenant", ResourceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa@11", ImmutableGeneration: 7, OwnerOperationID: op.ID}
	if err = s.ConfirmPlatformResourceIntent(ctx, op, intent, claim); err != nil {
		t.Fatal(err)
	}
	intents, err := s.PlatformResourceIntents(ctx, op, op.Revision)
	if err != nil || len(intents) != 1 || intents[0].ConfirmedAt == nil {
		t.Fatal("confirmed intent was not retained", err)
	}
	if err = s.CancelPlatformResourceIntent(ctx, op, intents[0]); !errors.Is(err, ErrConflict) {
		t.Fatal("confirmed intent was cancelled without releasing its claim", err)
	}
	if err = s.ReleasePlatformResourceClaim(ctx, op, claim); err != nil {
		t.Fatal(err)
	}
	intents, err = s.PlatformResourceIntents(ctx, op, op.Revision)
	if err != nil || len(intents) != 0 {
		t.Fatal("released claim left a live intent", err)
	}
}
