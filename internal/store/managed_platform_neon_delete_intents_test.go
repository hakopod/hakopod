package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func neonOlderTimelineDeleteFixture(t *testing.T) (*Store, Principal, ManagedPlatformOperation, PlatformResourceIntent, PlatformResourceIntent) {
	t.Helper()
	ctx := context.Background()
	tenantID, timelineID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	var parent, pending PlatformResourceIntent
	s, principal, op := neonUnprovisionedDeleteFixture(t, func(s *Store, create ManagedPlatformOperation) {
		var err error
		parent, err = s.ReservePlatformResourceIntent(ctx, create, PlatformResourceIntent{PlatformID: create.PlatformID, PlatformRevision: create.Revision, Component: "tenant", Kind: "neon_tenant", ExternalKey: tenantID, OwnerOperationID: create.ID})
		if err != nil {
			t.Fatal(err)
		}
		if err = s.ConfirmPlatformResourceIntent(ctx, create, parent, PlatformResourceClaim{PlatformID: create.PlatformID, PlatformRevision: create.Revision, Component: "tenant", Kind: "neon_tenant", ResourceID: "owned-tenant", ImmutableGeneration: 1, OwnerOperationID: create.ID}); err != nil {
			t.Fatal(err)
		}
		pending, err = s.ReservePlatformResourceIntent(ctx, create, PlatformResourceIntent{PlatformID: create.PlatformID, PlatformRevision: create.Revision, Component: "timeline", Kind: "neon_timeline", ExternalKey: tenantID + "/" + timelineID, OwnerOperationID: create.ID})
		if err != nil {
			t.Fatal(err)
		}
	})
	for op.Revision < 4 {
		if err := s.RecordManagedPlatformStep(ctx, op, "failed", "provider-unavailable", "", nil); err != nil {
			t.Fatal(err)
		}
		current, err := s.ManagedPlatform(ctx, principal, op.PlatformID, true)
		if err != nil {
			t.Fatal(err)
		}
		review := managedPlatformReview(t, s, principal, current, op.Plan, current.Revision, "delete")
		if _, err = s.AcceptManagedPlatform(ctx, principal, current, op.Plan, []byte("sealed-test-delete"), review, current.Revision, "pending-timeline-delete-"+NewID(), "delete"); err != nil {
			t.Fatal(err)
		}
		op, err = s.ClaimManagedPlatformOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	return s, principal, op, parent, pending
}

func TestNeonTimelineDeletionIntentsSurviveFailedDeletes(t *testing.T) {
	ctx := context.Background()
	s, _, op, parent, pending := neonOlderTimelineDeleteFixture(t)
	items, err := s.PlatformResourceIntents(ctx, op, op.Revision-1)
	if err != nil || len(items) != 0 {
		t.Fatalf("generic revision reads changed: count=%d error=%v", len(items), err)
	}
	items, err = s.NeonTimelineDeletionIntents(ctx, op, strings.Repeat("a", 32), strings.Repeat("b", 32), parent.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("original deletion authority unavailable: count=%d error=%v", len(items), err)
	}
	for _, item := range items {
		if item.PlatformRevision != 1 || item.OwnerOperationID != pending.OwnerOperationID {
			t.Fatal("lookup changed original revision or owner")
		}
	}
	if err = s.CancelPlatformResourceIntent(ctx, op, pending); !errors.Is(err, ErrInput) {
		t.Fatalf("generic cancellation accepted older intent: %v", err)
	}
	// The runtime establishes remote absence before this store transition.
	if err = s.CancelNeonTimelineDeletionIntent(ctx, op, pending); err != nil {
		t.Fatal(err)
	}
	items, err = s.NeonTimelineDeletionIntents(ctx, op, strings.Repeat("a", 32), strings.Repeat("b", 32), parent.ID)
	if err != nil || len(items) != 1 || items[0].ID != parent.ID || items[0].ConfirmedAt == nil {
		t.Fatalf("cancellation changed confirmed parent: count=%d error=%v", len(items), err)
	}
	var failedCount int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM managed_platform_operations WHERE platform_id=$1 AND revision<4 AND status='failed'", op.PlatformID).Scan(&failedCount); err != nil || failedCount != 3 {
		t.Fatalf("retry rewrote original failed operations: count=%d error=%v", failedCount, err)
	}
}

func TestNeonTimelineDeletionIntentsRequireMatchingAuthority(t *testing.T) {
	ctx := context.Background()
	s, _, op, parent, pending := neonOlderTimelineDeleteFixture(t)
	tenantID, timelineID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, test := range []struct {
		name                    string
		op                      ManagedPlatformOperation
		tenant, timeline, token string
	}{
		{name: "wrong tenant", op: op, tenant: strings.Repeat("c", 32), timeline: timelineID, token: parent.ID},
		{name: "wrong timeline", op: op, tenant: tenantID, timeline: strings.Repeat("c", 32), token: parent.ID},
		{name: "wrong parent", op: op, tenant: tenantID, timeline: timelineID, token: NewID()},
		{name: "missing parent", op: op, tenant: tenantID, timeline: timelineID},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := s.NeonTimelineDeletionIntents(ctx, test.op, test.tenant, test.timeline, test.token); !errors.Is(err, ErrConflict) {
				t.Fatalf("ambiguous authority was accepted: %v", err)
			}
		})
	}
	stale := op
	stale.Lease = NewID()
	if _, err := s.NeonTimelineDeletionIntents(ctx, stale, tenantID, timelineID, parent.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale lease read original authority: %v", err)
	}
	if err := s.CancelNeonTimelineDeletionIntent(ctx, stale, pending); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale lease cancelled original authority: %v", err)
	}
	for _, mutate := range []func(*ManagedPlatformOperation){
		func(value *ManagedPlatformOperation) { value.Kind = "create" },
		func(value *ManagedPlatformOperation) { value.Spec.Kind = "supabase" },
		func(value *ManagedPlatformOperation) { value.Maintenance = true },
	} {
		invalid := op
		mutate(&invalid)
		if _, err := s.NeonTimelineDeletionIntents(ctx, invalid, tenantID, timelineID, parent.ID); !errors.Is(err, ErrInput) {
			t.Fatalf("non-delete authority was accepted: %v", err)
		}
		if err := s.CancelNeonTimelineDeletionIntent(ctx, invalid, pending); !errors.Is(err, ErrInput) {
			t.Fatalf("non-delete cancellation was accepted: %v", err)
		}
	}
	for _, mutate := range []func(*PlatformResourceIntent){
		func(value *PlatformResourceIntent) { value.ID = NewID() },
		func(value *PlatformResourceIntent) { value.PlatformID = NewID() },
		func(value *PlatformResourceIntent) { value.PlatformRevision = op.Revision },
		func(value *PlatformResourceIntent) { value.OwnerOperationID = NewID() },
		func(value *PlatformResourceIntent) { value.ExternalKey += "-changed" },
		func(value *PlatformResourceIntent) { value.Component = "tenant" },
	} {
		invalid := pending
		mutate(&invalid)
		if err := s.CancelNeonTimelineDeletionIntent(ctx, op, invalid); err == nil {
			t.Fatal("changed original intent was cancelled")
		}
	}
	if _, err := s.NeonTimelineDeletionIntents(ctx, op, tenantID, timelineID, parent.ID); err != nil {
		t.Fatalf("refused operations changed original authority: %v", err)
	}
}

func TestNeonTimelineDeletionIntentsRejectRevokedKey(t *testing.T) {
	s, principal, op, parent, pending := neonOlderTimelineDeleteFixture(t)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, "UPDATE api_keys SET permissions=ARRAY['deployments:read'] WHERE id=$1", principal.KeyID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NeonTimelineDeletionIntents(ctx, op, strings.Repeat("a", 32), strings.Repeat("b", 32), parent.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked key read original authority: %v", err)
	}
	if err := s.CancelNeonTimelineDeletionIntent(ctx, op, pending); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked key cancelled original authority: %v", err)
	}
}

func TestNeonTimelineDeletionIntentsAllowRegistrationOnlyCleanup(t *testing.T) {
	s, _, op := neonUnprovisionedDeleteFixture(t, nil)
	items, err := s.NeonTimelineDeletionIntents(context.Background(), op, strings.Repeat("a", 32), strings.Repeat("b", 32), "")
	if err != nil || len(items) != 0 {
		t.Fatalf("empty provider state blocked cleanup: count=%d error=%v", len(items), err)
	}
}
