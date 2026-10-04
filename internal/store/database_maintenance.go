package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// DatabaseMaintenanceClaim fences certificate maintenance and database network
// reconciliation against lifecycle operations and backups. Claims expire after 25s.
type DatabaseMaintenanceClaim struct {
	store         *Store
	id, lease     string
	revision      int64
	nativeStorage bool
}

func (s *Store) ClaimDatabaseMaintenance(ctx context.Context, id string, revision int64) (*DatabaseMaintenanceClaim, error) {
	return s.claimDatabaseMaintenance(ctx, id, revision, false)
}

// Native storage revocation must not wait for a logical archive to finish. This
// claim authorizes only backup jobs, their credentials and network rules. It
// must not be used for certificate renewal or database process changes.
func (s *Store) ClaimDatabaseNativeStorageMaintenance(ctx context.Context, id string, revision int64) (*DatabaseMaintenanceClaim, error) {
	return s.claimDatabaseMaintenance(ctx, id, revision, true)
}

func (s *Store) claimDatabaseMaintenance(ctx context.Context, id string, revision int64, nativeStorage bool) (*DatabaseMaintenanceClaim, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Acquire the same row lock as lifecycle/backup acceptance before inspecting
	// related jobs. A single UPDATE with a subquery could use an older snapshot
	// after waiting for an accepting transaction to release this row.
	var locked string
	err = tx.QueryRow(ctx, `SELECT id FROM managed_databases WHERE id=$1 AND revision=$2 AND (status='ready' OR ($3 AND status='failed')) AND (NOT $3 OR spec->>'engine'='vitess') AND deleted_at IS NULL AND (maintenance_lease_until IS NULL OR maintenance_lease_until<now()) FOR UPDATE SKIP LOCKED`, id, revision, nativeStorage).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var busy bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_database_operations WHERE database_id=$1 AND status IN ('queued','running')) OR EXISTS(SELECT 1 FROM managed_database_public_endpoint_operations WHERE database_id=$1 AND status IN ('queued','running')) OR (NOT $2 AND EXISTS(SELECT 1 FROM backup_jobs WHERE status IN ('queued','running') AND (source->>'managed_database_id'=$1 OR target->>'managed_database_id'=$1)))`, id, nativeStorage).Scan(&busy)
	if err != nil {
		return nil, err
	}
	if busy {
		return nil, nil
	}
	lease := NewID()
	_, err = tx.Exec(ctx, `UPDATE managed_databases SET maintenance_lease=$2,maintenance_lease_until=clock_timestamp()+interval '25 seconds' WHERE id=$1`, id, lease)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &DatabaseMaintenanceClaim{store: s, id: id, lease: lease, revision: revision, nativeStorage: nativeStorage}, nil
}

func (c *DatabaseMaintenanceClaim) Check(ctx context.Context) error {
	var valid bool
	err := c.store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_databases d WHERE d.id=$1 AND d.revision=$2 AND d.maintenance_lease=$3 AND d.maintenance_lease_until>clock_timestamp() AND (d.status='ready' OR ($4 AND d.status='failed')) AND (NOT $4 OR d.spec->>'engine'='vitess') AND d.deleted_at IS NULL)`, c.id, c.revision, c.lease, c.nativeStorage).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrClaimLost
	}
	return nil
}

func (c *DatabaseMaintenanceClaim) Release() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = c.store.Pool.Exec(ctx, `UPDATE managed_databases SET maintenance_lease='',maintenance_lease_until=NULL WHERE id=$1 AND revision=$2 AND maintenance_lease=$3`, c.id, c.revision, c.lease)
}

// The caller holds the database row lock shared with maintenance claiming.
func databaseMaintenanceIdle(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var idle bool
	err := tx.QueryRow(ctx, `SELECT maintenance_lease_until IS NULL OR maintenance_lease_until<=clock_timestamp() FROM managed_databases WHERE id=$1`, id).Scan(&idle)
	return idle, err
}
