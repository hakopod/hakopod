package store

import (
	"context"
	"errors"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/jackc/pgx/v5"
)

type DatabaseColdStorageFence struct {
	DatabaseID   string
	Revision     int64
	JobID        string
	Kind         string
	CleanupToken string
}

func (s *Store) AcquireDatabaseColdStorageFence(ctx context.Context, databaseID string, revision int64, jobID, lease, kind string) error {
	if databaseID == "" || revision < 1 || jobID == "" || lease == "" || kind != "backup" && kind != "restore" {
		return backup.ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status, engine string
	if err = tx.QueryRow(ctx, `SELECT status,spec->>'engine' FROM managed_databases WHERE id=$1 AND revision=$2 AND deleted_at IS NULL FOR UPDATE`, databaseID, revision).Scan(&status, &engine); err != nil {
		return backupError(err)
	}
	if engine != "duckdb" || kind == "backup" && status != "ready" || kind == "restore" && status != "restoring" {
		return backup.ErrConflict
	}
	var authorized bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM backup_jobs WHERE id=$1 AND lease=$2 AND status='running' AND lease_until>clock_timestamp() AND NOT cancel_requested AND kind=$3 AND CASE WHEN $3='backup' THEN source->>'managed_database_id'=$4 ELSE target->>'managed_database_id'=$4 END)`, jobID, lease, kind, databaseID).Scan(&authorized)
	if err != nil {
		return err
	}
	if !authorized {
		return backup.ErrConflict
	}
	var existing bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_database_cold_storage_fences WHERE database_id=$1)`, databaseID).Scan(&existing); err != nil {
		return err
	}
	if existing {
		if err = checkDatabaseColdStorageFence(ctx, tx, databaseID, revision, jobID, kind); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if idle, idleErr := databaseMaintenanceIdle(ctx, tx, databaseID); idleErr != nil {
		return idleErr
	} else if !idle {
		return errDatabaseMaintenanceActive
	}
	_, err = tx.Exec(ctx, `INSERT INTO managed_database_cold_storage_fences(database_id,database_revision,backup_job_id,kind) VALUES($1,$2,$3,$4) ON CONFLICT(database_id) DO NOTHING`, databaseID, revision, jobID, kind)
	if err != nil {
		return err
	}
	if err = checkDatabaseColdStorageFence(ctx, tx, databaseID, revision, jobID, kind); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type coldStorageQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func checkDatabaseColdStorageFence(ctx context.Context, q coldStorageQuerier, databaseID string, revision int64, jobID, kind string) error {
	var valid bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_database_cold_storage_fences WHERE database_id=$1 AND database_revision=$2 AND backup_job_id=$3 AND kind=$4)`, databaseID, revision, jobID, kind).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrClaimLost
	}
	return nil
}

func (s *Store) CheckDatabaseColdStorageFence(ctx context.Context, databaseID string, revision int64, jobID, kind string) error {
	return checkDatabaseColdStorageFence(ctx, s.Pool, databaseID, revision, jobID, kind)
}

func (s *Store) CheckDatabaseColdStorageWorker(ctx context.Context, databaseID string, revision int64, jobID, lease, kind string) error {
	if err := s.CheckDatabaseColdStorageFence(ctx, databaseID, revision, jobID, kind); err != nil {
		return err
	}
	var valid bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM backup_jobs job JOIN managed_database_cold_storage_fences cold ON cold.backup_job_id=job.id WHERE job.id=$1 AND job.lease=$2 AND job.status='running' AND job.lease_until>clock_timestamp() AND NOT job.cancel_requested AND job.kind=$3 AND cold.database_id=$4 AND cold.database_revision=$5 AND cold.cleanup_token='')`, jobID, lease, kind, databaseID, revision).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrClaimLost
	}
	return nil
}

func (s *Store) ClaimDatabaseColdStorageCleanup(ctx context.Context, jobID, lease string) (DatabaseColdStorageFence, error) {
	var fence DatabaseColdStorageFence
	token := NewID()
	err := s.Pool.QueryRow(ctx, `UPDATE managed_database_cold_storage_fences fence SET cleanup_token=$3,cleanup_worker_lease=$2,updated_at=now() FROM backup_jobs job WHERE fence.backup_job_id=$1 AND job.id=fence.backup_job_id AND job.lease=$2 AND job.status='running' AND job.lease_until>clock_timestamp() RETURNING fence.database_id,fence.database_revision,fence.backup_job_id,fence.kind,fence.cleanup_token`, jobID, lease, token).Scan(&fence.DatabaseID, &fence.Revision, &fence.JobID, &fence.Kind, &fence.CleanupToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return fence, ErrClaimLost
	}
	return fence, err
}

func (s *Store) CheckDatabaseColdStorageCleanup(ctx context.Context, fence DatabaseColdStorageFence) error {
	var valid bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_database_cold_storage_fences cold JOIN backup_jobs job ON job.id=cold.backup_job_id JOIN managed_databases database ON database.id=cold.database_id WHERE cold.database_id=$1 AND cold.database_revision=$2 AND cold.backup_job_id=$3 AND cold.kind=$4 AND cold.cleanup_token=$5 AND cold.cleanup_worker_lease=job.lease AND job.status='running' AND job.lease_until>clock_timestamp() AND database.revision=cold.database_revision AND database.deleted_at IS NULL AND database.spec->>'engine'='duckdb' AND (cold.kind='backup' AND database.status='ready' OR cold.kind='restore' AND database.status='restoring'))`, fence.DatabaseID, fence.Revision, fence.JobID, fence.Kind, fence.CleanupToken).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrClaimLost
	}
	return nil
}

func (s *Store) DatabaseColdStorageFenceForJob(ctx context.Context, jobID string) (DatabaseColdStorageFence, bool, error) {
	var fence DatabaseColdStorageFence
	err := s.Pool.QueryRow(ctx, `SELECT database_id,database_revision,backup_job_id,kind,cleanup_token FROM managed_database_cold_storage_fences WHERE backup_job_id=$1`, jobID).Scan(&fence.DatabaseID, &fence.Revision, &fence.JobID, &fence.Kind, &fence.CleanupToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return fence, false, nil
	}
	return fence, err == nil, err
}

func (s *Store) ReleaseDatabaseColdStorageFence(ctx context.Context, fence DatabaseColdStorageFence) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM managed_database_cold_storage_fences cold USING backup_jobs job,managed_databases database WHERE cold.database_id=$1 AND cold.database_revision=$2 AND cold.backup_job_id=$3 AND cold.kind=$4 AND cold.cleanup_token=$5 AND cold.cleanup_worker_lease=job.lease AND job.id=cold.backup_job_id AND job.status='running' AND job.lease_until>clock_timestamp() AND database.id=cold.database_id AND database.revision=cold.database_revision AND database.deleted_at IS NULL`, fence.DatabaseID, fence.Revision, fence.JobID, fence.Kind, fence.CleanupToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrClaimLost
	}
	return nil
}

func databaseColdStorageIdle(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var idle bool
	err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM managed_database_cold_storage_fences WHERE database_id=$1)`, id).Scan(&idle)
	return idle, err
}
