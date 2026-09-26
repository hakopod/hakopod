package store

import (
	"context"
	"errors"
	"testing"

	"github.com/hakopod/hakopod/internal/backup"
)

// A queued backup job with the columns the worker needs and nothing else. The
// scheduler and the authorization path are covered elsewhere; these checks are
// about the durable claim, park and reap transitions.
func queuedBackupJob(t *testing.T, db *Store, id string) {
	t.Helper()
	source := backup.Source{Kind: "database", ApplicationID: id, Service: "db", Engine: "clickhouse", Database: "main"}
	_, err := db.Pool.Exec(context.Background(), "INSERT INTO backup_jobs(id,kind,status,destination_id,source,identity_id,idempotency_key,request_hash) VALUES($1,'backup','queued','destination-1',$2,'identity-1',$1,$1)", id, JSON(source))
	if err != nil {
		t.Fatal(err)
	}
}

func expireBackupLease(t *testing.T, db *Store, id string) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), "UPDATE backup_jobs SET lease_until=now()-interval '1 minute' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
}

func backupJobStatus(t *testing.T, db *Store, id string) (string, string) {
	t.Helper()
	var status, message string
	if err := db.Pool.QueryRow(context.Background(), "SELECT status,error FROM backup_jobs WHERE id=$1", id).Scan(&status, &message); err != nil {
		t.Fatal(err)
	}
	return status, message
}

func TestEngineManagedArtifactDigestConstraint(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	insert := func(sha, format string, bytes int64) error {
		_, err := db.Pool.Exec(ctx, "INSERT INTO backup_artifacts(id,job_id,destination_id,source,object_key,sha256,bytes,format,scope) VALUES($1,$1,'destination-1','{}','key',$2,$3,$4,'scope')", NewID(), sha, bytes, format)
		return err
	}
	if err := insert("", "engine:clickhouse", 4096); err != nil {
		t.Fatal("an engine-managed artifact must be accepted without a digest:", err)
	}
	if err := insert("", "engine:clickhouse", 0); err == nil {
		t.Fatal("an engine-managed artifact of zero bytes must be rejected")
	}
	if err := insert("abc123", "postgresql-custom", 4096); err == nil {
		t.Fatal("a dump artifact with a short digest must be rejected")
	}
	if err := insert("", "postgresql-custom", 4096); err == nil {
		t.Fatal("a dump artifact without a digest must be rejected")
	}
	full := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := insert(full, "postgresql-custom", 4096); err != nil {
		t.Fatal("a dump artifact with a full digest must still be accepted:", err)
	}
}

func TestBackupJobEngineRefRoundTrip(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	queuedBackupJob(t, db, "job-engine-ref")
	j, err := db.ClaimBackupJob(ctx, "lease-a")
	if err != nil {
		t.Fatal(err)
	}
	if ref, err := db.BackupJobEngineRef(ctx, j.ID); err != nil || ref != "" {
		t.Fatal("a fresh job carries no engine reference:", ref, err)
	}
	if lost, err := db.SetBackupJobEngineRef(ctx, j.ID, "lease-a", "hakopod_job_engine_ref"); err != nil || lost {
		t.Fatal("the lease holder must be able to record the engine reference:", lost, err)
	}
	// Recording the same reference again is how a retried poll behaves.
	if lost, err := db.SetBackupJobEngineRef(ctx, j.ID, "lease-a", "hakopod_job_engine_ref"); err != nil || lost {
		t.Fatal("recording the engine reference must be repeatable:", lost, err)
	}
	ref, err := db.BackupJobEngineRef(ctx, j.ID)
	if err != nil || ref != "hakopod_job_engine_ref" {
		t.Fatal("the engine reference must round-trip:", ref, err)
	}
	if lost, err := db.SetBackupJobEngineRef(ctx, j.ID, "lease-b", "other"); err != nil || !lost {
		t.Fatal("a worker without the lease must not write the engine reference:", lost, err)
	}
	if lost, err := db.SetBackupJobEngineRef(ctx, j.ID, "lease-a", ""); !errors.Is(err, backup.ErrInput) || !lost {
		t.Fatal("an empty engine reference is invalid input:", lost, err)
	}
}

// Requirement: a job waiting on the engine releases the streaming slot, and an
// expired lease returns it to the queue for another poll instead of failing it.
func TestResumableBackupJobReleasedAndReclaimed(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	queuedBackupJob(t, db, "job-resumable")
	j, err := db.ClaimBackupJob(ctx, "lease-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SetBackupJobEngineRef(ctx, j.ID, "lease-a", "engine-backup-1"); err != nil {
		t.Fatal(err)
	}
	if lost, err := db.ReleaseBackupJobToEngine(ctx, j.ID, "lease-a"); err != nil || lost {
		t.Fatal("the lease holder must be able to park a job waiting on the engine:", lost, err)
	}
	if status, _ := backupJobStatus(t, db, j.ID); status != "queued" {
		t.Fatal("a parked job must not hold the running slot, got status", status)
	}
	// A parked job holds nothing, so an ordinary dump takes the streaming slot
	// while the engine works. Backdated so the queue order picks it first.
	queuedBackupJob(t, db, "job-dump")
	if _, err = db.Pool.Exec(ctx, "UPDATE backup_jobs SET created_at=now()-interval '1 hour' WHERE id='job-dump'"); err != nil {
		t.Fatal(err)
	}
	dump, err := db.ClaimBackupJob(ctx, "lease-b")
	if err != nil {
		t.Fatal("a parked engine job must not starve other backups:", err)
	}
	if dump.ID != "job-dump" {
		t.Fatal("expected the dump to take the streaming slot, got", dump.ID)
	}
	if lost, err := db.ReleaseBackupJobToEngine(ctx, dump.ID, "lease-b"); err != nil || !lost {
		t.Fatal("a job with no engine reference must not be parked:", lost, err)
	}
	// Polling moves no bytes, so the resumable job stays claimable even though the
	// dump holds the slot.
	polled, err := db.ClaimBackupJob(ctx, "lease-c")
	if err != nil {
		t.Fatal("a resumable job must stay claimable while a dump streams:", err)
	}
	if polled.ID != j.ID {
		t.Fatal("expected the resumable job for polling, got", polled.ID)
	}
	// The poller died mid-wait. The engine is still working, so the job must come
	// back for another poll with its start time and error field intact.
	expireBackupLease(t, db, j.ID)
	resumed, err := db.ClaimBackupJob(ctx, "lease-d")
	if err != nil {
		t.Fatal("a resumable job with an expired lease must be re-claimable:", err)
	}
	if resumed.ID != j.ID {
		t.Fatal("expected the resumable job back for polling, got", resumed.ID)
	}
	if resumed.Status != "running" || resumed.Error != "" {
		t.Fatal("a re-claimed resumable job must not carry a failure:", resumed.Status, resumed.Error)
	}
	if resumed.StartedAt == nil || j.StartedAt == nil || !resumed.StartedAt.Equal(*j.StartedAt) {
		t.Fatal("a re-claimed resumable job must keep its original start time")
	}
	if ref, err := db.BackupJobEngineRef(ctx, j.ID); err != nil || ref != "engine-backup-1" {
		t.Fatal("a re-claimed resumable job must keep its engine reference:", ref, err)
	}
}

// Requirement: an ordinary job whose lease expired is still reaped, with the
// same message, because its streamed work is gone.
func TestNonResumableStaleBackupJobStillReaped(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	queuedBackupJob(t, db, "job-stale")
	j, err := db.ClaimBackupJob(ctx, "lease-a")
	if err != nil {
		t.Fatal(err)
	}
	expireBackupLease(t, db, j.ID)
	if _, err = db.ClaimBackupJob(ctx, "lease-b"); !errors.Is(err, backup.ErrNotFound) {
		t.Fatal("expected no further work after reaping, got", err)
	}
	status, message := backupJobStatus(t, db, j.ID)
	if status != "failed" {
		t.Fatal("a stale job with no engine reference must still fail, got", status)
	}
	if message != "Worker stopped before confirming completion. Any new restore database may be partial; inspect it and create a new review. An unrecorded object or multipart upload may require storage cleanup." {
		t.Fatal("the reaper message changed:", message)
	}
}

func TestFinishBackupJobAcceptsEngineManagedArtifact(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	queuedBackupJob(t, db, "job-finish")
	j, err := db.ClaimBackupJob(ctx, "lease-a")
	if err != nil {
		t.Fatal(err)
	}
	j.Lease = "lease-a"
	a := backup.Artifact{ID: NewID(), DestinationID: "destination-1", Source: j.Source, ObjectKey: "key", Bytes: 4096, Format: "engine:clickhouse", Scope: "scope"}
	empty := a
	empty.Bytes = 0
	if err = db.FinishBackupJob(ctx, j, "succeeded", "", &empty); !errors.Is(err, backup.ErrInput) {
		t.Fatal("an engine-managed artifact of zero bytes must be refused:", err)
	}
	short := a
	short.Format = "postgresql-custom"
	if err = db.FinishBackupJob(ctx, j, "succeeded", "", &short); !errors.Is(err, backup.ErrInput) {
		t.Fatal("a dump artifact without a digest must be refused:", err)
	}
	if err = db.FinishBackupJob(ctx, j, "succeeded", "", &a); err != nil {
		t.Fatal("an engine-managed artifact without a digest must be accepted:", err)
	}
	stored, err := db.BackupArtifact(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SHA256 != "" || stored.Bytes != 4096 || stored.Format != "engine:clickhouse" {
		t.Fatal("unexpected stored artifact:", stored.SHA256, stored.Bytes, stored.Format)
	}
}
