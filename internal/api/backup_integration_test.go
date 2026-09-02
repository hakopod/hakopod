package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/store"
)

type backupRequestClient struct {
	t      *testing.T
	server *httptest.Server
	token  string
}

func (c backupRequestClient) request(method, path string, input, output any, idem string) int {
	c.t.Helper()
	var body io.Reader
	if input != nil {
		body = bytes.NewReader(store.JSON(input))
	}
	request, err := http.NewRequest(method, c.server.URL+"/api/v1"+path, body)
	if err != nil {
		c.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	if idem != "" {
		request.Header.Set("Idempotency-Key", idem)
	}
	response, err := c.server.Client().Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	if output != nil && response.StatusCode < 400 {
		if err = json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(output); err != nil {
			c.t.Fatal("decode backup API response", err)
		}
	}
	return response.StatusCode
}

type transactionBackupRuntime struct{}

func (transactionBackupRuntime) Targets(context.Context) ([]backup.Target, error) {
	return []backup.Target{}, nil
}
func (transactionBackupRuntime) Resolve(_ context.Context, s backup.Source) (backup.Target, error) {
	return backup.Target{Source: s, Available: true}, nil
}
func (transactionBackupRuntime) Dump(context.Context, backup.Target, io.Writer) error {
	return errors.New("transaction fixture never executes backups")
}
func (transactionBackupRuntime) Restore(context.Context, backup.Target, io.Reader) error {
	return errors.New("transaction fixture never executes restores")
}

func TestBackupDurabilityAuthorizationScheduling(t *testing.T) {
	db, dsn := database(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "backup-transactions")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	management := &api.Server{Store: db, Auth: api.AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))}}
	management.ConfigureBackups(api.BackupConfig{DatabaseURL: dsn, StateDir: t.TempDir()})
	management.Backups.Runtime = transactionBackupRuntime{}
	server := httptest.NewServer(management.Handler())
	defer server.Close()
	client := backupRequestClient{t, server, raw}
	input := backup.DestinationInput{Name: "transaction-test", Endpoint: "https://storage.invalid", Region: "us-east-1", Bucket: "backup-tests", PathStyle: true, AccessKeyID: "test-access", SecretAccessKey: "test-secret"}
	var created struct {
		Destination backup.Destination `json:"destination"`
		RecoveryKey string             `json:"recovery_key"`
	}
	if status := client.request("POST", "/backup-destinations", input, &created, ""); status != 201 || created.RecoveryKey == "" {
		t.Fatalf("destination creation failed with status %d", status)
	}
	d, err := db.BackupDestination(ctx, created.Destination.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(d.EncryptedCredentials, []byte(input.SecretAccessKey)) || bytes.Contains(d.EncryptedCredentials, []byte(created.RecoveryKey)) {
		t.Fatal("database contains unencrypted backup credentials")
	}
	var listed map[string]any
	if status := client.request("GET", "/backup-destinations", nil, &listed, ""); status != 200 {
		t.Fatal(status)
	}
	text := string(store.JSON(listed))
	if strings.Contains(text, "test-secret") || strings.Contains(text, "AGE-SECRET-KEY-") {
		t.Fatal("read endpoint exposed backup credentials")
	}
	_, scoped, err := db.CreateKey(ctx, principal, store.KeyInput{Name: "deployment-only", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "deployments:write"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := client
	unauthorized.token = scoped
	if status := unauthorized.request("GET", "/backup-destinations", nil, nil, ""); status != 403 {
		t.Fatalf("scoped key accessed backup credentials: %d", status)
	}
	source := backup.Source{Kind: "management", Engine: "postgresql"}
	request := map[string]any{"destination_id": d.ID, "source": source}
	var j, repeat backup.Job
	if status := client.request("POST", "/backups", request, &j, "durable-backup-idem"); status != 202 {
		t.Fatal("enqueue", status)
	}
	if status := client.request("POST", "/backups", request, &repeat, "durable-backup-idem"); status != 202 || repeat.ID != j.ID {
		t.Fatal("idempotency failed", status)
	}
	reopened, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.BackupJob(ctx, j.ID)
	reopened.Close()
	if err != nil || persisted.Status != "queued" {
		t.Fatal("accepted job was not durable", err)
	}
	claimed, err := db.ClaimBackupJob(ctx, "worker-a")
	if err != nil || claimed.ID != j.ID {
		t.Fatal("claim failed", err)
	}
	if _, err = db.ClaimBackupJob(ctx, "worker-b"); !errors.Is(err, backup.ErrNotFound) {
		t.Fatal("second process claimed a global running slot", err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE backup_jobs SET lease_until=now()-interval '1 minute' WHERE id=$1", j.ID); err != nil {
		t.Fatal(err)
	}
	_, _ = db.ClaimBackupJob(ctx, "replacement")
	expired, err := db.BackupJob(ctx, j.ID)
	if err != nil || expired.Status != "failed" {
		t.Fatal("interrupted operation was silently replayed", err)
	}
	var schedule backup.Schedule
	scheduleInput := map[string]any{"name": "daily", "destination_id": d.ID, "source": source, "interval_hours": 24, "retention_count": 2, "enabled": true, "expected_revision": 0}
	if status := client.request("POST", "/backup-schedules", scheduleInput, &schedule, ""); status != 201 {
		t.Fatal("schedule", status)
	}
	for range 2 {
		if _, err = db.Pool.Exec(ctx, "UPDATE backup_schedules SET next_run_at=now()-interval '3 days' WHERE id=$1", schedule.ID); err != nil {
			t.Fatal(err)
		}
		if err = db.QueueDueBackups(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM backup_jobs WHERE schedule_id=$1", schedule.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("schedule catch-up created overlap", count, err)
	}
	artifactID := store.NewID()
	if _, err = db.Pool.Exec(ctx, "INSERT INTO backup_artifacts(id,job_id,destination_id,source,object_key,sha256,bytes,format,scope) VALUES($1,$2,$3,$4,$5,$6,128,'transaction-fixture','metadata transaction test only')", artifactID, store.NewID(), d.ID, store.JSON(source), backup.ObjectKey(d, artifactID), strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	plan := backup.RestorePlan{ID: store.NewID(), ArtifactID: artifactID, Target: backup.Target{Source: backup.Source{Kind: "database", Engine: "postgresql", Database: "hp_restore_12345678901234567890"}}, Confirmation: "hp_restore_12345678901234567890", ExpiresAt: time.Now().Add(time.Minute)}
	if err = db.SaveBackupRestorePlan(ctx, principal, plan); err != nil {
		t.Fatal(err)
	}
	if claimed, err := db.ClaimBackupArtifactDeletion(ctx, artifactID); err != nil || !claimed {
		t.Fatal("could not fence artifact deletion", err)
	}
	if _, err = db.AcceptBackupRestore(ctx, principal, artifactID, plan.ID, plan.Confirmation, "deleted-artifact-restore"); err == nil {
		t.Fatal("restore raced past pending deletion")
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE backup_artifacts SET deletion_pending=false WHERE id=$1", artifactID); err != nil {
		t.Fatal(err)
	}
	restore, err := db.AcceptBackupRestore(ctx, principal, artifactID, plan.ID, plan.Confirmation, "active-artifact-restore")
	if err != nil {
		t.Fatal(err)
	}
	assertRestoreReplay := func(stage string) {
		t.Helper()
		var replay backup.Job
		status := client.request("POST", "/backup-artifacts/"+artifactID+"/restore", map[string]string{"plan_id": plan.ID, "confirmation": plan.Confirmation}, &replay, "active-artifact-restore")
		if status != 202 || replay.ID != restore.ID {
			t.Fatalf("accepted restore retry was lost after %s: status %d", stage, status)
		}
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE backup_restore_plans SET expires_at=now()-interval '1 minute' WHERE id=$1", plan.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueueDueBackups(ctx); err != nil {
		t.Fatal(err)
	}
	otherPlan := plan
	otherPlan.ID = store.NewID()
	otherPlan.ExpiresAt = time.Now().Add(time.Minute)
	if err = db.SaveBackupRestorePlan(ctx, principal, otherPlan); err != nil {
		t.Fatal(err)
	}
	assertRestoreReplay("expiry and both cleanup paths")
	for _, attempt := range []struct {
		name, artifact, plan, confirmation, key string
	}{
		{"new key", artifactID, plan.ID, plan.Confirmation, "new-key-for-used-review"},
		{"changed confirmation", artifactID, plan.ID, "wrong", "active-artifact-restore"},
		{"different review", artifactID, otherPlan.ID, otherPlan.Confirmation, "active-artifact-restore"},
		{"different artifact", store.NewID(), plan.ID, plan.Confirmation, "active-artifact-restore"},
	} {
		status := client.request("POST", "/backup-artifacts/"+attempt.artifact+"/restore", map[string]string{"plan_id": attempt.plan, "confirmation": attempt.confirmation}, nil, attempt.key)
		if status < 400 {
			t.Fatal("accepted changed restore retry", attempt.name, status)
		}
	}
	otherActor := principal
	otherActor.ID = store.NewID()
	if _, err = db.AcceptBackupRestore(ctx, otherActor, artifactID, plan.ID, plan.Confirmation, "active-artifact-restore"); err == nil {
		t.Fatal("restore retry crossed actor identity")
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE backup_restore_plans SET expires_at=now()-interval '1 minute' WHERE id=$1", otherPlan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AcceptBackupRestore(ctx, principal, artifactID, otherPlan.ID, otherPlan.Confirmation, "expired-unused-review"); !errors.Is(err, backup.ErrConflict) {
		t.Fatal("unused expired review accepted a new execution", err)
	}
	if claimed, err := db.ClaimBackupArtifactDeletion(ctx, artifactID); err != nil || claimed {
		t.Fatal("active restore failed to protect artifact", err)
	}
	if _, err = db.CancelBackupJob(ctx, principal, restore.ID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := db.ClaimBackupArtifactDeletion(ctx, artifactID); err != nil || !claimed {
		t.Fatal("cancelled restore retained deletion fence", err)
	}
	assertRestoreReplay("artifact deletion pending")
	for i := 0; i < 4; i++ {
		id := store.NewID()
		if _, err = db.Pool.Exec(ctx, "INSERT INTO backup_artifacts(id,job_id,destination_id,source,object_key,sha256,bytes,format,scope,schedule_id,created_at) VALUES($1,$2,$3,$4,$5,$6,128,'transaction-fixture','metadata transaction test only',$7,$8)", id, store.NewID(), d.ID, store.JSON(source), backup.ObjectKey(d, id), strings.Repeat("0", 64), schedule.ID, time.Now().Add(-time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	expiredArtifacts, err := db.ExpiredBackupArtifacts(ctx, 16)
	if err != nil || len(expiredArtifacts) != 3 {
		t.Fatal("retention must keep two successful schedule artifacts and retry pending deletion", len(expiredArtifacts), err)
	}
	if err = db.MarkBackupArtifactDeleted(ctx, artifactID); err != nil {
		t.Fatal(err)
	}
	assertRestoreReplay("artifact deletion completed")
	if _, err = db.Pool.Exec(ctx, "UPDATE backup_jobs SET finished_at=now()-interval '91 days' WHERE id=$1", restore.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueueDueBackups(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM backup_restore_plans WHERE id=$1", plan.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("used review outlived bounded job retention", count, err)
	}
	if _, err = db.AcceptBackupRestore(ctx, principal, artifactID, plan.ID, plan.Confirmation, "active-artifact-restore"); err == nil {
		t.Fatal("pruned receipt created a new restore")
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", principal.KeyID); err != nil {
		t.Fatal(err)
	}
	// The already failed job cannot renew its lease, even with the old token.
	if cancel, err := db.HeartbeatBackupJob(ctx, j.ID, "worker-a"); err == nil && !cancel {
		t.Fatal("expired job retained execution authority")
	}
	t.Log("encrypted credentials, authorization, durable acceptance, one global job, crash failure, schedules, retention, deletion fencing and exact restore retries after expiry/deletion verified; accepted receipts pruned with 90-day job retention")
}
