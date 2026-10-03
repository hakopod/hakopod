package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestManagedPlatformHeartbeatPreventsClaimAfterOriginalLeaseExpiry(t *testing.T) {
	s, principal, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-runtime-plan"), review, 0, "platform-renewal", "create"); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var originalDeadline time.Time
	if err = s.Pool.QueryRow(ctx, `UPDATE managed_platform_operations
		SET lease_until=clock_timestamp()+interval '2 seconds'
		WHERE id=$1 AND lease=$2 AND status='running'
		RETURNING lease_until`, claimed.ID, claimed.Lease).Scan(&originalDeadline); err != nil {
		t.Fatal(err)
	}
	if err = s.HeartbeatManagedPlatformOperation(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	var renewedDeadline time.Time
	if err = s.Pool.QueryRow(ctx, `SELECT lease_until FROM managed_platform_operations WHERE id=$1`, claimed.ID).Scan(&renewedDeadline); err != nil {
		t.Fatal(err)
	}
	if !renewedDeadline.After(originalDeadline.Add(20 * time.Second)) {
		t.Fatalf("heartbeat did not extend the exact operation lease: original=%v renewed=%v", originalDeadline, renewedDeadline)
	}

	time.Sleep(time.Until(originalDeadline) + 50*time.Millisecond)
	if _, err = s.ClaimManagedPlatformOperation(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("competing worker claimed an operation protected by its renewed lease: %v", err)
	}
}
