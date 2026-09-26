package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/jackc/pgx/v5"
)

// An artifact whose format carries this prefix was written to object storage by
// the database engine itself. The server never streamed the bytes, so it has no
// digest of its own to record. Only the server writes the format column.
const engineArtifactFormat = "engine:"

// An engine backup name such as a ClickHouse backup identifier. The same bound
// as the engine_ref column in migration 044.
const maxEngineRef = 256

// A resumable job is one waiting on a backup the database engine is performing
// itself: hakopod holds only the engine's own identifier for it.
const resumableBackupJob = "engine_ref IS NOT NULL"

// One durable streaming slot across API processes, for the jobs that actually
// move bytes through this server. A job that is only polling an engine parks in
// 'queued' with its engine_ref instead of holding that slot, and stays claimable
// while a dump streams, because polling competes for nothing. Expired running
// work fails visibly instead of replaying a restore that might have already
// created data, unless it is resumable: there the engine is still working and
// only the poller died.
func (s *Store) ClaimBackupJob(ctx context.Context, lease string) (backup.Job, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return backup.Job{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044231)"); err != nil {
		return backup.Job{}, err
	}
	// Park resumable work before the reaper runs. Failing it would abandon a
	// backup the engine goes on to finish, leaving objects nothing can delete.
	if _, err = tx.Exec(ctx, "UPDATE backup_jobs SET status='queued',lease='',lease_until=NULL WHERE status='running' AND lease_until<now() AND "+resumableBackupJob); err != nil {
		return backup.Job{}, err
	}
	if _, err = tx.Exec(ctx, "UPDATE backup_jobs SET status='failed',error='Worker stopped before confirming completion. Any new restore database may be partial; inspect it and create a new review. An unrecorded object or multipart upload may require storage cleanup.',finished_at=now(),lease='' WHERE status='running' AND lease_until<now()"); err != nil {
		return backup.Job{}, err
	}
	var active bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM backup_jobs WHERE status='running')").Scan(&active); err != nil {
		return backup.Job{}, err
	}
	// While a job streams, only resumable work may be claimed. A cancelled
	// resumable job is still claimed so the worker can stop the engine and record
	// the outcome; an ordinary queued job cancels without ever being claimed.
	// started_at survives a re-claim so elapsed engine time stays truthful.
	j, err := scanBackupJob(tx.QueryRow(ctx, "UPDATE backup_jobs SET status='running',lease=$1,lease_until=now()+interval '30 seconds',started_at=COALESCE(started_at,now()) WHERE id=(SELECT id FROM backup_jobs WHERE status='queued' AND (NOT cancel_requested OR "+resumableBackupJob+") AND (NOT $2 OR "+resumableBackupJob+") AND NOT EXISTS(SELECT 1 FROM volume_resizes vr WHERE (vr.application_id=backup_jobs.source->>'application_id' OR vr.application_id=backup_jobs.target->>'application_id') AND "+resizeBlocking+") ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING "+backupJobCols, lease, active))
	if err != nil {
		if errors.Is(err, backup.ErrNotFound) {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return j, commitErr
			}
		}
		return j, err
	}
	return j, tx.Commit(ctx)
}
func (s *Store) HeartbeatBackupJob(ctx context.Context, id, lease string) (bool, error) {
	j, err := s.BackupJob(ctx, id)
	if err != nil {
		return true, err
	}
	if j.Status != "running" || j.Lease != lease || j.CancelRequested {
		return true, nil
	}
	if j.Authority != nil {
		if e := s.authorizeWorkerBackup(ctx, j); e != nil {
			return true, e
		}
	} else if j.KeyID != "" {
		p, e := s.KeyPrincipal(ctx, j.KeyID)
		if e != nil {
			return true, e
		}
		if p.ID != j.IdentityID || !p.IsAdmin() {
			return true, ErrForbidden
		}
	} else {
		var authorized bool
		if err = s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM identities WHERE id=$1 AND admin AND NOT disabled AND project='' AND environment='')", j.IdentityID).Scan(&authorized); err != nil {
			return true, err
		}
		if !authorized {
			return true, ErrForbidden
		}
	}
	r, err := s.Pool.Exec(ctx, "UPDATE backup_jobs SET lease_until=now()+interval '30 seconds' WHERE id=$1 AND status='running' AND lease=$2 AND lease_until>now() AND NOT cancel_requested", id, lease)
	if err != nil {
		return true, err
	}
	return r.RowsAffected() != 1, nil
}

// Record the engine's own identifier for the backup it has started, so a later
// poll can find it again. Safe to repeat, and only the lease holder may write it.
// Reports true when the lease is no longer held, matching HeartbeatBackupJob.
func (s *Store) SetBackupJobEngineRef(ctx context.Context, id, lease, ref string) (bool, error) {
	if ref == "" || len(ref) > maxEngineRef {
		return true, backup.ErrInput
	}
	r, err := s.Pool.Exec(ctx, "UPDATE backup_jobs SET engine_ref=$3 WHERE id=$1 AND status='running' AND lease=$2 AND lease_until>now()", id, lease, ref)
	if err != nil {
		return true, err
	}
	return r.RowsAffected() != 1, nil
}

// Park a job that is waiting on the engine. It gives up the streaming slot and
// its lease but keeps its engine_ref and started_at, so ClaimBackupJob picks it
// up again for the next poll instead of the reaper failing it. Reports true when
// the job was not parked, which means the lease moved on and this worker should
// stop touching the job.
func (s *Store) ReleaseBackupJobToEngine(ctx context.Context, id, lease string) (bool, error) {
	r, err := s.Pool.Exec(ctx, "UPDATE backup_jobs SET status='queued',lease='',lease_until=NULL WHERE id=$1 AND status='running' AND lease=$2 AND "+resumableBackupJob, id, lease)
	if err != nil {
		return true, err
	}
	return r.RowsAffected() != 1, nil
}

// The engine identifier a parked job is waiting on. Empty when the job is an
// ordinary dump or has not reached the engine yet.
func (s *Store) BackupJobEngineRef(ctx context.Context, id string) (string, error) {
	var ref *string
	if err := s.Pool.QueryRow(ctx, "SELECT engine_ref FROM backup_jobs WHERE id=$1", id).Scan(&ref); err != nil {
		return "", backupError(err)
	}
	if ref == nil {
		return "", nil
	}
	return *ref, nil
}

func (s *Store) FinishBackupJob(ctx context.Context, j backup.Job, status, message string, a *backup.Artifact) error {
	if status != "succeeded" && status != "failed" && status != "cancelled" {
		return backup.ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var cancelled bool
	var lease string
	var current string
	if err = tx.QueryRow(ctx, "SELECT status,lease,cancel_requested FROM backup_jobs WHERE id=$1 FOR UPDATE", j.ID).Scan(&current, &lease, &cancelled); err != nil {
		return err
	}
	if current != "running" || lease != j.Lease {
		return backup.ErrConflict
	}
	// A completed artifact remains useful even when a cancel arrived during the
	// final object-store acknowledgement. Its successful commit is truthful.
	if a != nil {
		// An engine-managed artifact has no server-computed digest, and the SQL
		// check agrees. Its reported size still has to be real.
		engineManaged := a.SHA256 == "" && strings.HasPrefix(a.Format, engineArtifactFormat)
		if status != "succeeded" || a.Bytes < 1 || (len(a.SHA256) != 64 && !engineManaged) {
			return backup.ErrInput
		}
		_, err = tx.Exec(ctx, "INSERT INTO backup_artifacts(id,job_id,destination_id,source,object_key,sha256,bytes,format,scope,schedule_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)", a.ID, j.ID, a.DestinationID, JSON(a.Source), a.ObjectKey, a.SHA256, a.Bytes, a.Format, a.Scope, j.ScheduleID)
		if err != nil {
			return err
		}
		j.ArtifactID = a.ID
		j.Bytes = a.Bytes
	}
	if len(message) > 1024 {
		message = message[:1024]
	}
	_, err = tx.Exec(ctx, "UPDATE backup_jobs SET status=$3,error=$4,artifact_id=$5,bytes=$6,lease='',lease_until=NULL,finished_at=now() WHERE id=$1 AND lease=$2", j.ID, j.Lease, status, message, j.ArtifactID, j.Bytes)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,$3,$4,$5)", j.IdentityID, j.KeyID, "backup."+j.Kind+"."+status, j.ID, JSON(map[string]any{"bytes": j.Bytes, "artifact_id": j.ArtifactID})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const backupScheduleCols = "id,name,destination_id,source,interval_hours,retention_count,enabled,revision,next_run_at,created_at,updated_at,identity_id,authority"

func scanBackupSchedule(row scanner) (backup.Schedule, error) {
	var s backup.Schedule
	var source []byte
	err := row.Scan(&s.ID, &s.Name, &s.DestinationID, &source, &s.IntervalHours, &s.RetentionCount, &s.Enabled, &s.Revision, &s.NextRunAt, &s.CreatedAt, &s.UpdatedAt, &s.IdentityID, &s.Authority)
	if err != nil {
		return s, backupError(err)
	}
	return s, json.Unmarshal(source, &s.Source)
}
func (s *Store) BackupSchedules(ctx context.Context) ([]backup.Schedule, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+backupScheduleCols+" FROM backup_schedules WHERE ($1 OR (authority->>'project'=$2 AND authority->>'environment'=$3)) ORDER BY created_at,id LIMIT 64", backupUnscoped(ctx), backupProject(ctx), backupEnvironment(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []backup.Schedule{}
	for rows.Next() {
		schedule, e := scanBackupSchedule(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, schedule)
	}
	return out, rows.Err()
}
func (s *Store) PutBackupSchedule(ctx context.Context, p Principal, schedule backup.Schedule, expected int64) (backup.Schedule, error) {
	if expected > 0 {
		if err := s.authorizeBackupSchedule(ctx, p, schedule.ID); err != nil {
			return schedule, err
		}
	}
	if err := backupAdmin(p); err != nil {
		return schedule, err
	}
	if err := schedule.Validate(); err != nil {
		return schedule, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return schedule, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044230)"); err != nil {
		return schedule, err
	}
	if err = authorizeBackupJob(ctx, tx, p, backup.Job{DestinationID: schedule.DestinationID, Source: schedule.Source}); err != nil {
		return schedule, err
	}
	schedule.Authority = backupAuthority(p)
	if err = rejectPreviewBackup(ctx, tx, schedule.Source.ApplicationID); err != nil {
		return schedule, err
	}
	var revision int64
	err = tx.QueryRow(ctx, "SELECT revision FROM backup_schedules WHERE id=$1 FOR UPDATE", schedule.ID).Scan(&revision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return schedule, err
	}
	if revision != expected {
		return schedule, backup.ErrConflict
	}
	if expected == 0 {
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM backup_schedules").Scan(&count); err != nil {
			return schedule, err
		}
		if !p.IsAdmin() {
			var scopedCount int
			if err = tx.QueryRow(ctx, "SELECT count(*) FROM backup_schedules WHERE authority->>'project'=$1 AND authority->>'environment'=$2", p.Project, p.Environment).Scan(&scopedCount); err != nil {
				return schedule, err
			}
			if scopedCount >= 8 {
				return schedule, fmt.Errorf("%w: at most eight backup schedules per workspace", backup.ErrConflict)
			}
		}
		if count >= 64 {
			return schedule, fmt.Errorf("%w: at most 64 schedules are supported", backup.ErrConflict)
		}
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM backup_destinations WHERE id=$1)", schedule.DestinationID).Scan(&exists); err != nil {
		return schedule, err
	}
	if !exists {
		return schedule, backup.ErrNotFound
	}
	schedule.NextRunAt = time.Now().UTC().Add(time.Duration(schedule.IntervalHours) * time.Hour)
	result, err := scanBackupSchedule(tx.QueryRow(ctx, "INSERT INTO backup_schedules(id,name,destination_id,source,interval_hours,retention_count,enabled,revision,next_run_at,identity_id,authority) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(id) DO UPDATE SET name=excluded.name,destination_id=excluded.destination_id,source=excluded.source,interval_hours=excluded.interval_hours,retention_count=excluded.retention_count,enabled=excluded.enabled,revision=excluded.revision,next_run_at=excluded.next_run_at,identity_id=excluded.identity_id,authority=excluded.authority,updated_at=now() RETURNING "+backupScheduleCols, schedule.ID, schedule.Name, schedule.DestinationID, JSON(schedule.Source), schedule.IntervalHours, schedule.RetentionCount, schedule.Enabled, expected+1, schedule.NextRunAt, p.ID, schedule.Authority))
	if err != nil {
		return schedule, err
	}
	if err = backupAudit(ctx, tx, p, "backup.schedule.configured", schedule.ID); err != nil {
		return schedule, err
	}
	return result, tx.Commit(ctx)
}
func (s *Store) DeleteBackupSchedule(ctx context.Context, p Principal, id string, revision int64) error {
	if err := s.authorizeBackupSchedule(ctx, p, id); err != nil {
		return err
	}
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
	r, err := tx.Exec(ctx, "DELETE FROM backup_schedules WHERE id=$1 AND revision=$2", id, revision)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return backup.ErrConflict
	}
	if err = backupAudit(ctx, tx, p, "backup.schedule.deleted", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) QueueDueBackups(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044230)"); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "SELECT "+backupScheduleCols+" FROM backup_schedules WHERE enabled AND next_run_at<=now() ORDER BY next_run_at LIMIT 8 FOR UPDATE")
	if err != nil {
		return err
	}
	due := []backup.Schedule{}
	for rows.Next() {
		schedule, e := scanBackupSchedule(rows)
		if e != nil {
			rows.Close()
			return e
		}
		due = append(due, schedule)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, schedule := range due {
		var authorized, active bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM identities WHERE id=$1 AND admin AND NOT disabled AND project='' AND environment=''),EXISTS(SELECT 1 FROM backup_jobs WHERE schedule_id=$2 AND status IN ('queued','running'))", schedule.IdentityID, schedule.ID).Scan(&authorized, &active); err != nil {
			return err
		}
		var scoped Principal
		if schedule.Authority != nil {
			var e error
			scoped, e = s.backupWorkerPrincipal(ctx, schedule.IdentityID, "", schedule.Authority)
			authorized = e == nil
		}
		if !authorized {
			if _, err = tx.Exec(ctx, "UPDATE backup_schedules SET enabled=false,revision=revision+1,updated_at=now() WHERE id=$1", schedule.ID); err != nil {
				return err
			}
			continue
		}
		if !active {
			var full bool
			if err = tx.QueryRow(ctx, "SELECT count(*)>=64 OR count(*) FILTER (WHERE authority->>'project'=$1 AND authority->>'environment'=$2)>=4 FROM backup_jobs WHERE status IN ('queued','running')", scoped.Project, scoped.Environment).Scan(&full); err != nil {
				return err
			}
			// Retry later without rolling back other workspaces' scheduled jobs.
			if full {
				if _, err = tx.Exec(ctx, "UPDATE backup_schedules SET next_run_at=now()+interval '1 minute' WHERE id=$1", schedule.ID); err != nil {
					return err
				}
				continue
			}
		}
		if !active {
			p := Principal{ID: schedule.IdentityID, Admin: true, Permissions: []string{"admin"}}
			if schedule.Authority != nil {
				p = scoped
			}
			job := backup.Job{Kind: "backup", DestinationID: schedule.DestinationID, Source: schedule.Source, ScheduleID: schedule.ID}
			if e := authorizeBackupJob(ctx, tx, p, job); e != nil {
				if !errors.Is(e, backup.ErrNotFound) && !errors.Is(e, ErrForbidden) {
					return e
				}
				// A deleted source or revoked destination must not stall every
				// other workspace's scheduler transaction.
				if _, err = tx.Exec(ctx, "UPDATE backup_schedules SET enabled=false,revision=revision+1,updated_at=now() WHERE id=$1", schedule.ID); err != nil {
					return err
				}
				continue
			}
			_, err = enqueueBackup(ctx, tx, p, job, "schedule:"+schedule.ID+":"+schedule.NextRunAt.UTC().Format(time.RFC3339Nano))
			if err != nil {
				return err
			}
		}
		// Coalesce missed intervals after downtime, without catch-up storms.
		if _, err = tx.Exec(ctx, "UPDATE backup_schedules SET next_run_at=now()+interval '1 hour'*interval_hours WHERE id=$1", schedule.ID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, pruneUnusedBackupRestorePlans); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM backup_jobs WHERE id IN (SELECT id FROM backup_jobs WHERE finished_at<now()-interval '90 days' ORDER BY finished_at LIMIT 100)"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) ExpiredBackupArtifacts(ctx context.Context, limit int) ([]backup.Artifact, error) {
	limit = min(max(limit, 1), 16)
	rows, err := s.Pool.Query(ctx, "SELECT "+backupArtifactCols+" FROM backup_artifacts WHERE deleted_at IS NULL AND (deletion_pending OR id IN (SELECT id FROM (SELECT a.id,s.retention_count,row_number() OVER(PARTITION BY a.schedule_id ORDER BY a.created_at DESC,a.id DESC) position FROM backup_artifacts a JOIN backup_schedules s ON s.id=a.schedule_id WHERE a.deleted_at IS NULL) ranked WHERE position>retention_count)) AND NOT EXISTS(SELECT 1 FROM backup_jobs j WHERE j.artifact_id=backup_artifacts.id AND j.kind='restore' AND j.status IN ('queued','running')) ORDER BY created_at LIMIT $1", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []backup.Artifact{}
	for rows.Next() {
		a, e := scanBackupArtifact(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) MarkBackupArtifactDeleted(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, "UPDATE backup_artifacts SET deleted_at=now(),deletion_pending=false WHERE id=$1 AND deleted_at IS NULL", id)
	return err
}
func (s *Store) ClaimBackupArtifactDeletion(ctx context.Context, id string) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044230)"); err != nil {
		return false, err
	}
	result, err := tx.Exec(ctx, "UPDATE backup_artifacts SET deletion_pending=true WHERE id=$1 AND deleted_at IS NULL AND NOT EXISTS(SELECT 1 FROM backup_jobs WHERE artifact_id=$1 AND kind='restore' AND status IN ('queued','running'))", id)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}
