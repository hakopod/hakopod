package store

import (
	"context"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

// Observation leases distribute bounded read work across workers and processes.
// They do not authorize mutations; database maintenance uses its own fence.
func (s *Store) ClaimDatabaseObservation(ctx context.Context) (database.Resource, string, error) {
	lease := NewID()
	d, err := scanDatabase(s.Pool.QueryRow(ctx, `UPDATE managed_databases SET observation_lease=$1,observation_lease_until=clock_timestamp()+interval '60 seconds' WHERE id=(SELECT id FROM managed_databases WHERE deleted_at IS NULL AND (status='ready' OR (status='failed' AND spec->>'engine'='vitess')) AND observation_next_at<=now() AND (observation_lease_until IS NULL OR observation_lease_until<now()) AND COALESCE((observation->>'observed_at')::timestamptz,'epoch')<now()-interval '10 seconds' ORDER BY COALESCE((observation->>'observed_at')::timestamptz,'epoch'),id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING `+databaseCols, lease))
	return d, lease, err
}

func (s *Store) ReleaseDatabaseObservation(id, lease string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = s.Pool.Exec(ctx, `UPDATE managed_databases SET observation_lease='',observation_lease_until=NULL,observation_next_at=clock_timestamp()+interval '10 seconds' WHERE id=$1 AND observation_lease=$2`, id, lease)
}

func (s *Store) ObserveClaimedDatabase(ctx context.Context, id string, revision int64, lease string, o database.Observation) error {
	if lease == "" {
		return ErrInput
	}
	return s.observeDatabase(ctx, id, revision, lease, o)
}
