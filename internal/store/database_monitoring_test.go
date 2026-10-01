package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
)

func TestDatabaseHistoryScopeRetentionAndLateObservations(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "monitoring-fixture", "create"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	cpu, memory, sampled := 12.0, int64(4096), now.Add(-10*time.Minute)
	observation := database.Observation{Revision: 1, ObservedAt: now.Add(-10 * time.Minute), Status: "ready", Metrics: &database.Metrics{Available: true, CPU: &cpu, Memory: &memory, SampledAt: &sampled}}
	if err := s.ObserveDatabase(ctx, d.ID, 1, observation); err != nil {
		t.Fatal(err)
	}
	observation.ObservedAt = now.Add(-9 * time.Minute)
	observation.Metrics = nil
	observation.Status = "unknown"
	if err := s.ObserveDatabase(ctx, d.ID, 1, observation); err != nil {
		t.Fatal(err)
	}
	late := observation
	late.ObservedAt = now.Add(-10 * time.Minute)
	late.Status = "ready"
	if err := s.ObserveDatabase(ctx, d.ID, 1, late); err != nil {
		t.Fatal(err)
	}
	latest, err := s.Database(ctx, p, d.ID, false)
	if err != nil || latest.Observation.Status != "unknown" {
		t.Fatal("late observation replaced newer health", err)
	}
	history, err := s.DatabaseMetricHistory(ctx, p, d.ID, "1h")
	if err != nil || len(history.Items) != 2 || history.Items[0].Resources == nil || history.Items[1].Resources != nil {
		t.Fatal("source samples and gaps were not retained", err, history)
	}
	outsider := p
	outsider.Application = "restricted-app"
	if _, err = s.DatabaseMetricHistory(ctx, outsider, d.ID, "1h"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("application key accessed database history", err)
	}
	if _, err = s.DatabaseMetricHistory(ctx, p, d.ID, "30d"); !errors.Is(err, ErrInput) {
		t.Fatal("unbounded window accepted", err)
	}
	// Multiple observations in a minute retain only the latest point.
	observation.ObservedAt = now.Add(-9*time.Minute + time.Second)
	if err = s.ObserveDatabase(ctx, d.ID, 1, observation); err != nil {
		t.Fatal(err)
	}
	history, err = s.DatabaseMetricHistory(ctx, p, d.ID, "24h")
	if err != nil || len(history.Items) != 2 {
		t.Fatal("minute resolution was not bounded", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_database_metric_samples SET bucket=bucket-interval '25 hours' WHERE database_id=$1", d.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.PruneDatabaseMetrics(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM managed_database_metric_samples WHERE database_id=$1", d.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired history retained", err)
	}
}
