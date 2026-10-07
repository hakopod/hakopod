package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/database"
)

func TestMyDuckColdStorageFenceRequiresAndFollowsExactWorkerLease(t *testing.T) {
	db := isolatedDatabase(t)
	_ = bootstrapPrincipal(t, db)
	ctx := context.Background()
	databaseID, jobID := NewID(), NewID()
	spec := database.Spec{SchemaVersion: 1, Name: "cold", Engine: "duckdb", Version: database.MyDuckVersion, Mode: "standalone", Shards: 1, CPU: "500m", Memory: "1Gi", StorageGiB: 10, TLS: &database.TLSConfig{Mode: "required"}}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO managed_databases(id,project,environment,name,revision,spec,status,credentials) VALUES($1,'demo','development',$2,1,$3,'ready',$4)`, databaseID, spec.Name, JSON(spec), []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	source := backup.Source{Kind: "managed_database", ManagedDatabaseID: databaseID, Engine: "duckdb"}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO backup_jobs(id,kind,status,destination_id,source,identity_id,idempotency_key,request_hash,lease,lease_until) VALUES($1,'backup','running','destination',$2,'identity','cold','hash','lease-a',now()+interval '1 minute')`, jobID, JSON(source)); err != nil {
		t.Fatal(err)
	}
	if err := db.AcquireDatabaseColdStorageFence(ctx, databaseID, 1, jobID, "wrong", "backup"); !errors.Is(err, backup.ErrConflict) {
		t.Fatal("foreign lease acquired fence", err)
	}
	if err := db.AcquireDatabaseColdStorageFence(ctx, databaseID, 1, jobID, "lease-a", "backup"); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckDatabaseColdStorageWorker(ctx, databaseID, 1, jobID, "lease-a", "backup"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE backup_jobs SET cancel_requested=true WHERE id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckDatabaseColdStorageWorker(ctx, databaseID, 1, jobID, "lease-a", "backup"); !errors.Is(err, ErrClaimLost) {
		t.Fatal("cancelled worker retained transfer authority", err)
	}
	first, err := db.ClaimDatabaseColdStorageCleanup(ctx, jobID, "lease-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE backup_jobs SET lease='lease-b',cancel_requested=false WHERE id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	second, err := db.ClaimDatabaseColdStorageCleanup(ctx, jobID, "lease-b")
	if err != nil {
		t.Fatal(err)
	}
	if first.CleanupToken == second.CleanupToken {
		t.Fatal("reclaimed cleanup reused stale authority")
	}
	if err = db.CheckDatabaseColdStorageCleanup(ctx, first); !errors.Is(err, ErrClaimLost) {
		t.Fatal("stale cleanup authority remained valid", err)
	}
	if err = db.CheckDatabaseColdStorageCleanup(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE backup_jobs SET lease_until=$2 WHERE id=$1`, jobID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckDatabaseColdStorageWorker(ctx, databaseID, 1, jobID, "lease-b", "backup"); !errors.Is(err, ErrClaimLost) {
		t.Fatal("expired worker retained mutation authority", err)
	}
	if err := db.CheckDatabaseColdStorageCleanup(ctx, second); !errors.Is(err, ErrClaimLost) {
		t.Fatal("expired cleanup retained mutation authority", err)
	}
	reclaimed, err := db.ClaimBackupJob(ctx, "lease-c")
	if err != nil || reclaimed.ID != jobID || reclaimed.Lease != "lease-c" || reclaimed.Status != "running" {
		t.Fatal("fenced cold job was not reclaimed for cleanup", reclaimed, err)
	}
	third, err := db.ClaimDatabaseColdStorageCleanup(ctx, jobID, "lease-c")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CheckDatabaseColdStorageCleanup(ctx, third); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckDatabaseColdStorageFence(ctx, databaseID, 1, jobID, "backup"); err != nil {
		t.Fatal("cleanup authority disappeared with worker lease", err)
	}
	if err := db.ReleaseDatabaseColdStorageFence(ctx, third); err != nil {
		t.Fatal(err)
	}
}
