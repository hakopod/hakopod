package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
)

const databaseMigrationRecoveryColumns = `id,database_id,status,phase,message,plan,before_evidence,after_evidence,created_at,started_at,finished_at,identity_id,key_id,lease`

func scanDatabaseMigrationRecovery(row scanner) (database.MigrationLockRecoveryOperation, error) {
	var op database.MigrationLockRecoveryOperation
	err := row.Scan(&op.ID, &op.DatabaseID, &op.Status, &op.Phase, &op.Message, &op.Plan, &op.Before, &op.After, &op.CreatedAt, &op.StartedAt, &op.FinishedAt, &op.IdentityID, &op.KeyID, &op.Lease)
	return op, err
}

func (s *Store) MigrationLockRecoveryOperation(ctx context.Context, p Principal, id string) (database.MigrationLockRecoveryOperation, error) {
	op, err := scanDatabaseMigrationRecovery(s.Pool.QueryRow(ctx, `SELECT `+databaseMigrationRecoveryColumns+` FROM database_migration_lock_recovery_operations WHERE id=$1`, id))
	if err != nil {
		return op, err
	}
	d, err := s.DatabaseInternal(ctx, op.DatabaseID)
	if err != nil {
		return op, err
	}
	if !p.AllowsDatabase(d.Project, d.Environment, false) || !p.Allows("deployments:read", d.Project, d.Environment, op.Plan.ApplicationName) {
		return database.MigrationLockRecoveryOperation{}, ErrForbidden
	}
	return op, nil
}

func (s *Store) SaveMigrationLockRecoveryPlan(ctx context.Context, p Principal, databaseID, applicationID, service, profile string, evidence database.MigrationLockEvidence) (database.MigrationLockRecoveryPlan, error) {
	d, err := s.Database(ctx, p, databaseID, true)
	if err != nil {
		return database.MigrationLockRecoveryPlan{}, err
	}
	a, err := s.Application(ctx, applicationID)
	if err != nil {
		return database.MigrationLockRecoveryPlan{}, err
	}
	if d.Spec.Engine != "postgresql" || profile != database.InfisicalKnexPostgresProfile {
		return database.MigrationLockRecoveryPlan{}, fmt.Errorf("%w: this migration recovery profile is unsupported", ErrInput)
	}
	if a.Project != d.Project || a.Environment != d.Environment || !p.Allows("deployments:write", a.Project, a.Environment, a.Name) {
		return database.MigrationLockRecoveryPlan{}, ErrForbidden
	}
	if evidence.Profile != profile || evidence.DatabaseID != d.ID || evidence.DatabaseRevision != d.Revision || evidence.ApplicationID != a.ID || evidence.ApplicationRevision != a.Revision || evidence.Service != service || !evidence.Repairable() {
		return database.MigrationLockRecoveryPlan{}, fmt.Errorf("%w: evidence does not prove an abandoned Knex migration lock", ErrConflict)
	}
	plan := database.MigrationLockRecoveryPlan{ID: NewID(), Evidence: evidence, EvidenceSHA256: evidence.Digest(), ApplicationName: a.Name, DatabaseName: d.Spec.Name, Warnings: []string{"Deletes and recreates only the single Knex infisical_migrations_lock row.", "Infisical's heartbeat startup lock is never modified.", "The worker repeats all session, pod, revision and lock checks immediately before repair."}, ExpiresAt: time.Now().UTC().Add(database.ReviewLifetime)}
	_, err = s.Pool.Exec(ctx, `INSERT INTO managed_database_reviews(id,database_id,identity_id,revision,kind,payload,expires_at) VALUES($1,$2,$3,$4,'migration-lock-recovery',$5,$6)`, plan.ID, d.ID, p.ID, d.Revision, JSON(plan), plan.ExpiresAt)
	return plan, err
}

func (s *Store) AcceptMigrationLockRecovery(ctx context.Context, p Principal, databaseID, reviewID, confirmApplication, confirmDatabase, idem string) (database.MigrationLockRecoveryOperation, error) {
	var empty database.MigrationLockRecoveryOperation
	if len(idem) < 8 || len(idem) > 128 {
		return empty, ErrInput
	}
	hash := sha256.Sum256(JSON(struct{ DatabaseID, ReviewID string }{databaseID, reviewID}))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,93))`, p.ID+":"+idem); err != nil {
		return empty, err
	}
	var oldID string
	var oldHash []byte
	err = tx.QueryRow(ctx, `SELECT id,request_hash FROM database_migration_lock_recovery_operations WHERE identity_id=$1 AND idempotency_key=$2`, p.ID, idem).Scan(&oldID, &oldHash)
	if err == nil {
		if !bytes.Equal(oldHash, hash[:]) {
			return empty, ErrConflict
		}
		op, scanErr := scanDatabaseMigrationRecovery(tx.QueryRow(ctx, `SELECT `+databaseMigrationRecoveryColumns+` FROM database_migration_lock_recovery_operations WHERE id=$1`, oldID))
		if scanErr != nil {
			return empty, scanErr
		}
		d, databaseErr := s.DatabaseInternal(ctx, op.DatabaseID)
		if databaseErr != nil || !p.AllowsDatabase(d.Project, d.Environment, true) || !p.Allows("deployments:write", d.Project, d.Environment, op.Plan.ApplicationName) {
			return empty, ErrForbidden
		}
		return op, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	d, err := scanDatabase(tx.QueryRow(ctx, `SELECT `+databaseCols+` FROM managed_databases WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, databaseID))
	if err != nil {
		return empty, err
	}
	var plan database.MigrationLockRecoveryPlan
	err = tx.QueryRow(ctx, `SELECT payload FROM managed_database_reviews WHERE id=$1 AND database_id=$2 AND identity_id=$3 AND revision=$4 AND kind='migration-lock-recovery' AND consumed_at IS NULL AND expires_at>now() FOR UPDATE`, reviewID, databaseID, p.ID, d.Revision).Scan(&plan)
	if err != nil {
		return empty, ErrConflict
	}
	var appRevision int64
	if err = tx.QueryRow(ctx, `SELECT revision FROM applications WHERE id=$1 FOR UPDATE`, plan.Evidence.ApplicationID).Scan(&appRevision); err != nil {
		return empty, err
	}
	if confirmApplication != plan.ApplicationName || confirmDatabase != plan.DatabaseName || appRevision != plan.Evidence.ApplicationRevision || plan.EvidenceSHA256 != plan.Evidence.Digest() || !plan.Evidence.Repairable() || !p.AllowsDatabase(d.Project, d.Environment, true) || !p.Allows("deployments:write", d.Project, d.Environment, plan.ApplicationName) {
		return empty, ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE managed_database_reviews SET consumed_at=now() WHERE id=$1`, reviewID); err != nil {
		return empty, err
	}
	op, err := scanDatabaseMigrationRecovery(tx.QueryRow(ctx, `INSERT INTO database_migration_lock_recovery_operations(id,database_id,application_id,database_revision,application_revision,identity_id,key_id,idempotency_key,request_hash,plan,before_evidence) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+databaseMigrationRecoveryColumns, NewID(), d.ID, plan.Evidence.ApplicationID, d.Revision, appRevision, p.ID, p.KeyID, idem, hash[:], JSON(plan), JSON(plan.Evidence)))
	if err != nil {
		return empty, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'database.migration-lock.recovery.accept',$3,$4)`, p.ID, p.KeyID, d.ID, JSON(map[string]any{"operation_id": op.ID, "review_id": reviewID, "profile": plan.Evidence.Profile, "evidence_sha256": plan.EvidenceSHA256})); err != nil {
		return empty, err
	}
	return op, tx.Commit(ctx)
}

func (s *Store) ClaimMigrationLockRecovery(ctx context.Context) (database.MigrationLockRecoveryOperation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return database.MigrationLockRecoveryOperation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(793044293)`); err != nil {
		return database.MigrationLockRecoveryOperation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE database_migration_lock_recovery_operations SET status='queued',lease='',lease_until=NULL WHERE status='running' AND lease_until<now()`); err != nil {
		return database.MigrationLockRecoveryOperation{}, err
	}
	op, err := scanDatabaseMigrationRecovery(tx.QueryRow(ctx, `UPDATE database_migration_lock_recovery_operations SET status='running',lease=$1,lease_until=now()+interval '45 seconds',started_at=COALESCE(started_at,now()) WHERE id=(SELECT id FROM database_migration_lock_recovery_operations WHERE status='queued' AND next_attempt_at<=now() ORDER BY next_attempt_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING `+databaseMigrationRecoveryColumns, NewID()))
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

func (s *Store) CheckMigrationLockRecovery(ctx context.Context, op database.MigrationLockRecoveryOperation) error {
	var ok bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM database_migration_lock_recovery_operations o JOIN managed_databases d ON d.id=o.database_id JOIN applications a ON a.id=o.application_id WHERE o.id=$1 AND o.lease=$2 AND o.lease_until>now() AND o.status='running' AND d.revision=o.database_revision AND a.revision=o.application_revision)`, op.ID, op.Lease).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrConflict
	}
	d, err := s.DatabaseInternal(ctx, op.DatabaseID)
	if err != nil {
		return err
	}
	return s.Reauthorize(ctx, op.KeyID, d.Project, d.Environment, op.Plan.ApplicationName)
}

func (s *Store) RecordMigrationLockRecovery(ctx context.Context, op database.MigrationLockRecoveryOperation, after *database.MigrationLockEvidence, status, phase, message string) error {
	if status != "queued" && status != "succeeded" && status != "failed" && status != "cancelled" || len(phase) > 64 || len(message) > 512 {
		return ErrInput
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE database_migration_lock_recovery_operations SET status=$3,phase=$4,message=$5,after_evidence=$6,next_attempt_at=now()+interval '3 seconds',lease='',lease_until=NULL,finished_at=CASE WHEN $3='queued' THEN NULL ELSE now() END WHERE id=$1 AND lease=$2 AND status='running' AND lease_until>now()`, op.ID, op.Lease, status, phase, message, JSON(after))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'database.migration-lock.recovery.outcome',$3,$4)`, op.IdentityID, op.KeyID, op.DatabaseID, JSON(map[string]any{"operation_id": op.ID, "status": status, "phase": phase, "evidence_sha256": func() string {
		if after == nil {
			return ""
		}
		return after.Digest()
	}()}))
	return err
}
