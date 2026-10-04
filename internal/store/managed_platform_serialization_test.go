package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func runBehindManagedPlatformRowUpdate(t *testing.T, s *Store, update func(context.Context, pgx.Tx) error, action func(context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	blocker, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if err = update(ctx, blocker); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() { result <- action(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		err = s.Pool.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND pid<>pg_backend_pid()
			AND state='active' AND wait_event_type='Lock'
			AND query LIKE '%managed_platform_operations o JOIN managed_platforms p%FOR UPDATE OF o,p%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("managed platform transaction did not reach the operation row lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		return err
	case <-ctx.Done():
		t.Fatal("managed platform transaction did not finish after blocker committed")
		return ctx.Err()
	}
}

func managedPlatformSerializationFixture(t *testing.T) (*Store, Principal, ManagedPlatformOperation) {
	t.Helper()
	s, principal, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-runtime-plan"), review, 0, "serialization", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return s, principal, op
}

func TestManagedPlatformHeartbeatRetriesWholeSerializableTransaction(t *testing.T) {
	s, _, op := managedPlatformSerializationFixture(t)
	update := func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE managed_platform_operations SET lease_until=lease_until+interval '1 second' WHERE id=$1", op.ID)
		return err
	}
	err := runBehindManagedPlatformRowUpdate(t, s, update, func(ctx context.Context) error {
		return s.heartbeatManagedPlatformOperation(ctx, op)
	})
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "40001" {
		t.Fatalf("unretried heartbeat did not expose the forced stale snapshot: %v", err)
	}
	err = runBehindManagedPlatformRowUpdate(t, s, update, func(ctx context.Context) error {
		return s.HeartbeatManagedPlatformOperation(ctx, op)
	})
	if err != nil {
		t.Fatalf("heartbeat did not retry its whole serializable transaction: %v", err)
	}
	var remaining float64
	if err = s.Pool.QueryRow(context.Background(), "SELECT EXTRACT(EPOCH FROM lease_until-clock_timestamp()) FROM managed_platform_operations WHERE id=$1", op.ID).Scan(&remaining); err != nil || remaining <= 20 {
		t.Fatalf("retried heartbeat did not extend the live lease: remaining=%v err=%v", remaining, err)
	}
}

func TestManagedPlatformIntentConfirmationRetriesWholeSerializableTransaction(t *testing.T) {
	s, _, op := managedPlatformSerializationFixture(t)
	ctx := context.Background()
	intent, err := s.ReservePlatformResourceIntent(ctx, op, PlatformResourceIntent{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: "tenant", Kind: "neon_tenant", ExternalKey: "11111111111111111111111111111111", OwnerOperationID: op.ID})
	if err != nil {
		t.Fatal(err)
	}
	claim := PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: intent.Component, Kind: intent.Kind, ResourceID: intent.ExternalKey, ImmutableGeneration: 1, OwnerOperationID: op.ID}
	update := func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE managed_platform_operations SET lease_until=lease_until+interval '1 second' WHERE id=$1", op.ID)
		return err
	}
	err = runBehindManagedPlatformRowUpdate(t, s, update, func(ctx context.Context) error {
		return s.confirmPlatformResourceIntent(ctx, op, intent, claim)
	})
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "40001" {
		t.Fatalf("unretried confirmation did not expose the forced stale snapshot: %v", err)
	}
	err = runBehindManagedPlatformRowUpdate(t, s, update, func(ctx context.Context) error {
		return s.ConfirmPlatformResourceIntent(ctx, op, intent, claim)
	})
	if err != nil {
		t.Fatalf("confirmation did not retry its whole serializable transaction: %v", err)
	}
	var intents, claims int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM platform_resource_intents WHERE id=$1 AND confirmed_at IS NOT NULL AND released_at IS NULL", intent.ID).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM platform_component_resources WHERE intent_id=$1 AND released_at IS NULL", intent.ID).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if intents != 1 || claims != 1 {
		t.Fatalf("confirmation retry duplicated or lost durable state: intents=%d claims=%d", intents, claims)
	}
}

func TestManagedPlatformSerializationRetryRechecksLeaseAndAuthority(t *testing.T) {
	t.Run("stale lease", func(t *testing.T) {
		s, _, op := managedPlatformSerializationFixture(t)
		err := runBehindManagedPlatformRowUpdate(t, s, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "UPDATE managed_platform_operations SET lease=$2,lease_until=clock_timestamp()+interval '30 seconds' WHERE id=$1", op.ID, NewID())
			return err
		}, func(ctx context.Context) error { return s.HeartbeatManagedPlatformOperation(ctx, op) })
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("retry used authority from the aborted stale-lease snapshot: %v", err)
		}
	})
	t.Run("revoked authority", func(t *testing.T) {
		s, principal, op := managedPlatformSerializationFixture(t)
		err := runBehindManagedPlatformRowUpdate(t, s, func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "UPDATE api_keys SET revoked_at=clock_timestamp() WHERE id=$1", principal.KeyID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "UPDATE managed_platform_operations SET lease_until=lease_until+interval '1 second' WHERE id=$1", op.ID)
			return err
		}, func(ctx context.Context) error { return s.HeartbeatManagedPlatformOperation(ctx, op) })
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("retry used authority from the aborted pre-revocation snapshot: %v", err)
		}
	})
}
