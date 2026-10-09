package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
)

func TestDatabaseFailureHistorySurvivesHealthyObservationsAndEnforcesScope(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "failure-history", "create"); err != nil {
		t.Fatal(err)
	}
	when := time.Now().UTC().Add(-time.Minute)
	exit, restart, limit := int32(137), int32(1), int64(256<<20)
	evidence := database.FailureEvidence{Code: "memory_limit_exceeded", Summary: "PostgreSQL member database-1 exceeded its 256 MiB memory limit.", OccurredAt: when, ObservedAt: when.Add(time.Second), Revision: 1, Member: "database-1", MemberUID: "old-primary", Container: "postgres", Reason: "OOMKilled", ExitCode: &exit, RestartCount: &restart, MemoryLimitBytes: &limit, Source: "kubernetes_container_status"}
	if err := s.ObserveDatabase(ctx, d.ID, 1, database.Observation{Revision: 1, ObservedAt: evidence.ObservedAt, Status: "unknown", Failures: []database.FailureEvidence{evidence}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveDatabase(ctx, d.ID, 1, database.Observation{Revision: 1, ObservedAt: evidence.ObservedAt.Add(time.Second), Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	history, err := s.DatabaseFailureHistory(ctx, p, d.ID)
	if err != nil || len(history.Items) != 1 || history.Items[0].MemberUID != "old-primary" || history.Limit != database.MaxFailureHistory {
		t.Fatal("healthy replacement did not retain exact failure evidence", history, err)
	}
	outsider := p
	outsider.Application = "application-key"
	if _, err = s.DatabaseFailureHistory(ctx, outsider, d.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("application key read retained database failure evidence", err)
	}
}

func TestDatabaseFailureHistoryUpsertsAndBoundsEvidence(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "failure-bound", "create"); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for i := 0; i < database.MaxFailureHistory+2; i++ {
		evidence := database.FailureEvidence{Code: "memory_limit_exceeded", Summary: "Kubernetes reported OOMKilled.", OccurredAt: base.Add(time.Duration(i) * time.Second), ObservedAt: base.Add(time.Duration(i) * time.Minute), Revision: 1, Member: "database-1", MemberUID: "member", Container: "postgres", Reason: "OOMKilled", Source: "kubernetes_container_status"}
		if err := s.ObserveDatabase(ctx, d.ID, 1, database.Observation{Revision: 1, ObservedAt: evidence.ObservedAt, Status: "unknown", Failures: []database.FailureEvidence{evidence}}); err != nil {
			t.Fatal(err)
		}
	}
	history, err := s.DatabaseFailureHistory(ctx, p, d.ID)
	if err != nil || len(history.Items) != database.MaxFailureHistory {
		t.Fatal("failure history exceeded its bound", len(history.Items), err)
	}
	latest := history.Items[0].FailureEvidence
	latest.ObservedAt = latest.ObservedAt.Add(time.Second)
	if err = s.ObserveDatabase(ctx, d.ID, 1, database.Observation{Revision: 1, ObservedAt: latest.ObservedAt, Status: "unknown", Failures: []database.FailureEvidence{latest}}); err != nil {
		t.Fatal(err)
	}
	history, err = s.DatabaseFailureHistory(ctx, p, d.ID)
	if err != nil || len(history.Items) != database.MaxFailureHistory {
		t.Fatal("repeated evidence changed the bounded history", len(history.Items), err)
	}
	if !history.Items[0].LastSeenAt.Equal(latest.ObservedAt) {
		t.Fatal("repeated evidence did not update one retained record", history.Items[0])
	}
}
