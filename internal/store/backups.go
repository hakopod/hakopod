package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/jackc/pgx/v5"
)

func backupError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return backup.ErrNotFound
	}
	return err
}
func backupAdmin(p Principal) error {
	if !p.IsAdmin() {
		return ErrForbidden
	}
	return nil
}
func backupAudit(ctx context.Context, tx pgx.Tx, p Principal, action, id string) error {
	_, err := tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,$3,$4)", p.ID, p.KeyID, action, id)
	return err
}
func scanBackupDestination(row scanner) (backup.Destination, error) {
	var d backup.Destination
	var data []byte
	err := row.Scan(&d.ID, &d.Name, &d.Revision, &data, &d.EncryptedCredentials, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return d, backupError(err)
	}
	var config backup.Destination
	if err = json.Unmarshal(data, &config); err != nil {
		return d, err
	}
	config.ID = d.ID
	config.Name = d.Name
	config.Revision = d.Revision
	config.EncryptedCredentials = d.EncryptedCredentials
	config.CreatedAt = d.CreatedAt
	config.UpdatedAt = d.UpdatedAt
	return config, nil
}

const backupDestinationCols = "id,name,revision,config,credentials,created_at,updated_at"

func (s *Store) BackupDestination(ctx context.Context, id string) (backup.Destination, error) {
	return scanBackupDestination(s.Pool.QueryRow(ctx, "SELECT "+backupDestinationCols+" FROM backup_destinations WHERE id=$1", id))
}
func (s *Store) BackupDestinations(ctx context.Context) ([]backup.Destination, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+backupDestinationCols+" FROM backup_destinations ORDER BY created_at,id LIMIT 32")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []backup.Destination{}
	for rows.Next() {
		d, e := scanBackupDestination(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) BackupApplications(ctx context.Context) ([]Application, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+appCols+" FROM applications ORDER BY id LIMIT 128")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		a, e := scanApp(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) PutBackupDestination(ctx context.Context, p Principal, d backup.Destination, expected int64) (backup.Destination, error) {
	if err := backupAdmin(p); err != nil {
		return d, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return d, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044230)"); err != nil {
		return d, err
	}
	var revision int64
	err = tx.QueryRow(ctx, "SELECT revision FROM backup_destinations WHERE id=$1 FOR UPDATE", d.ID).Scan(&revision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return d, err
	}
	if revision != expected {
		return d, backup.ErrConflict
	}
	if expected == 0 {
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM backup_destinations").Scan(&count); err != nil {
			return d, err
		}
		if count >= 32 {
			return d, fmt.Errorf("%w: at most 32 destinations are supported", backup.ErrInput)
		}
	}
	d.Revision = expected + 1
	result, err := scanBackupDestination(tx.QueryRow(ctx, "INSERT INTO backup_destinations(id,name,revision,config,credentials) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET name=excluded.name,revision=excluded.revision,config=excluded.config,credentials=excluded.credentials,updated_at=now() RETURNING "+backupDestinationCols, d.ID, d.Name, d.Revision, JSON(d), d.EncryptedCredentials))
	if err != nil {
		return d, err
	}
	if err = backupAudit(ctx, tx, p, "backup.destination.configured", d.ID); err != nil {
		return d, err
	}
	return result, tx.Commit(ctx)
}
func (s *Store) DeleteBackupDestination(ctx context.Context, p Principal, id string, revision int64) error {
	if err := backupAdmin(p); err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044230)"); err != nil {
		return err
	}
	var used bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM backup_artifacts WHERE destination_id=$1 AND deleted_at IS NULL) OR EXISTS(SELECT 1 FROM backup_jobs WHERE destination_id=$1 AND status IN ('queued','running')) OR EXISTS(SELECT 1 FROM backup_schedules WHERE destination_id=$1)", id).Scan(&used); err != nil {
		return err
	}
	if used {
		return fmt.Errorf("%w: destination still has stored artifacts, schedules or active jobs", backup.ErrConflict)
	}
	r, err := tx.Exec(ctx, "DELETE FROM backup_destinations WHERE id=$1 AND revision=$2", id, revision)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return backup.ErrConflict
	}
	if err = backupAudit(ctx, tx, p, "backup.destination.deleted", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const backupJobCols = "id,kind,status,destination_id,source,target,artifact_id,schedule_id,identity_id,key_id,error,bytes,cancel_requested,created_at,started_at,finished_at,lease"

func scanBackupJob(row scanner) (backup.Job, error) {
	var j backup.Job
	var source, target []byte
	err := row.Scan(&j.ID, &j.Kind, &j.Status, &j.DestinationID, &source, &target, &j.ArtifactID, &j.ScheduleID, &j.IdentityID, &j.KeyID, &j.Error, &j.Bytes, &j.CancelRequested, &j.CreatedAt, &j.StartedAt, &j.FinishedAt, &j.Lease)
	if err != nil {
		return j, backupError(err)
	}
	if err = json.Unmarshal(source, &j.Source); err != nil {
		return j, err
	}
	if len(target) > 0 {
		err = json.Unmarshal(target, &j.Target)
	}
	return j, err
}
func (s *Store) BackupJob(ctx context.Context, id string) (backup.Job, error) {
	return scanBackupJob(s.Pool.QueryRow(ctx, "SELECT "+backupJobCols+" FROM backup_jobs WHERE id=$1", id))
}
func (s *Store) BackupJobs(ctx context.Context, cursor, destination string) ([]backup.Job, string, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+backupJobCols+" FROM backup_jobs WHERE ($1='' OR (created_at,id)<(SELECT created_at,id FROM backup_jobs WHERE id=$1)) AND ($2='' OR destination_id=$2) ORDER BY created_at DESC,id DESC LIMIT 101", cursor, destination)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []backup.Job{}
	for rows.Next() {
		j, e := scanBackupJob(rows)
		if e != nil {
			return nil, "", e
		}
		out = append(out, j)
	}
	next := ""
	if len(out) > 100 {
		out = out[:100]
		next = out[99].ID
	}
	return out, next, rows.Err()
}
func backupRequestHash(j backup.Job) string {
	sum := sha256.Sum256(JSON(struct {
		Kind, Destination, Artifact string
		Source                      backup.Source
		Target                      *backup.Target
	}{j.Kind, j.DestinationID, j.ArtifactID, j.Source, j.Target}))
	return hex.EncodeToString(sum[:])
}
func enqueueBackup(ctx context.Context, tx pgx.Tx, p Principal, j backup.Job, idem string) (backup.Job, error) {
	if err := rejectPreviewBackup(ctx, tx, j.Source.ApplicationID); err != nil {
		return j, err
	}
	if j.Target != nil {
		if err := rejectPreviewBackup(ctx, tx, j.Target.ApplicationID); err != nil {
			return j, err
		}
	}
	var oldHash string
	var existing string
	err := tx.QueryRow(ctx, "SELECT id,request_hash FROM backup_jobs WHERE identity_id=$1 AND idempotency_key=$2", p.ID, idem).Scan(&existing, &oldHash)
	hash := backupRequestHash(j)
	if err == nil {
		if hash != oldHash {
			return j, backup.ErrConflict
		}
		return scanBackupJob(tx.QueryRow(ctx, "SELECT "+backupJobCols+" FROM backup_jobs WHERE id=$1", existing))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return j, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM backup_jobs WHERE status IN ('queued','running')").Scan(&count); err != nil {
		return j, err
	}
	if count >= 64 {
		return j, fmt.Errorf("%w: backup queue is full (64 jobs)", backup.ErrConflict)
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM backup_destinations WHERE id=$1)", j.DestinationID).Scan(&exists); err != nil {
		return j, err
	}
	if !exists {
		return j, backup.ErrNotFound
	}
	if j.ID == "" {
		j.ID = NewID()
	}
	var target any
	if j.Target != nil {
		target = JSON(j.Target)
	}
	result, err := scanBackupJob(tx.QueryRow(ctx, "INSERT INTO backup_jobs(id,kind,status,destination_id,source,target,artifact_id,schedule_id,identity_id,key_id,idempotency_key,request_hash) VALUES($1,$2,'queued',$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING "+backupJobCols, j.ID, j.Kind, j.DestinationID, JSON(j.Source), target, j.ArtifactID, j.ScheduleID, p.ID, p.KeyID, idem, hash))
	if err != nil {
		return j, err
	}
	return result, backupAudit(ctx, tx, p, "backup."+j.Kind+".queued", j.ID)
}
func (s *Store) EnqueueBackup(ctx context.Context, p Principal, j backup.Job, idem string) (backup.Job, error) {
	if err := backupAdmin(p); err != nil {
		return j, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return j, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044230)"); err != nil {
		return j, err
	}
	result, err := enqueueBackup(ctx, tx, p, j, idem)
	if err != nil {
		return j, err
	}
	return result, tx.Commit(ctx)
}
func (s *Store) CancelBackupJob(ctx context.Context, p Principal, id string) (backup.Job, error) {
	if err := backupAdmin(p); err != nil {
		return backup.Job{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return backup.Job{}, err
	}
	defer tx.Rollback(ctx)
	j, err := scanBackupJob(tx.QueryRow(ctx, "UPDATE backup_jobs SET cancel_requested=true,status=CASE WHEN status='queued' THEN 'cancelled' ELSE status END,finished_at=CASE WHEN status='queued' THEN now() ELSE finished_at END WHERE id=$1 RETURNING "+backupJobCols, id))
	if err != nil {
		return j, err
	}
	if err = backupAudit(ctx, tx, p, "backup.cancel.requested", id); err != nil {
		return j, err
	}
	return j, tx.Commit(ctx)
}

const backupArtifactCols = "id,job_id,destination_id,source,object_key,sha256,bytes,format,scope,schedule_id,created_at,deleted_at,deletion_pending"

func scanBackupArtifact(row scanner) (backup.Artifact, error) {
	var a backup.Artifact
	var source []byte
	err := row.Scan(&a.ID, &a.JobID, &a.DestinationID, &source, &a.ObjectKey, &a.SHA256, &a.Bytes, &a.Format, &a.Scope, &a.ScheduleID, &a.CreatedAt, &a.DeletedAt, &a.DeletionPending)
	if err != nil {
		return a, backupError(err)
	}
	return a, json.Unmarshal(source, &a.Source)
}
func (s *Store) BackupArtifact(ctx context.Context, id string) (backup.Artifact, error) {
	return scanBackupArtifact(s.Pool.QueryRow(ctx, "SELECT "+backupArtifactCols+" FROM backup_artifacts WHERE id=$1 AND deleted_at IS NULL", id))
}
func (s *Store) BackupArtifacts(ctx context.Context, cursor, destination string) ([]backup.Artifact, string, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+backupArtifactCols+" FROM backup_artifacts WHERE deleted_at IS NULL AND ($1='' OR (created_at,id)<(SELECT created_at,id FROM backup_artifacts WHERE id=$1)) AND ($2='' OR destination_id=$2) ORDER BY created_at DESC,id DESC LIMIT 101", cursor, destination)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []backup.Artifact{}
	for rows.Next() {
		a, e := scanBackupArtifact(rows)
		if e != nil {
			return nil, "", e
		}
		out = append(out, a)
	}
	next := ""
	if len(out) > 100 {
		out = out[:100]
		next = out[99].ID
	}
	return out, next, rows.Err()
}

// Accepted reviews are immutable retry receipts for the lifetime of their job.
// Only unused reviews expire; deleting a retained job cascades to its receipt.
const pruneUnusedBackupRestorePlans = "DELETE FROM backup_restore_plans WHERE id IN (SELECT id FROM backup_restore_plans WHERE used_at IS NULL AND expires_at<now() ORDER BY expires_at LIMIT 100)"

func (s *Store) SaveBackupRestorePlan(ctx context.Context, p Principal, plan backup.RestorePlan) error {
	if err := backupAdmin(p); err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044230)"); err != nil {
		return err
	}
	var available bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM backup_artifacts WHERE id=$1 AND deleted_at IS NULL AND NOT deletion_pending)", plan.ArtifactID).Scan(&available); err != nil {
		return err
	}
	if !available {
		return backup.ErrConflict
	}
	if _, err = tx.Exec(ctx, pruneUnusedBackupRestorePlans); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM backup_restore_plans WHERE identity_id=$1 AND used_at IS NULL AND expires_at>now()", p.ID).Scan(&count); err != nil {
		return err
	}
	if count >= 32 {
		return fmt.Errorf("%w: at most 32 pending restore reviews per administrator", backup.ErrConflict)
	}
	_, err = tx.Exec(ctx, "INSERT INTO backup_restore_plans(id,identity_id,artifact_id,plan,expires_at) VALUES($1,$2,$3,$4,$5)", plan.ID, p.ID, plan.ArtifactID, JSON(plan), plan.ExpiresAt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) AcceptBackupRestore(ctx context.Context, p Principal, artifactID, planID, confirmation, idem string) (backup.Job, error) {
	var j backup.Job
	if err := backupAdmin(p); err != nil {
		return j, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return j, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044230)"); err != nil {
		return j, err
	}
	var data []byte
	var expiry time.Time
	var used *time.Time
	var acceptedJob *string
	err = tx.QueryRow(ctx, "SELECT plan,expires_at,used_at,job_id FROM backup_restore_plans WHERE id=$1 AND identity_id=$2 AND artifact_id=$3 FOR UPDATE", planID, p.ID, artifactID).Scan(&data, &expiry, &used, &acceptedJob)
	if err != nil {
		return j, backupError(err)
	}
	var plan backup.RestorePlan
	if err = json.Unmarshal(data, &plan); err != nil {
		return j, err
	}
	if plan.ID != planID || plan.ArtifactID != artifactID || plan.Confirmation != plan.Target.Database {
		return j, backup.ErrConflict
	}
	if confirmation != plan.Confirmation {
		return j, fmt.Errorf("%w: type the exact new database name from the restore review", backup.ErrInput)
	}
	// Resolve accepted retries before expiry or artifact availability checks. The
	// receipt binds this exact review, actor, artifact and confirmation to one job;
	// a different idempotency key can never execute an expired or used review.
	if used != nil && acceptedJob != nil {
		var storedHash string
		if err = tx.QueryRow(ctx, "SELECT request_hash FROM backup_jobs WHERE id=$1 AND identity_id=$2 AND idempotency_key=$3", *acceptedJob, p.ID, idem).Scan(&storedHash); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return j, backup.ErrConflict
			}
			return j, err
		}
		j, err = scanBackupJob(tx.QueryRow(ctx, "SELECT "+backupJobCols+" FROM backup_jobs WHERE id=$1", *acceptedJob))
		if err != nil {
			return j, err
		}
		expected := backup.Job{Kind: "restore", DestinationID: j.DestinationID, Source: j.Source, Target: &plan.Target, ArtifactID: artifactID}
		if backupRequestHash(expected) != storedHash || backupRequestHash(j) != storedHash {
			return backup.Job{}, backup.ErrConflict
		}
		return j, nil
	}
	if time.Now().After(expiry) {
		return j, fmt.Errorf("%w: restore review expired; create another review", backup.ErrConflict)
	}
	var keyUsed bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM backup_jobs WHERE identity_id=$1 AND idempotency_key=$2)", p.ID, idem).Scan(&keyUsed); err != nil {
		return j, err
	}
	if keyUsed {
		return j, backup.ErrConflict
	}
	a, err := scanBackupArtifact(tx.QueryRow(ctx, "SELECT "+backupArtifactCols+" FROM backup_artifacts WHERE id=$1 AND deleted_at IS NULL AND NOT deletion_pending", artifactID))
	if err != nil {
		return j, err
	}
	j = backup.Job{Kind: "restore", DestinationID: a.DestinationID, Source: a.Source, Target: &plan.Target, ArtifactID: a.ID}
	result, err := enqueueBackup(ctx, tx, p, j, idem)
	if err != nil {
		return j, err
	}
	if _, err = tx.Exec(ctx, "UPDATE backup_restore_plans SET used_at=now(),job_id=$2 WHERE id=$1", planID, result.ID); err != nil {
		return j, err
	}
	return result, tx.Commit(ctx)
}

// Preview data has an explicit expiry lifecycle. Do not create backup/restore
// jobs that race namespace cleanup or silently extend that data lifetime.
func rejectPreviewBackup(ctx context.Context, tx pgx.Tx, application string) error {
	if application == "" {
		return nil
	}
	var preview bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM previews WHERE application_id=$1)", application).Scan(&preview); err != nil {
		return err
	}
	if preview {
		return fmt.Errorf("%w: backups and restores are unavailable for temporary previews", backup.ErrInput)
	}
	return nil
}
