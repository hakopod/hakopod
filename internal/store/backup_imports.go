package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/jackc/pgx/v5"
)

const importCols = "id,spec,status,expires_at,artifact_id,lease"

func scanImport(row scanner) (backup.Import, error) {
	var i backup.Import
	err := row.Scan(&i.ID, &i.Spec, &i.Status, &i.ExpiresAt, &i.ArtifactID, &i.Lease)
	return i, err
}

func (s *Store) PrepareBackupImport(ctx context.Context, p Principal, input backup.ImportSpec, idem string) (backup.Import, error) {
	if !p.CanManageBackups() {
		return backup.Import{}, ErrForbidden
	}
	if err := input.Validate(time.Now()); err != nil {
		return backup.Import{}, err
	}
	if len(idem) < 8 || len(idem) > 128 {
		return backup.Import{}, ErrInput
	}
	d, err := s.BackupDestination(WithBackupPrincipal(ctx, p), input.DestinationID)
	if err != nil {
		return backup.Import{}, err
	}
	hash := sha256.Sum256(JSON(input))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return backup.Import{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,48))", p.ID); err != nil {
		return backup.Import{}, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044230)"); err != nil {
		return backup.Import{}, err
	}
	if err = tx.QueryRow(ctx, "SELECT revision FROM backup_destinations WHERE id=$1 FOR SHARE", input.DestinationID).Scan(&d.Revision); err != nil {
		return backup.Import{}, err
	}
	var global int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM backup_imports WHERE status IN ('pending','uploading','cleaning')").Scan(&global); err != nil {
		return backup.Import{}, err
	}
	if global >= 128 {
		return backup.Import{}, fmt.Errorf("%w: too many pending archive imports", ErrConflict)
	}
	var id string
	var old []byte
	err = tx.QueryRow(ctx, "SELECT id,request_hash FROM backup_imports WHERE identity_id=$1 AND idempotency_key=$2", p.ID, idem).Scan(&id, &old)
	if err == nil {
		if !bytes.Equal(old, hash[:]) {
			return backup.Import{}, ErrConflict
		}
		return scanImport(tx.QueryRow(ctx, "SELECT "+importCols+" FROM backup_imports WHERE id=$1", id))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return backup.Import{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM backup_imports WHERE identity_id=$1 AND status IN ('pending','uploading','cleaning')", p.ID).Scan(&count); err != nil {
		return backup.Import{}, err
	}
	if count >= 16 {
		return backup.Import{}, fmt.Errorf("%w: at most sixteen pending archive imports", ErrConflict)
	}
	id = NewID()
	i, err := scanImport(tx.QueryRow(ctx, "INSERT INTO backup_imports(id,identity_id,key_id,idempotency_key,request_hash,spec,destination_revision,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,now()+interval '30 minutes') RETURNING "+importCols, id, p.ID, p.KeyID, idem, hash[:], JSON(input), d.Revision))
	if err != nil {
		return i, err
	}
	return i, tx.Commit(ctx)
}

func (s *Store) BackupImport(ctx context.Context, p Principal, id string) (backup.Import, error) {
	if !p.CanManageBackups() {
		return backup.Import{}, ErrForbidden
	}
	i, err := scanImport(s.Pool.QueryRow(ctx, "SELECT "+importCols+" FROM backup_imports WHERE id=$1 AND identity_id=$2", id, p.ID))
	if err != nil {
		return i, err
	}
	_, err = s.BackupDestination(WithBackupPrincipal(ctx, p), i.Spec.DestinationID)
	return i, err
}
func (s *Store) ClaimBackupImport(ctx context.Context, p Principal, id string) (backup.Import, error) {
	i, err := s.BackupImport(ctx, p, id)
	if err != nil || i.Status == "completed" {
		return i, err
	}
	lease := NewID()
	i, err = scanImport(s.Pool.QueryRow(ctx, "UPDATE backup_imports SET status='uploading',lease=$3,lease_until=now()+interval '16 minutes' WHERE id=$1 AND identity_id=$2 AND expires_at>now() AND status='pending' RETURNING "+importCols, id, p.ID, lease))
	if errors.Is(err, pgx.ErrNoRows) {
		err = fmt.Errorf("%w: import review expired or another upload is running", ErrConflict)
	}
	return i, err
}
func (s *Store) CheckBackupImport(ctx context.Context, p Principal, i backup.Import) error {
	current, err := s.KeyPrincipal(ctx, p.KeyID)
	if err != nil {
		return err
	}
	current.Project, current.Environment = p.Project, p.Environment
	if current.ID != p.ID || !current.CanManageBackups() {
		return ErrForbidden
	}
	d, err := s.BackupDestination(WithBackupPrincipal(ctx, current), i.Spec.DestinationID)
	if err != nil {
		return err
	}
	var valid bool
	err = s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM backup_imports WHERE id=$1 AND identity_id=$2 AND lease=$3 AND status='uploading' AND lease_until>now() AND destination_revision=$4)", i.ID, p.ID, i.Lease, d.Revision).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	if s.AuthorizeBackup != nil && d.Project != "" {
		return s.AuthorizeBackup(ctx, current.ID, d.Project, d.Environment)
	}
	return nil
}
func (s *Store) ReleaseBackupImport(ctx context.Context, i backup.Import) error {
	_, err := s.Pool.Exec(ctx, "UPDATE backup_imports SET status='pending',lease='',lease_until=NULL WHERE id=$1 AND lease=$2 AND status='uploading'", i.ID, i.Lease)
	return err
}

func (s *Store) FinishBackupImport(ctx context.Context, p Principal, i backup.Import, a backup.Artifact) error {
	if err := s.CheckBackupImport(ctx, p, i); err != nil {
		return err
	}
	if a.ID != i.ID || a.Source.Kind != "docker_import" || a.DestinationID != i.Spec.DestinationID || a.SourceVersion != i.Spec.SourceVersion || a.VerifiedAt == nil || a.CapturedAt == nil || !a.CapturedAt.Equal(i.Spec.CapturedAt) || a.Bytes < 1 || len(a.SHA256) != 64 {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var lock string
	if err = tx.QueryRow(ctx, "SELECT id FROM backup_imports WHERE id=$1 FOR UPDATE", i.ID).Scan(&lock); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, "SELECT id FROM backup_destinations WHERE id=$1 FOR SHARE", i.Spec.DestinationID).Scan(&lock); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, "SELECT id FROM api_keys WHERE id=$1 FOR SHARE", p.KeyID).Scan(&lock); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, "SELECT id FROM identities WHERE id=$1 FOR SHARE", p.ID).Scan(&lock); err != nil {
		return err
	}
	if err = s.CheckBackupImport(ctx, p, i); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, "UPDATE backup_imports SET status='completed',artifact_id=$1,lease='',lease_until=NULL WHERE id=$1 AND identity_id=$2 AND lease=$3 AND status='uploading' AND lease_until>now()", i.ID, p.ID, i.Lease)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, "INSERT INTO backup_jobs(id,kind,status,destination_id,source,artifact_id,identity_id,key_id,idempotency_key,request_hash,authority,bytes,started_at,finished_at) VALUES($1,'backup','succeeded',$2,$3,$1,$4,$5,$6,$6,$7,$8,now(),now())", i.ID, a.DestinationID, JSON(a.Source), p.ID, p.KeyID, "import:"+i.ID, backupAuthority(p), a.Bytes)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO backup_artifacts(id,job_id,destination_id,source,object_key,sha256,bytes,format,scope,source_version,captured_at,verified_at) VALUES($1,$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)", i.ID, a.DestinationID, JSON(a.Source), a.ObjectKey, a.SHA256, a.Bytes, a.Format, a.Scope, a.SourceVersion, a.CapturedAt, a.VerifiedAt)
	if err != nil {
		return err
	}
	if err = backupAudit(ctx, tx, p, "backup.import", i.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CleanupBackupImports fences abandoned uploads before touching their staging
// objects. A retry cannot start until deletion has completed under this lease.
func (s *Store) CleanupBackupImports(ctx context.Context, discard func(context.Context, backup.Import) error) error {
	for n := 0; n < 4; n++ {
		lease := NewID()
		i, err := scanImport(s.Pool.QueryRow(ctx, `UPDATE backup_imports SET status='cleaning',lease=$1,lease_until=now()+interval '1 minute' WHERE id=(SELECT id FROM backup_imports WHERE (status='pending' AND expires_at<now()) OR (status IN ('uploading','cleaning') AND lease_until<now()) ORDER BY expires_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING `+importCols, lease))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		clean, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = discard(clean, i)
		cancel()
		if err != nil {
			return err
		}
		_, err = s.Pool.Exec(ctx, "UPDATE backup_imports SET status=CASE WHEN expires_at>now() THEN 'pending' ELSE 'expired' END,lease='',lease_until=NULL WHERE id=$1 AND lease=$2 AND status='cleaning'", i.ID, i.Lease)
		if err != nil {
			return err
		}
	}
	return nil
}
