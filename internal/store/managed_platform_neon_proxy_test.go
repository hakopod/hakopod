package store

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func neonProxyRecord(op ManagedPlatformOperation, sealed byte) NeonProxyEndpointRecord {
	return NeonProxyEndpointRecord{
		EndpointID:       op.PlatformID,
		PlatformID:       op.PlatformID,
		PlatformRevision: op.Revision,
		OwnerOperationID: op.ID,
		Generation:       op.Revision,
		Enabled:          true,
		Address:          "neon-compute-0.managed-platform-" + op.PlatformID + ".svc:55433",
		ServerName:       "neon-compute-0.managed-platform-" + op.PlatformID + ".svc",
		ProjectID:        op.PlatformID,
		BranchID:         "22222222222222222222222222222222",
		ComputeID:        "compute-0",
		EncryptedRoles:   bytes.Repeat([]byte{sealed}, 32),
	}
}

func TestManagedPlatformNeonProxyEndpointLifecycleIsGenerationFenced(t *testing.T) {
	s, principal, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-runtime-plan"), review, 0, "neon-proxy-create", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record := neonProxyRecord(op, 1)
	if err = s.ActivateNeonProxyEndpoint(ctx, op, record); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NeonProxyEndpoint(ctx, op.PlatformID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("proxy route became visible before the platform was ready", err)
	}
	retry := record
	retry.EncryptedRoles = bytes.Repeat([]byte{2}, 32)
	if err = s.ActivateNeonProxyEndpoint(ctx, op, retry); err != nil {
		t.Fatal("same-operation retry did not replace authenticated ciphertext", err)
	}
	if err = s.RecordManagedPlatformStep(ctx, op, "succeeded", "ready", "", map[string]any{"status": "ready"}); err != nil {
		t.Fatal(err)
	}
	visible, err := s.NeonProxyEndpoint(ctx, op.PlatformID)
	if err != nil || !bytes.Equal(visible.EncryptedRoles, retry.EncryptedRoles) {
		t.Fatal("ready proxy route was not returned with retry ciphertext", err)
	}

	current, err := s.ManagedPlatform(ctx, principal, item.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	updateReview := managedPlatformReview(t, s, principal, current, plan, 1, "update")
	if _, err = s.AcceptManagedPlatform(ctx, principal, current, plan, []byte("sealed-update"), updateReview, 1, "neon-proxy-update", "update"); err != nil {
		t.Fatal(err)
	}
	update, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stale := neonProxyRecord(update, 3)
	stale.Generation = 1
	if err = s.ActivateNeonProxyEndpoint(ctx, update, stale); !errors.Is(err, ErrInput) {
		t.Fatal("stale proxy generation was accepted", err)
	}
	foreign := neonProxyRecord(update, 3)
	foreign.EndpointID = "33333333333333333333333333333333"
	if err = s.ActivateNeonProxyEndpoint(ctx, update, foreign); !errors.Is(err, ErrInput) {
		t.Fatal("foreign proxy endpoint was accepted", err)
	}
	next := neonProxyRecord(update, 3)
	if err = s.ActivateNeonProxyEndpoint(ctx, update, next); err != nil {
		t.Fatal("optimistic proxy generation update failed", err)
	}
	if err = s.RecordManagedPlatformStep(ctx, update, "succeeded", "ready", "", map[string]any{"status": "ready"}); err != nil {
		t.Fatal(err)
	}

	current, err = s.ManagedPlatform(ctx, principal, item.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	deleteReview := managedPlatformReview(t, s, principal, current, plan, 2, "delete")
	if _, err = s.AcceptManagedPlatform(ctx, principal, current, plan, []byte("sealed-delete"), deleteReview, 2, "neon-proxy-delete", "delete"); err != nil {
		t.Fatal(err)
	}
	deleting, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeNeonProxyEndpoint(ctx, deleting); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NeonProxyEndpoint(ctx, deleting.PlatformID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("revoked proxy route remained visible", err)
	}
}

func TestManagedPlatformNeonProxyEndpointAllowsSkippedInactiveRevision(t *testing.T) {
	t.Run("delete revokes generation before failed update", func(t *testing.T) {
		s, principal, item, plan := managedPlatformFixture(t)
		ctx := context.Background()
		review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
		if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-create"), review, 0, "neon-skip-create", "create"); err != nil {
			t.Fatal(err)
		}
		create, err := s.ClaimManagedPlatformOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.ActivateNeonProxyEndpoint(ctx, create, neonProxyRecord(create, 1)); err != nil {
			t.Fatal(err)
		}
		if err = s.RecordManagedPlatformStep(ctx, create, "succeeded", "ready", "", map[string]any{"status": "ready"}); err != nil {
			t.Fatal(err)
		}

		current, err := s.ManagedPlatform(ctx, principal, item.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		updateReview := managedPlatformReview(t, s, principal, current, plan, 1, "update")
		if _, err = s.AcceptManagedPlatform(ctx, principal, current, plan, []byte("sealed-update"), updateReview, 1, "neon-skip-update", "update"); err != nil {
			t.Fatal(err)
		}
		update, err := s.ClaimManagedPlatformOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.RecordManagedPlatformStep(ctx, update, "failed", "before-proxy-activation", "", map[string]any{"status": "failed"}); err != nil {
			t.Fatal(err)
		}

		current, err = s.ManagedPlatform(ctx, principal, item.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		deleteReview := managedPlatformReview(t, s, principal, current, plan, 2, "delete")
		if _, err = s.AcceptManagedPlatform(ctx, principal, current, plan, []byte("sealed-delete"), deleteReview, 2, "neon-skip-delete", "delete"); err != nil {
			t.Fatal(err)
		}
		deleting, err := s.ClaimManagedPlatformOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.RevokeNeonProxyEndpoint(ctx, deleting); err != nil {
			t.Fatal("revision 3 delete did not revoke generation 1", err)
		}
		if err = s.RevokeNeonProxyEndpoint(ctx, deleting); err != nil {
			t.Fatal("repeated revocation was not idempotent", err)
		}
		if _, err = s.NeonProxyEndpoint(ctx, item.ID); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("revoked proxy route remained visible", err)
		}
		var generation, revision int64
		var owner string
		var enabled bool
		if err = s.Pool.QueryRow(ctx, "SELECT generation,platform_revision,owner_operation_id,enabled FROM managed_platform_neon_proxy_endpoints WHERE endpoint_id=$1", item.ID).Scan(&generation, &revision, &owner, &enabled); err != nil {
			t.Fatal(err)
		}
		if generation != 3 || revision != 3 || owner != deleting.ID || enabled {
			t.Fatalf("revocation did not advance ownership to delete operation: generation=%d revision=%d owner=%q enabled=%t", generation, revision, owner, enabled)
		}
	})

	t.Run("update activates after failed inactive revision", func(t *testing.T) {
		s, principal, item, plan := managedPlatformFixture(t)
		ctx := context.Background()
		review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
		if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-create"), review, 0, "neon-gap-create", "create"); err != nil {
			t.Fatal(err)
		}
		create, err := s.ClaimManagedPlatformOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.ActivateNeonProxyEndpoint(ctx, create, neonProxyRecord(create, 1)); err != nil {
			t.Fatal(err)
		}
		if err = s.RecordManagedPlatformStep(ctx, create, "succeeded", "ready", "", map[string]any{"status": "ready"}); err != nil {
			t.Fatal(err)
		}
		current, err := s.ManagedPlatform(ctx, principal, item.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		updateReview := managedPlatformReview(t, s, principal, current, plan, 1, "update")
		if _, err = s.AcceptManagedPlatform(ctx, principal, current, plan, []byte("sealed-update-2"), updateReview, 1, "neon-gap-update-2", "update"); err != nil {
			t.Fatal(err)
		}
		failed, err := s.ClaimManagedPlatformOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.RecordManagedPlatformStep(ctx, failed, "failed", "before-proxy-activation", "", map[string]any{"status": "failed"}); err != nil {
			t.Fatal(err)
		}
		current, err = s.ManagedPlatform(ctx, principal, item.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		updateReview = managedPlatformReview(t, s, principal, current, plan, 2, "update")
		if _, err = s.AcceptManagedPlatform(ctx, principal, current, plan, []byte("sealed-update-3"), updateReview, 2, "neon-gap-update-3", "update"); err != nil {
			t.Fatal(err)
		}
		third, err := s.ClaimManagedPlatformOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.ActivateNeonProxyEndpoint(ctx, third, neonProxyRecord(third, 3)); err != nil {
			t.Fatal("revision 3 update did not replace generation 1", err)
		}
		var generation int64
		if err = s.Pool.QueryRow(ctx, "SELECT generation FROM managed_platform_neon_proxy_endpoints WHERE endpoint_id=$1", item.ID).Scan(&generation); err != nil || generation != 3 {
			t.Fatalf("proxy generation did not advance across inactive revision: %d %v", generation, err)
		}
	})
}
