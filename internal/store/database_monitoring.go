package store

import (
	"context"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
)

func recordDatabaseMetricPoint(ctx context.Context, tx pgx.Tx, id string, revision int64, o database.Observation) error {
	if o.Revision != revision || o.ObservedAt.IsZero() {
		return nil
	}
	now := time.Now()
	if o.ObservedAt.After(now.Add(10*time.Second)) || o.ObservedAt.Before(now.Add(-24*time.Hour)) {
		return nil
	}
	point := database.MetricPoint{ObservedAt: o.ObservedAt, Revision: revision, Status: o.Status, Resources: o.Metrics, Engine: o.EngineMetrics}
	_, err := tx.Exec(ctx, `INSERT INTO managed_database_metric_samples(database_id,bucket,point) VALUES($1,$2,$3) ON CONFLICT(database_id,bucket) DO UPDATE SET point=EXCLUDED.point WHERE (managed_database_metric_samples.point->>'observed_at')::timestamptz < (EXCLUDED.point->>'observed_at')::timestamptz`, id, o.ObservedAt.UTC().Truncate(time.Minute), JSON(point))
	return err
}

// Cleanup is globally bounded and runs independently of an open dashboard.
// Soft-deleted database samples expire on the same 24-hour schedule.
func (s *Store) PruneDatabaseMetrics(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM managed_database_metric_samples WHERE (database_id,bucket) IN (SELECT database_id,bucket FROM managed_database_metric_samples WHERE bucket<now()-interval '24 hours' ORDER BY bucket LIMIT 1000)`)
	return err
}

func (s *Store) DatabaseMetricHistory(ctx context.Context, p Principal, id, window string) (database.MetricHistory, error) {
	result := database.MetricHistory{Items: []database.MetricPoint{}, Until: time.Now().UTC(), ResolutionSeconds: 60, RetentionHours: 24}
	hours := 1
	switch window {
	case "", "1h":
	case "6h":
		hours = 6
	case "24h":
		hours = 24
	default:
		return result, fmt.Errorf("%w: monitoring range must be 1h, 6h or 24h", ErrInput)
	}
	if _, err := s.Database(ctx, p, id, false); err != nil {
		return result, err
	}
	result.From = result.Until.Add(-time.Duration(hours) * time.Hour)
	rows, err := s.Pool.Query(ctx, `SELECT point FROM managed_database_metric_samples WHERE database_id=$1 AND bucket >= $2 AND bucket <= $3 ORDER BY bucket LIMIT 1441`, id, result.From.Truncate(time.Minute), result.Until)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var point database.MetricPoint
		if err = rows.Scan(&point); err != nil {
			return result, err
		}
		result.Items = append(result.Items, point)
	}
	return result, rows.Err()
}
