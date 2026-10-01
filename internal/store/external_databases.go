package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/externaldatabase"
	"github.com/jackc/pgx/v5"
)

const externalDatabaseCols = `id,project,environment,revision,credential_revision,spec,status,observation,created_at,updated_at,deleted_at,credentials,credential_digest`
const externalDatabaseOperationCols = `id,database_id,revision,kind,status,message,spec,created_at,finished_at,lease`

func scanExternalDatabase(row scanner) (externaldatabase.Resource, error) {
	var r externaldatabase.Resource
	err := row.Scan(&r.ID, &r.Project, &r.Environment, &r.Revision, &r.CredentialRevision, &r.Spec, &r.Status, &r.Observation, &r.CreatedAt, &r.UpdatedAt, &r.DeletedAt, &r.EncryptedCredentials, &r.CredentialDigest)
	return r, err
}
func scanExternalDatabaseOperation(row scanner) (externaldatabase.Operation, error) {
	var o externaldatabase.Operation
	err := row.Scan(&o.ID, &o.DatabaseID, &o.Revision, &o.Kind, &o.Status, &o.Message, &o.Spec, &o.CreatedAt, &o.FinishedAt, &o.Lease)
	return o, err
}
func (s *Store) ExternalDatabases(ctx context.Context, p Principal, project, environment string) ([]externaldatabase.Resource, error) {
	if !p.AllowsDatabase(project, environment, false) {
		return nil, ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, "SELECT "+externalDatabaseCols+" FROM external_databases WHERE project=$1 AND environment=$2 AND deleted_at IS NULL ORDER BY name LIMIT $3", project, environment, externaldatabase.MaxDatabases)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []externaldatabase.Resource{}
	for rows.Next() {
		r, err := scanExternalDatabase(rows)
		if err != nil {
			return nil, err
		}
		r.EncryptedCredentials = nil
		r.CredentialDigest = nil
		items = append(items, r)
	}
	return items, rows.Err()
}
func (s *Store) ExternalDatabaseInternal(ctx context.Context, id string) (externaldatabase.Resource, error) {
	return scanExternalDatabase(s.Pool.QueryRow(ctx, "SELECT "+externalDatabaseCols+" FROM external_databases WHERE id=$1", id))
}
func (s *Store) ExternalDatabase(ctx context.Context, p Principal, id string, write bool) (externaldatabase.Resource, error) {
	r, err := s.ExternalDatabaseInternal(ctx, id)
	if err != nil {
		return r, err
	}
	if r.DeletedAt != nil || !p.AllowsDatabase(r.Project, r.Environment, write) {
		return externaldatabase.Resource{}, pgx.ErrNoRows
	}
	r.EncryptedCredentials = nil
	r.CredentialDigest = nil
	return r, nil
}

// AcceptExternalDatabase commits the current configuration, immutable operation
// and audit together. Credentials never enter the operation or audit payload.
func (s *Store) AcceptExternalDatabase(ctx context.Context, p Principal, r externaldatabase.Resource, expected int64, idem, kind string, rotate bool) (externaldatabase.Operation, error) {
	if !p.AllowsDatabase(r.Project, r.Environment, true) {
		return externaldatabase.Operation{}, ErrForbidden
	}
	if kind == "create" || kind == "update" && !rotate {
		return externaldatabase.Operation{}, fmt.Errorf("%w: %s", ErrConflict, externaldatabase.ErrNewChangesUnavailable)
	}
	if s.RequireDatabaseAdmission && s.AdmitDatabase == nil {
		return externaldatabase.Operation{}, ErrForbidden
	}
	if r.Spec.Validate() != nil || len(r.ID) != 32 || expected < 0 || len(idem) < 8 || len(idem) > 128 || kind != "delete" && len(r.EncryptedCredentials) < 32 || len(r.CredentialDigest) != 32 || kind != "create" && kind != "update" && kind != "delete" || kind == "create" && expected != 0 || kind != "create" && expected == 0 {
		return externaldatabase.Operation{}, ErrInput
	}
	var requestCredentialDigest []byte
	if rotate {
		requestCredentialDigest = r.CredentialDigest
	}
	hash := sha256.Sum256(JSON(struct {
		ID, Project, Environment, Kind string
		Expected                       int64
		Spec                           externaldatabase.Spec
		CredentialDigest               []byte
		Rotate                         bool
	}{r.ID, r.Project, r.Environment, kind, expected, r.Spec, requestCredentialDigest, rotate}))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return externaldatabase.Operation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,56))", p.ID+":"+idem); err != nil {
		return externaldatabase.Operation{}, err
	}
	var oldID string
	var oldHash []byte
	err = tx.QueryRow(ctx, "SELECT id,request_hash FROM external_database_operations WHERE identity_id=$1 AND idempotency_key=$2", p.ID, idem).Scan(&oldID, &oldHash)
	if err == nil {
		if !bytes.Equal(oldHash, hash[:]) {
			return externaldatabase.Operation{}, ErrConflict
		}
		return scanExternalDatabaseOperation(tx.QueryRow(ctx, "SELECT "+externalDatabaseOperationCols+" FROM external_database_operations WHERE id=$1", oldID))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return externaldatabase.Operation{}, err
	}
	if s.AdmitDatabase != nil {
		if err = s.AdmitDatabase(ctx, tx, p, r.Project, r.Environment, idem); err != nil {
			return externaldatabase.Operation{}, err
		}
	}
	var scope string
	if err = tx.QueryRow(ctx, "SELECT name FROM environments WHERE project=$1 AND name=$2 FOR UPDATE", r.Project, r.Environment).Scan(&scope); err != nil {
		return externaldatabase.Operation{}, err
	}
	old, err := scanExternalDatabase(tx.QueryRow(ctx, "SELECT "+externalDatabaseCols+" FROM external_databases WHERE id=$1 FOR UPDATE", r.ID))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return externaldatabase.Operation{}, err
	}
	if expected == 0 {
		if old.ID != "" || !rotate {
			return externaldatabase.Operation{}, ErrConflict
		}
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM external_databases WHERE project=$1 AND environment=$2 AND deleted_at IS NULL", r.Project, r.Environment).Scan(&count); err != nil {
			return externaldatabase.Operation{}, err
		}
		if count >= externaldatabase.MaxDatabases {
			return externaldatabase.Operation{}, fmt.Errorf("%w: external database limit reached", ErrConflict)
		}
		r.CredentialRevision = 1
	} else {
		if old.ID == "" || old.Revision != expected || old.DeletedAt != nil || old.Project != r.Project || old.Environment != r.Environment {
			return externaldatabase.Operation{}, ErrConflict
		}
		if old.Spec.Name != r.Spec.Name {
			return externaldatabase.Operation{}, fmt.Errorf("%w: the connection name cannot change", ErrInput)
		}
		if kind == "update" && old.Spec != r.Spec {
			return externaldatabase.Operation{}, fmt.Errorf("%w: %s", ErrConflict, externaldatabase.ErrNewChangesUnavailable)
		}
		var busy bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM external_database_operations WHERE database_id=$1 AND status IN ('queued','running'))", r.ID).Scan(&busy); err != nil {
			return externaldatabase.Operation{}, err
		}
		if busy {
			return externaldatabase.Operation{}, fmt.Errorf("%w: another connection operation is in progress", ErrConflict)
		}
		if !rotate && (!old.Spec.SameEndpoint(r.Spec) || !bytes.Equal(old.EncryptedCredentials, r.EncryptedCredentials) || !bytes.Equal(old.CredentialDigest, r.CredentialDigest)) {
			return externaldatabase.Operation{}, fmt.Errorf("%w: supply credentials explicitly when changing the database endpoint", ErrInput)
		}
		r.CredentialRevision = old.CredentialRevision
		if rotate {
			r.CredentialRevision++
		}
		if kind == "delete" {
			if old.Spec != r.Spec || rotate {
				return externaldatabase.Operation{}, ErrInput
			}
			if err = externalDatabaseUnreferenced(ctx, tx, r.ID); err != nil {
				return externaldatabase.Operation{}, err
			}
		}
	}
	var duplicate bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM external_databases WHERE project=$1 AND environment=$2 AND name=$3 AND id<>$4 AND deleted_at IS NULL)", r.Project, r.Environment, r.Spec.Name, r.ID).Scan(&duplicate); err != nil {
		return externaldatabase.Operation{}, err
	}
	if duplicate {
		return externaldatabase.Operation{}, fmt.Errorf("%w: this connection name is in use", ErrConflict)
	}
	r.Revision = expected + 1
	r.Status = "pending"
	if kind == "delete" {
		r.Status = "deleting"
	}
	_, err = tx.Exec(ctx, `INSERT INTO external_databases(id,project,environment,name,revision,credential_revision,spec,credentials,credential_digest,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(id) DO UPDATE SET name=excluded.name,revision=excluded.revision,credential_revision=excluded.credential_revision,spec=excluded.spec,credentials=excluded.credentials,credential_digest=excluded.credential_digest,status=excluded.status,observation='{}',observation_lease='',observation_lease_until=NULL,updated_at=now()`, r.ID, r.Project, r.Environment, r.Spec.Name, r.Revision, r.CredentialRevision, JSON(r.Spec), r.EncryptedCredentials, r.CredentialDigest, r.Status)
	if err != nil {
		return externaldatabase.Operation{}, err
	}
	op, err := scanExternalDatabaseOperation(tx.QueryRow(ctx, "INSERT INTO external_database_operations(id,database_id,revision,identity_id,key_id,idempotency_key,request_hash,kind,spec) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING "+externalDatabaseOperationCols, NewID(), r.ID, r.Revision, p.ID, p.KeyID, idem, hash[:], kind, JSON(r.Spec)))
	if err != nil {
		return op, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,$3,$4,$5)", p.ID, p.KeyID, "external_database."+kind, r.ID, JSON(map[string]any{"revision": r.Revision, "credential_revision": r.CredentialRevision, "credentials_rotated": rotate, "operation_id": op.ID}))
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

func (s *Store) ExternalDatabaseOperation(ctx context.Context, p Principal, id string) (externaldatabase.Operation, error) {
	op, err := scanExternalDatabaseOperation(s.Pool.QueryRow(ctx, "SELECT "+externalDatabaseOperationCols+" FROM external_database_operations WHERE id=$1", id))
	if err != nil {
		return op, err
	}
	r, err := s.ExternalDatabaseInternal(ctx, op.DatabaseID)
	if err != nil {
		return op, err
	}
	if !p.AllowsDatabase(r.Project, r.Environment, false) {
		return externaldatabase.Operation{}, pgx.ErrNoRows
	}
	return op, nil
}

func (s *Store) ClaimExternalDatabaseOperation(ctx context.Context) (externaldatabase.Operation, error) {
	return scanExternalDatabaseOperation(s.Pool.QueryRow(ctx, `UPDATE external_database_operations SET status='running',lease=$1,lease_until=clock_timestamp()+interval '30 seconds' WHERE id=(SELECT id FROM external_database_operations WHERE status='queued' OR status='running' AND lease_until<clock_timestamp() ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING `+externalDatabaseOperationCols, NewID()))
}

func (s *Store) CompleteExternalDatabaseOperation(ctx context.Context, op externaldatabase.Operation, o externaldatabase.Observation) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var revision int64
	if err = tx.QueryRow(ctx, "SELECT revision FROM external_databases WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", op.DatabaseID).Scan(&revision); err != nil {
		return err
	}
	if revision != op.Revision {
		return ErrConflict
	}
	status, message := "succeeded", "Connection removed from Hakopod. The provider database was not changed."
	resourceStatus := "deleted"
	if op.Kind != "delete" {
		if o.Revision != op.Revision || o.ObservedAt.IsZero() {
			return ErrConflict
		}
		resourceStatus, message = o.Status, o.Message
		if o.Status != "ready" || !o.TLSVerified || !o.QueryVerified {
			status = "failed"
			resourceStatus = "unreachable"
		}
	}
	result, err := tx.Exec(ctx, "UPDATE external_database_operations SET status=$4,message=$5,finished_at=now(),lease='',lease_until=NULL WHERE id=$1 AND revision=$2 AND lease=$3 AND status='running' AND lease_until>clock_timestamp()", op.ID, op.Revision, op.Lease, status, message)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, "UPDATE external_databases SET status=$3,observation=$4,updated_at=now(),deleted_at=CASE WHEN $3='deleted' THEN now() ELSE NULL END,credentials=CASE WHEN $3='deleted' THEN ''::bytea ELSE credentials END,observation_next_at=now()+interval '60 seconds' WHERE id=$1 AND revision=$2", op.DatabaseID, op.Revision, resourceStatus, JSON(o))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type ExternalDatabaseObservationClaim struct {
	Resource externaldatabase.Resource
	Lease    string
}

func (s *Store) ClaimExternalDatabaseObservation(ctx context.Context) (ExternalDatabaseObservationClaim, error) {
	lease := NewID()
	r, err := scanExternalDatabase(s.Pool.QueryRow(ctx, `UPDATE external_databases SET observation_lease=$1,observation_lease_until=clock_timestamp()+interval '30 seconds' WHERE id=(SELECT id FROM external_databases d WHERE deleted_at IS NULL AND status IN ('ready','unreachable') AND observation_next_at<=now() AND (observation_lease_until IS NULL OR observation_lease_until<clock_timestamp()) AND NOT EXISTS(SELECT 1 FROM external_database_operations o WHERE o.database_id=d.id AND o.status IN ('queued','running')) ORDER BY observation_next_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING `+externalDatabaseCols, lease))
	return ExternalDatabaseObservationClaim{r, lease}, err
}
func (s *Store) RecordExternalDatabaseObservation(ctx context.Context, claim ExternalDatabaseObservationClaim, o externaldatabase.Observation) error {
	if o.Revision != claim.Resource.Revision || o.ObservedAt.IsZero() || o.Status != "ready" && o.Status != "unreachable" {
		return ErrInput
	}
	if o.Status == "ready" && (!o.TLSVerified || !o.QueryVerified) {
		return ErrInput
	}
	result, err := s.Pool.Exec(ctx, `UPDATE external_databases SET status=$4,observation=$5,observation_next_at=now()+interval '60 seconds',observation_lease='',observation_lease_until=NULL WHERE id=$1 AND revision=$2 AND observation_lease=$3 AND observation_lease_until>clock_timestamp() AND status IN ('ready','unreachable') AND deleted_at IS NULL`, claim.Resource.ID, claim.Resource.Revision, claim.Lease, o.Status, JSON(o))
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func externalDatabaseUnreferenced(ctx context.Context, tx pgx.Tx, id string) error {
	var used bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
	 SELECT 1 FROM applications a,jsonb_each(a.spec->'services') s,jsonb_each(s.value->'bindings') b WHERE b.value->>'external_database'=$1
	 UNION ALL SELECT 1 FROM deployments d,jsonb_array_elements(jsonb_build_array(d.spec,d.resolved_spec,d.recovery_spec)) versions(value),jsonb_each(versions.value->'services') s,jsonb_each(s.value->'bindings') b
	 WHERE (d.status IN ('queued','running') OR d.id=(SELECT id FROM deployments WHERE application_id=d.application_id ORDER BY revision DESC LIMIT 1) OR d.id=(SELECT id FROM deployments WHERE application_id=d.application_id AND status='succeeded' ORDER BY revision DESC LIMIT 1)) AND b.value->>'external_database'=$1
	 UNION ALL SELECT 1 FROM external_database_reviews r JOIN deployments d ON d.application_id=r.payload#>>'{plan,application_id}' AND to_jsonb(d.revision-1)=r.payload#>'{plan,application_revision}'
	 WHERE r.database_id=$1 AND r.kind='disconnect' AND r.consumed_at IS NOT NULL AND d.status IN ('queued','running'))`, id).Scan(&used)
	if err != nil {
		return err
	}
	if used {
		return fmt.Errorf("%w: remove saved connections and successfully redeploy every connected application before deleting this connection", ErrConflict)
	}
	return nil
}

func externalDatabaseFresh(r externaldatabase.Resource) error {
	if !r.Verified(time.Now().UTC()) {
		return fmt.Errorf("%w: wait for a fresh verified external database connection", ErrConflict)
	}
	return nil
}
