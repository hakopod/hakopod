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

const databaseCols = `id,project,environment,revision,spec,status,observation,created_at,updated_at,deleted_at,credentials,recovery`
const databaseOperationCols = `id,database_id,revision,kind,status,phase,message,spec,created_at,started_at,finished_at,identity_id,key_id,lease,review,switchover`

func scanDatabase(row scanner) (database.Resource, error) {
	var d database.Resource
	err := row.Scan(&d.ID, &d.Project, &d.Environment, &d.Revision, &d.Spec, &d.Status, &d.Observation, &d.CreatedAt, &d.UpdatedAt, &d.DeletedAt, &d.EncryptedCredentials, &d.Recovery)
	return d, err
}
func scanDatabaseOperation(row scanner) (database.Operation, error) {
	var o database.Operation
	err := row.Scan(&o.ID, &o.DatabaseID, &o.Revision, &o.Kind, &o.Status, &o.Phase, &o.Message, &o.Spec, &o.CreatedAt, &o.StartedAt, &o.FinishedAt, &o.IdentityID, &o.KeyID, &o.Lease, &o.Review, &o.Switchover)
	return o, err
}
func (p Principal) AllowsDatabase(project, environment string, write bool) bool {
	permission := "deployments:read"
	if write {
		permission = "deployments:write"
	}
	return p.Application == "" && p.Allows(permission, project, environment, "")
}
func (s *Store) Databases(ctx context.Context, p Principal, project, environment string) ([]database.Resource, error) {
	if !p.AllowsDatabase(project, environment, false) {
		return nil, ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, "SELECT "+databaseCols+" FROM managed_databases WHERE project=$1 AND environment=$2 AND deleted_at IS NULL ORDER BY name LIMIT $3", project, environment, database.MaxDatabases)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []database.Resource{}
	for rows.Next() {
		d, err := scanDatabase(rows)
		if err != nil {
			return nil, err
		}
		d.EncryptedCredentials = nil
		items = append(items, d)
	}
	return items, rows.Err()
}
func (s *Store) Database(ctx context.Context, p Principal, id string, write bool) (database.Resource, error) {
	d, err := s.DatabaseInternal(ctx, id)
	if err != nil {
		return d, err
	}
	if !p.AllowsDatabase(d.Project, d.Environment, write) {
		return database.Resource{}, pgx.ErrNoRows
	}
	d.EncryptedCredentials = nil
	return d, nil
}

// DatabaseInternal is only for authenticated credential access and reconciliation.
func (s *Store) DatabaseInternal(ctx context.Context, id string) (database.Resource, error) {
	return scanDatabase(s.Pool.QueryRow(ctx, "SELECT "+databaseCols+" FROM managed_databases WHERE id=$1 AND deleted_at IS NULL", id))
}
func (s *Store) DatabaseOperations(ctx context.Context, p Principal, id string) ([]database.Operation, error) {
	if _, err := s.Database(ctx, p, id, false); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, "SELECT "+databaseOperationCols+" FROM managed_database_operations WHERE database_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []database.Operation{}
	for rows.Next() {
		o, err := scanDatabaseOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DatabaseOperation remains readable after deletion, so callers can distinguish
// completed deletion from a missing or inaccessible resource. Scope is checked
// against the original database identity, including its retained tombstone.
func (s *Store) DatabaseOperation(ctx context.Context, p Principal, id string) (database.Operation, error) {
	op, err := scanDatabaseOperation(s.Pool.QueryRow(ctx, "SELECT "+databaseOperationCols+" FROM managed_database_operations WHERE id=$1", id))
	if err != nil {
		return database.Operation{}, err
	}
	var project, environment string
	if err = s.Pool.QueryRow(ctx, "SELECT project,environment FROM managed_databases WHERE id=$1", op.DatabaseID).Scan(&project, &environment); err != nil {
		return database.Operation{}, err
	}
	if !p.AllowsDatabase(project, environment, false) {
		return database.Operation{}, pgx.ErrNoRows
	}
	return op, nil
}

// AcceptDatabase stores the immutable revision, durable operation and audit in
// one transaction. Resource limits serialize on the environment row.
func (s *Store) AcceptDatabase(ctx context.Context, p Principal, d database.Resource, expected int64, idem, kind string) (database.Operation, error) {
	if kind == "resize" {
		return database.Operation{}, ErrInput
	}
	return s.acceptDatabase(ctx, p, d, expected, idem, kind, "")
}
func (s *Store) AcceptDatabaseResize(ctx context.Context, p Principal, d database.Resource, expected int64, idem, reviewID string) (database.Operation, error) {
	if reviewID == "" {
		return database.Operation{}, ErrInput
	}
	return s.acceptDatabase(ctx, p, d, expected, idem, "resize", reviewID)
}
func (s *Store) acceptDatabase(ctx context.Context, p Principal, d database.Resource, expected int64, idem, kind, reviewID string) (database.Operation, error) {
	return waitForDatabaseMaintenance(ctx, func(attempt context.Context) (database.Operation, error) {
		return s.acceptDatabaseOnce(attempt, p, d, expected, idem, kind, reviewID)
	})
}

func (s *Store) acceptDatabaseOnce(ctx context.Context, p Principal, d database.Resource, expected int64, idem, kind, reviewID string) (database.Operation, error) {
	if expected == 0 && kind == "create" {
		d.Spec = d.Spec.WithSecureDefaults()
	}
	if !p.AllowsDatabase(d.Project, d.Environment, true) {
		return database.Operation{}, ErrForbidden
	}
	if s.RequireDatabaseAdmission && s.AdmitDatabase == nil {
		return database.Operation{}, fmt.Errorf("%w: managed database admission must be configured by Cloud", ErrForbidden)
	}
	if err := d.Spec.Validate(); err != nil {
		return database.Operation{}, fmt.Errorf("%w: %s", ErrInput, err)
	}
	if len(idem) < 8 || len(idem) > 128 || expected < 0 {
		return database.Operation{}, fmt.Errorf("%w: a valid revision and 8–128 character Idempotency-Key are required", ErrInput)
	}
	if kind != "create" && kind != "resize" && kind != "delete" {
		return database.Operation{}, ErrInput
	}
	hash := sha256.Sum256(JSON(struct {
		Project, Environment, ID, Kind, ReviewID string
		Expected                                 int64
		Spec                                     database.Spec
	}{d.Project, d.Environment, d.ID, kind, reviewID, expected, d.Spec}))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return database.Operation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,46))", p.ID+":"+idem); err != nil {
		return database.Operation{}, err
	}
	var oldHash []byte
	var oldID string
	err = tx.QueryRow(ctx, "SELECT id,request_hash FROM managed_database_operations WHERE identity_id=$1 AND idempotency_key=$2", p.ID, idem).Scan(&oldID, &oldHash)
	if err == nil {
		if !bytes.Equal(oldHash, hash[:]) {
			return database.Operation{}, ErrConflict
		}
		return scanDatabaseOperation(tx.QueryRow(ctx, "SELECT "+databaseOperationCols+" FROM managed_database_operations WHERE id=$1", oldID))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return database.Operation{}, err
	}
	if s.AdmitDatabase != nil {
		if err = s.AdmitDatabase(ctx, tx, p, d.Project, d.Environment, idem); err != nil {
			return database.Operation{}, err
		}
	}
	var scope string
	if err = tx.QueryRow(ctx, "SELECT name FROM environments WHERE project=$1 AND name=$2 FOR UPDATE", d.Project, d.Environment).Scan(&scope); err != nil {
		return database.Operation{}, err
	}
	current, err := scanDatabase(tx.QueryRow(ctx, "SELECT "+databaseCols+" FROM managed_databases WHERE project=$1 AND environment=$2 AND name=$3 AND deleted_at IS NULL FOR UPDATE", d.Project, d.Environment, d.Spec.Name))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return database.Operation{}, err
	}
	if current.Revision != expected || current.DeletedAt != nil {
		return database.Operation{}, ErrConflict
	}
	var maintaining bool
	if current.ID != "" {
		if err = tx.QueryRow(ctx, "SELECT COALESCE(maintenance_lease_until>now(),false) FROM managed_databases WHERE id=$1", current.ID).Scan(&maintaining); err != nil {
			return database.Operation{}, err
		}
		var publicEndpointBusy bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM managed_database_public_endpoint_operations WHERE database_id=$1 AND status IN ('queued','running'))", current.ID).Scan(&publicEndpointBusy); err != nil {
			return database.Operation{}, err
		}
		if publicEndpointBusy {
			return database.Operation{}, fmt.Errorf("%w: a database public endpoint operation is in progress", ErrConflict)
		}
	}
	var review *database.ResizePlan
	if kind == "resize" {
		var plan database.ResizePlan
		err = tx.QueryRow(ctx, "SELECT payload FROM managed_database_reviews WHERE id=$1 AND database_id=$2 AND identity_id=$3 AND revision=$4 AND kind='resize' AND consumed_at IS NULL AND expires_at>now() FOR UPDATE", reviewID, d.ID, p.ID, expected).Scan(&plan)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return database.Operation{}, ErrConflict
			}
			return database.Operation{}, err
		}
		if !plan.Current.Equal(current.Spec) || !plan.Proposed.Equal(d.Spec) || plan.ExpectedRevision != expected || len(plan.BlockedReasons) > 0 || !plan.ExpiresAt.After(time.Now()) {
			return database.Operation{}, ErrConflict
		}
		review = &plan
	}
	if expected == 0 {
		if kind != "create" || len(d.EncryptedCredentials) == 0 || len(d.EncryptedCredentials) > 8192 {
			return database.Operation{}, ErrInput
		}
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM managed_databases WHERE project=$1 AND environment=$2 AND deleted_at IS NULL", d.Project, d.Environment).Scan(&count); err != nil {
			return database.Operation{}, err
		}
		if count >= database.MaxDatabases {
			return database.Operation{}, fmt.Errorf("%w: this environment supports at most %d managed databases", ErrInput, database.MaxDatabases)
		}
		if d.ID == "" {
			d.ID = NewID()
		}
		if err = s.validateVitessBackupTx(ctx, tx, p, d); err != nil {
			return database.Operation{}, err
		}
		_, err = tx.Exec(ctx, "INSERT INTO managed_databases(id,project,environment,name,revision,spec,credentials) VALUES($1,$2,$3,$4,1,$5,$6)", d.ID, d.Project, d.Environment, d.Spec.Name, JSON(d.Spec), d.EncryptedCredentials)
	} else {
		var backingUp bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM backup_jobs WHERE status IN ('queued','running') AND (source->>'managed_database_id'=$1 OR target->>'managed_database_id'=$1))", d.ID).Scan(&backingUp); err != nil {
			return database.Operation{}, err
		}
		if backingUp {
			return database.Operation{}, fmt.Errorf("%w: a database backup or recovery is still running", ErrConflict)
		}
		if d.ID != current.ID || kind == "create" || current.Status == "pending" || current.Status == "deleting" || current.Status == "restoring" {
			return database.Operation{}, ErrConflict
		}
		if d.Spec.Engine != current.Spec.Engine || d.Spec.Version != current.Spec.Version || d.Spec.Mode != current.Spec.Mode || !d.Spec.Placement.Equal(current.Spec.Placement) || d.Spec.TLSRequired() != current.Spec.TLSRequired() {
			return database.Operation{}, ErrInput
		}
		if d.Spec.Engine == "oracle" && !d.Spec.Equal(current.Spec) {
			return database.Operation{}, ErrInput
		}
		if database.PublicEndpointRequiresMembers(current.Spec) && !d.Spec.Equal(current.Spec) {
			if err = ensureNoDatabasePublicEndpoints(ctx, tx, d.ID); err != nil {
				return database.Operation{}, err
			}
		}
		status := "pending"
		if kind == "delete" {
			if err = databaseUnreferenced(ctx, tx, d.ID); err != nil {
				return database.Operation{}, err
			}
			if err = ensureNoDatabasePublicEndpoints(ctx, tx, d.ID); err != nil {
				return database.Operation{}, err
			}
			status = "deleting"
		}
		if maintaining {
			return database.Operation{}, fmt.Errorf("%w: %w", ErrConflict, errDatabaseMaintenanceActive)
		}
		if review != nil {
			if _, err = tx.Exec(ctx, "UPDATE managed_database_reviews SET consumed_at=now() WHERE id=$1", reviewID); err != nil {
				return database.Operation{}, err
			}
		}
		_, err = tx.Exec(ctx, "UPDATE managed_databases SET revision=revision+1,spec=$2,status=$3,updated_at=now() WHERE id=$1", d.ID, JSON(d.Spec), status)
	}
	if err != nil {
		return database.Operation{}, err
	}
	if err = s.reserveDatabaseAllocation(ctx, tx, d, kind); err != nil {
		return database.Operation{}, err
	}
	o, err := scanDatabaseOperation(tx.QueryRow(ctx, "INSERT INTO managed_database_operations(id,database_id,revision,identity_id,key_id,idempotency_key,request_hash,kind,spec,review) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING "+databaseOperationCols, NewID(), d.ID, expected+1, p.ID, p.KeyID, idem, hash[:], kind, JSON(d.Spec), review))
	if err != nil {
		return o, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,$3,$4,$5)", p.ID, p.KeyID, "database."+kind, d.ID, JSON(map[string]any{"revision": expected + 1, "operation_id": o.ID})); err != nil {
		return o, err
	}
	return o, tx.Commit(ctx)
}

func (s *Store) ClaimDatabaseOperation(ctx context.Context) (database.Operation, error) {
	// One short lease for a reconciliation step; no wait for rollout while holding
	// the lane. Every runtime write repeats authorization and lease validation.
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return database.Operation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044246)"); err != nil {
		return database.Operation{}, err
	}
	_, err = tx.Exec(ctx, "UPDATE managed_database_operations SET status='queued',lease='',lease_until=NULL WHERE status='running' AND lease_until<now()")
	if err != nil {
		return database.Operation{}, err
	}
	var busy bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM managed_database_operations WHERE status='running')").Scan(&busy); err != nil {
		return database.Operation{}, err
	}
	if busy {
		return database.Operation{}, pgx.ErrNoRows
	}
	o, err := scanDatabaseOperation(tx.QueryRow(ctx, "UPDATE managed_database_operations SET status='running',lease=$1,lease_until=now()+interval '30 seconds',started_at=COALESCE(started_at,now()) WHERE id=(SELECT id FROM managed_database_operations WHERE status='queued' AND next_attempt_at<=now() ORDER BY next_attempt_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING "+databaseOperationCols, NewID()))
	if err != nil {
		return o, err
	}
	return o, tx.Commit(ctx)
}
func (s *Store) CheckDatabaseOperation(ctx context.Context, o database.Operation) error {
	var valid bool
	if err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM managed_database_operations o JOIN managed_databases d ON d.id=o.database_id WHERE o.id=$1 AND o.lease=$2 AND o.lease_until>now() AND o.status='running' AND d.revision=o.revision)", o.ID, o.Lease).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	d, err := s.DatabaseInternal(ctx, o.DatabaseID)
	if err != nil {
		return err
	}
	return s.Reauthorize(ctx, o.KeyID, d.Project, d.Environment, "")
}
func (s *Store) RecordDatabaseStep(ctx context.Context, o database.Operation, observation database.Observation, status, phase, message string) error {
	if status != "queued" && status != "succeeded" && status != "failed" && status != "cancelled" {
		return ErrInput
	}
	if len(message) > 512 || len(phase) > 64 {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, "UPDATE managed_database_operations SET status=$3,phase=$4,message=$5,next_attempt_at=now()+interval '3 seconds',lease='',lease_until=NULL,finished_at=CASE WHEN $3='queued' THEN NULL ELSE now() END WHERE id=$1 AND lease=$2 AND status='running' AND lease_until>now()", o.ID, o.Lease, status, phase, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	state := "pending"
	if o.Kind == "delete" {
		state = "deleting"
	}
	if status == "succeeded" {
		state = "ready"
		if o.Kind == "delete" {
			state = "deleted"
		}
	}
	if status == "failed" || status == "cancelled" {
		state = "failed"
	}
	tag, err = tx.Exec(ctx, "UPDATE managed_databases SET status=$3,observation=$4,updated_at=now(),deleted_at=CASE WHEN $3='deleted' THEN now() ELSE deleted_at END WHERE id=$1 AND revision=$2", o.DatabaseID, o.Revision, state, JSON(observation))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if status == "succeeded" && o.Kind != "delete" {
		var desired database.Spec
		if err = tx.QueryRow(ctx, "SELECT spec FROM managed_databases WHERE id=$1", o.DatabaseID).Scan(&desired); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE managed_databases SET reserved_memory_bytes=$2 WHERE id=$1", o.DatabaseID, DatabaseMemoryReservation(desired)); err != nil {
			return err
		}
		// Redis retains removed shard PVCs. Keep storage charged until namespace
		// deletion verifies reclamation; a successful resize alone is not evidence.
	}
	if err = recordDatabaseMetricPoint(ctx, tx, o.DatabaseID, o.Revision, observation); err != nil {
		return err
	}
	if err = recordDatabaseFailures(ctx, tx, o.DatabaseID, o.Revision, observation.ObservedAt, observation.Failures); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) ObserveDatabase(ctx context.Context, id string, revision int64, o database.Observation) error {
	return s.observeDatabase(ctx, id, revision, "", o)
}

// ObserveRecoveringDatabase records the ready topology produced by the active
// restore worker before that worker can mark its backup job successful.
func (s *Store) ObserveRecoveringDatabase(ctx context.Context, id string, revision int64, jobID string, o database.Observation) error {
	if jobID == "" || o.Status != "ready" || !o.Fresh(time.Now(), revision) {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE managed_databases d SET observation=$4,updated_at=now()
		FROM backup_jobs j WHERE d.id=$1 AND d.revision=$2 AND d.deleted_at IS NULL
		AND d.status='restoring' AND d.recovery->>'job_id'=$3
		AND j.id=$3 AND j.kind='restore' AND j.status='running' AND NOT j.cancel_requested
		AND j.lease<>'' AND j.lease_until>clock_timestamp()
		AND j.target->>'managed_database_id'=$1 AND (j.target->>'revision')::bigint=$2
		AND j.target->>'engine'=d.spec->>'engine'
		AND COALESCE((d.observation->>'observed_at')::timestamptz,'epoch')<=$5`, id, revision, jobID, JSON(o), o.ObservedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if err = recordDatabaseMetricPoint(ctx, tx, id, revision, o); err != nil {
		return err
	}
	if err = recordDatabaseFailures(ctx, tx, id, revision, o.ObservedAt, o.Failures); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) observeDatabase(ctx context.Context, id string, revision int64, lease string, o database.Observation) error {
	if o.Revision != revision || o.ObservedAt.IsZero() {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, "UPDATE managed_databases SET observation=$3 WHERE id=$1 AND revision=$2 AND deleted_at IS NULL AND COALESCE((observation->>'observed_at')::timestamptz,'epoch')<=$4 AND ($5='' OR ((status='ready' OR (status='failed' AND spec->>'engine'='vitess')) AND observation_lease=$5 AND observation_lease_until>clock_timestamp()))", id, revision, JSON(o), o.ObservedAt, lease)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if lease != "" {
			return ErrClaimLost
		}
		return nil
	}
	if err = recordDatabaseMetricPoint(ctx, tx, id, revision, o); err != nil {
		return err
	}
	if err = recordDatabaseFailures(ctx, tx, id, revision, o.ObservedAt, o.Failures); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) DatabaseBackupEvidence(ctx context.Context, id string, revision int64) (*database.BackupEvidence, error) {
	var e database.BackupEvidence
	err := s.Pool.QueryRow(ctx, `SELECT v.artifact_id,v.database_id,v.revision,v.captured_at,v.verified_at,v.sha256 FROM managed_database_backup_verifications v JOIN backup_artifacts a ON a.id=v.artifact_id WHERE v.database_id=$1 AND v.revision=$2 AND a.deleted_at IS NULL AND NOT a.deletion_pending ORDER BY v.captured_at DESC LIMIT 1`, id, revision).Scan(&e.ArtifactID, &e.DatabaseID, &e.Revision, &e.CapturedAt, &e.VerifiedAt, &e.SHA256)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &e, err
}
func (s *Store) SaveDatabaseReview(ctx context.Context, p Principal, d database.Resource, kind string, payload any, expires time.Time) (string, error) {
	if !p.AllowsDatabase(d.Project, d.Environment, true) {
		return "", ErrForbidden
	}
	if kind != "resize" && kind != "resize-retry" && kind != "switchover" || expires.After(time.Now().Add(database.ReviewLifetime+time.Second)) || !expires.After(time.Now()) {
		return "", ErrInput
	}
	if kind == "resize-retry" {
		plan, ok := payload.(database.ResizeRetryReview)
		if !ok || !validDatabaseResizeRetryReview(d, plan) || !plan.ExpiresAt.Equal(expires) {
			return "", ErrInput
		}
	}
	if kind == "switchover" {
		plan, ok := payload.(database.OracleSwitchoverReview)
		if !ok || !validOracleSwitchoverReview(d, plan) || !plan.ExpiresAt.Equal(expires) {
			return "", ErrInput
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var revision int64
	if err = tx.QueryRow(ctx, "SELECT revision FROM managed_databases WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", d.ID).Scan(&revision); err != nil {
		return "", err
	}
	if revision != d.Revision {
		return "", ErrConflict
	}
	if _, err = tx.Exec(ctx, "DELETE FROM managed_database_reviews WHERE database_id=$1 AND (expires_at<=now() OR consumed_at IS NOT NULL)", d.ID); err != nil {
		return "", err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM managed_database_reviews WHERE database_id=$1", d.ID).Scan(&count); err != nil {
		return "", err
	}
	if count >= 20 {
		return "", fmt.Errorf("%w: wait for an existing database review to expire", ErrConflict)
	}
	id := NewID()
	_, err = tx.Exec(ctx, "INSERT INTO managed_database_reviews(id,database_id,identity_id,revision,kind,payload,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)", id, d.ID, p.ID, d.Revision, kind, JSON(payload), expires)
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) AuditDatabaseCredentials(ctx context.Context, p Principal, d database.Resource) error {
	if !p.AllowsDatabase(d.Project, d.Environment, true) {
		return ErrForbidden
	}
	_, err := s.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'database.credentials.read',$3)", p.ID, p.KeyID, d.ID)
	return err
}

func (s *Store) BackupManagedDatabases(ctx context.Context) ([]database.Resource, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+databaseCols+" FROM managed_databases WHERE deleted_at IS NULL AND ($1 OR (project=$2 AND environment=$3)) ORDER BY id LIMIT 64", backupUnscoped(ctx), backupProject(ctx), backupEnvironment(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []database.Resource{}
	for rows.Next() {
		d, err := scanDatabase(rows)
		if err != nil {
			return nil, err
		}
		d.EncryptedCredentials = nil
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) RefreshDatabaseRecoveries(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `WITH finished AS (SELECT d.id,j.status,j.finished_at FROM managed_databases d JOIN backup_jobs j ON j.id=d.recovery->>'job_id' WHERE d.status='restoring' AND j.status IN ('succeeded','failed','cancelled') ORDER BY d.id LIMIT 64) UPDATE managed_databases d SET status=CASE WHEN f.status='succeeded' THEN 'ready' ELSE 'failed' END,recovery=CASE WHEN f.status='succeeded' THEN jsonb_set(d.recovery,'{restored_at}',to_jsonb(f.finished_at)) ELSE d.recovery END,updated_at=now() FROM finished f WHERE d.id=f.id`)
	return err
}
