package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
)

func TestDatabaseObservationClaimsDistributeAndFenceExpiredWorkers(t *testing.T) {
	s, p, fixture := databaseFixture(t)
	ctx := context.Background()
	for _, name := range []string{"observed-one", "observed-two"} {
		d := fixture
		d.ID = NewID()
		d.Spec.Name = name
		if _, err := s.AcceptDatabase(ctx, p, d, 0, name, "create"); err != nil {
			t.Fatal(err)
		}
		op, err := s.ClaimDatabaseOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.RecordDatabaseStep(ctx, op, database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now().Add(-time.Minute)}, "succeeded", "ready", ""); err != nil {
			t.Fatal(err)
		}
	}
	type claim struct {
		d     database.Resource
		lease string
		err   error
	}
	results := make(chan claim, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() { d, lease, err := s.ClaimDatabaseObservation(ctx); results <- claim{d, lease, err} })
	}
	workers.Wait()
	close(results)
	claims := []claim{}
	for c := range results {
		if c.err != nil {
			t.Fatal(c.err)
		}
		claims = append(claims, c)
	}
	if claims[0].d.ID == claims[1].d.ID {
		t.Fatal("workers claimed the same database")
	}
	if _, _, err := s.ClaimDatabaseObservation(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("a leased database was claimed twice", err)
	}
	old := claims[0]
	if _, err := s.Pool.Exec(ctx, "UPDATE managed_databases SET observation_lease_until=now()-interval '1 second' WHERE id=$1", old.d.ID); err != nil {
		t.Fatal(err)
	}
	fresh, lease, err := s.ClaimDatabaseObservation(ctx)
	if err != nil || fresh.ID != old.d.ID || lease == old.lease {
		t.Fatal("expired work was not reclaimed", err)
	}
	s.ReleaseDatabaseObservation(old.d.ID, old.lease)
	failure := database.FailureEvidence{Code: "memory_limit_exceeded", Summary: "Development fixture OOM evidence.", OccurredAt: time.Now().Add(-time.Minute), ObservedAt: time.Now(), Revision: 1, Member: "database-1", MemberUID: "fixture-member", Container: "postgres", Reason: "OOMKilled", Source: "kubernetes_container_status"}
	observation := database.Observation{Status: "ready", Revision: 1, ObservedAt: failure.ObservedAt, Failures: []database.FailureEvidence{failure}}
	if err = s.ObserveClaimedDatabase(ctx, old.d.ID, 1, old.lease, observation); !errors.Is(err, ErrClaimLost) {
		t.Fatal("expired worker published", err)
	}
	if err = s.ObserveClaimedDatabase(ctx, fresh.ID, 1, lease, observation); err != nil {
		t.Fatal("old release displaced new worker", err)
	}
	var retained int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM managed_database_failure_evidence WHERE database_id=$1", fresh.ID).Scan(&retained); err != nil || retained != 1 {
		t.Fatal("current fenced worker did not retain one failure record", retained, err)
	}
	s.ReleaseDatabaseObservation(fresh.ID, lease)
	if _, _, err = s.ClaimDatabaseObservation(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("fresh observation was collected immediately again", err)
	}
	history, err := s.DatabaseMetricHistory(ctx, p, fresh.ID, "1h")
	if err != nil || len(history.Items) == 0 {
		t.Fatal("claimed observation did not enter durable history", err)
	}
	remaining := claims[1]
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_databases SET revision=revision+1 WHERE id=$1", remaining.d.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.ObserveClaimedDatabase(ctx, remaining.d.ID, 1, remaining.lease, observation); !errors.Is(err, ErrClaimLost) {
		t.Fatal("old revision observation accepted", err)
	}
}
