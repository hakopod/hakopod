package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/database"
)

func TestRecoveringDatabaseRequiresCurrentJobBeforePublishingReady(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "recovery-observation-create", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	old := database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now().Add(-time.Minute), Members: []database.Member{{Name: "old-gateway", UID: "old-uid", Ready: true}}}
	if err = s.RecordDatabaseStep(ctx, op, old, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	jobID := NewID()
	target := backup.Target{Source: backup.Source{Kind: "managed_database", ManagedDatabaseID: d.ID, Engine: d.Spec.Engine}, Revision: 1}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO backup_jobs(id,kind,status,destination_id,source,target,identity_id,idempotency_key,request_hash,started_at,lease,lease_until) VALUES($1,'restore','running','destination-1',$2,$3,$4,$1,$1,now(),'restore-worker',now()+interval '1 minute')", jobID, JSON(target.Source), JSON(target), p.ID); err != nil {
		t.Fatal(err)
	}
	recovery := database.Recovery{JobID: jobID, ArtifactID: "artifact-fixture"}
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_databases SET status='restoring',recovery=$2 WHERE id=$1", d.ID, JSON(recovery)); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.DatabaseInternal(ctx, d.ID)
	if err != nil || loaded.Status != "restoring" || loaded.Observation.Members[0].UID != "old-uid" {
		t.Fatal("restore did not retain its pre-recovery observation", loaded.Status, err)
	}
	stale := database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now().Add(-database.ObservationMaxAge - time.Second)}
	if err = s.ObserveRecoveringDatabase(ctx, d.ID, 1, jobID, stale); !errors.Is(err, ErrInput) {
		t.Fatal("stale recovery observation was accepted", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE backup_jobs SET cancel_requested=true WHERE id=$1", jobID); err != nil {
		t.Fatal(err)
	}
	fresh := database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now().UTC(), Members: []database.Member{{Name: "new-gateway", UID: "new-uid", Ready: true}}}
	if err = s.ObserveRecoveringDatabase(ctx, d.ID, 1, jobID, fresh); !errors.Is(err, ErrConflict) {
		t.Fatal("cancelled recovery job replaced the observation", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE backup_jobs SET cancel_requested=false WHERE id=$1", jobID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE backup_jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", jobID); err != nil {
		t.Fatal(err)
	}
	if err = s.ObserveRecoveringDatabase(ctx, d.ID, 1, jobID, fresh); !errors.Is(err, ErrConflict) {
		t.Fatal("expired recovery worker replaced the observation", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE backup_jobs SET lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1", jobID); err != nil {
		t.Fatal(err)
	}
	if err = s.ObserveRecoveringDatabase(ctx, d.ID, 1, "wrong-job", fresh); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong recovery job replaced the observation", err)
	}
	wrongRevision := fresh
	wrongRevision.Revision = 2
	if err = s.ObserveRecoveringDatabase(ctx, d.ID, 2, jobID, wrongRevision); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong database revision replaced the observation", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE backup_jobs SET status='succeeded',finished_at=now() WHERE id=$1", jobID); err != nil {
		t.Fatal(err)
	}
	if err = s.ObserveRecoveringDatabase(ctx, d.ID, 1, jobID, fresh); !errors.Is(err, ErrConflict) {
		t.Fatal("finished recovery job replaced the observation", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE backup_jobs SET status='running',finished_at=NULL WHERE id=$1", jobID); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.DatabaseInternal(ctx, d.ID)
	if err != nil || loaded.Observation.Members[0].UID != "old-uid" {
		t.Fatal("rejected recovery observation changed the stored member", err)
	}
	if err = s.ObserveRecoveringDatabase(ctx, d.ID, 1, jobID, fresh); err != nil {
		t.Fatal(err)
	}
	older := fresh
	older.ObservedAt = fresh.ObservedAt.Add(-time.Second)
	older.Members = []database.Member{{Name: "old-member", UID: "older-uid", Ready: true}}
	if err = s.ObserveRecoveringDatabase(ctx, d.ID, 1, jobID, older); !errors.Is(err, ErrConflict) {
		t.Fatal("older recovery observation replaced the current member", err)
	}
	loaded, err = s.DatabaseInternal(ctx, d.ID)
	if err != nil || loaded.Status != "restoring" || loaded.Observation.Members[0].UID != "new-uid" {
		t.Fatal("fresh recovery observation was not stored before readiness", loaded.Status, err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE backup_jobs SET status='succeeded',finished_at=now() WHERE id=$1", jobID); err != nil {
		t.Fatal(err)
	}
	if err = s.RefreshDatabaseRecoveries(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.DatabaseInternal(ctx, d.ID)
	if err != nil || loaded.Status != "ready" || loaded.Observation.Members[0].UID != "new-uid" {
		t.Fatal("ready database did not retain the post-restore observation", loaded.Status, err)
	}
}
