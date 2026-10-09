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
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/backup"
	managed "github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type backupRequestClient struct {
	t      *testing.T
	server *httptest.Server
	token  string
}

func TestBackupRequestClientReturnsOnlyBoundedProblemCode(t *testing.T) {
	t.Run("successful_empty_response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()
		status, code := (backupRequestClient{t: t, server: server}).requestCode("POST", "/backups", nil, nil, "")
		if status != http.StatusNoContent || code != "" {
			t.Fatal("successful empty response was classified as a problem")
		}
	})
	for _, test := range []struct {
		name, body, want string
	}{
		{"known", `{"error":{"code":"backup_target_unavailable","message":"private detail"}}`, "backup_target_unavailable"},
		{"invalid", `{"error":{"code":"private detail"}}`, "unavailable"},
		{"malformed", `{`, "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			status, code := (backupRequestClient{t: t, server: server}).requestCode("POST", "/backups", nil, nil, "")
			if status != http.StatusConflict || code != test.want {
				t.Fatal("problem code classification differs")
			}
		})
	}
}

func (c backupRequestClient) request(method, path string, input, output any, idem string) int {
	status, _ := c.requestCode(method, path, input, output, idem)
	return status
}

func (c backupRequestClient) requestCode(method, path string, input, output any, idem string) (int, string) {
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
	if response.StatusCode < 400 {
		if output != nil {
			if err = json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(output); err != nil {
				c.t.Fatal("decode backup API response", err)
			}
		}
		return response.StatusCode, ""
	}
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&failure); err != nil || !regexp.MustCompile(`^[a-z_]{1,64}$`).MatchString(failure.Error.Code) {
		return response.StatusCode, "unavailable"
	}
	return response.StatusCode, failure.Error.Code
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

type managedAdmissionRuntime struct {
	resolveCalls int
}

func (*managedAdmissionRuntime) Targets(context.Context) ([]backup.Target, error) {
	return nil, nil
}
func (r *managedAdmissionRuntime) Resolve(context.Context, backup.Source) (backup.Target, error) {
	r.resolveCalls++
	return backup.Target{}, errors.New("managed admission must not resolve live runtime state")
}
func (*managedAdmissionRuntime) Dump(context.Context, backup.Target, io.Writer) error {
	return errors.New("managed admission fixture never executes backups")
}
func (*managedAdmissionRuntime) Restore(context.Context, backup.Target, io.Reader) error {
	return errors.New("managed admission fixture never executes restores")
}

func TestManagedRestoreHTTPConflictPreservesReviewAndTarget(t *testing.T) {
	db, dsn := database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	raw, err := db.Bootstrap(ctx, "managed-restore-http-conflict")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	target := managed.Resource{ID: store.NewID(), Project: "demo", Environment: "development", Spec: managed.Spec{SchemaVersion: 1, Name: "restore-http-target", Engine: "oracle", Version: "23.26", Mode: "standalone", Shards: 1, CPU: "1", Memory: "4Gi", StorageGiB: 10, TLS: &managed.TLSConfig{Mode: "required"}, Oracle: &managed.OracleConfig{Edition: "free"}}, EncryptedCredentials: []byte("sealed-test-fixture")}
	if _, err = db.AcceptDatabase(ctx, principal, target, 0, "restore-http-target", "create"); err != nil {
		t.Fatal(err)
	}
	operation, err := db.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observation := managed.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now().UTC()}
	if err = db.RecordDatabaseStep(ctx, operation, observation, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	target, err = db.DatabaseInternal(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := db.PutBackupDestination(ctx, principal, backup.Destination{ID: store.NewID(), Name: "restore-http-destination", EncryptedCredentials: []byte("sealed-test-fixture"), EncryptionRecipient: "age-fixture"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	artifactID := store.NewID()
	source := backup.Source{Kind: "managed_database", ManagedDatabaseID: store.NewID(), Engine: "oracle"}
	capturedAt := time.Now().Add(-time.Minute).UTC()
	if _, err = db.Pool.Exec(ctx, "INSERT INTO backup_artifacts(id,job_id,destination_id,source,source_version,source_revision,object_key,sha256,bytes,format,scope,captured_at,verified_at) VALUES($1,$2,$3,$4,'23.26',1,$5,$6,128,'age-v1+oracle-datapump-v1','managed restore HTTP conflict fixture',$7,now())", artifactID, store.NewID(), destination.ID, store.JSON(source), backup.ObjectKey(destination, artifactID), strings.Repeat("0", 64), capturedAt); err != nil {
		t.Fatal(err)
	}
	management := &api.Server{Store: db, Auth: api.AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{17}, 32))}}
	management.ConfigureBackups(api.BackupConfig{DatabaseURL: dsn, StateDir: t.TempDir()})
	server := httptest.NewServer(management.Handler())
	defer server.Close()
	server.Client().Timeout = 30 * time.Second
	client := backupRequestClient{t: t, server: server, token: raw}
	plan := backup.RestorePlan{ID: store.NewID(), ArtifactID: artifactID, Target: backup.Target{Source: backup.Source{Kind: "managed_database", ManagedDatabaseID: target.ID, Engine: target.Spec.Engine}, ManagedDatabaseName: target.Spec.Name, Revision: target.Revision, Available: true}, Confirmation: target.Spec.Name, ExpiresAt: time.Now().Add(time.Minute)}
	if err = db.SaveBackupRestorePlan(ctx, principal, plan); err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: "restore-http-binding", Services: map[string]spec.Service{"api": {Image: "nginx:alpine", Bindings: map[string]spec.Binding{"DATABASE_URL": {ManagedDatabase: target.ID, Protocol: "oracle", Endpoint: "read_write"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := db.Accept(ctx, principal, target.Project, target.Environment, app, 0, "restore-http-bind")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", bound.ID); err != nil {
		t.Fatal(err)
	}
	before, err := db.DatabaseInternal(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status, code := client.requestCode("POST", "/backup-artifacts/"+artifactID+"/restore", map[string]string{"plan_id": plan.ID, "confirmation": plan.Confirmation}, nil, "restore-http-bound-refusal"); status != http.StatusConflict || code != "conflict" {
		t.Fatal("bound restore response", status, code)
	}
	after, err := db.DatabaseInternal(ctx, target.ID)
	if err != nil || after.Status != before.Status || after.Revision != before.Revision || !reflect.DeepEqual(after.Recovery, before.Recovery) {
		t.Fatal("denied restore changed target state", err)
	}
	var used *time.Time
	var jobID *string
	if err = db.Pool.QueryRow(ctx, "SELECT used_at,job_id FROM backup_restore_plans WHERE id=$1", plan.ID).Scan(&used, &jobID); err != nil || used != nil || jobID != nil {
		t.Fatal("denied restore consumed its review", used, jobID, err)
	}
	var jobs int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM backup_jobs WHERE idempotency_key=$1", "restore-http-bound-refusal").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("denied restore enqueued a job", jobs, err)
	}
	service := app.Services["api"]
	service.Bindings = nil
	app.Services["api"] = service
	unbound, err := db.Accept(ctx, principal, target.Project, target.Environment, app, 1, "restore-http-unbind")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", unbound.ID); err != nil {
		t.Fatal(err)
	}
	fresh := plan
	fresh.ID = store.NewID()
	if err = db.SaveBackupRestorePlan(ctx, principal, fresh); err != nil {
		t.Fatal("fresh restore plan", err)
	}
	var accepted backup.Job
	if status, code := client.requestCode("POST", "/backup-artifacts/"+artifactID+"/restore", map[string]string{"plan_id": fresh.ID, "confirmation": fresh.Confirmation}, &accepted, "restore-http-after-unbind"); status != http.StatusAccepted || code != "" || accepted.ID == "" {
		t.Fatal("unbound restore acceptance", status, code, accepted.ID)
	}
	recovering, err := db.DatabaseInternal(ctx, target.ID)
	if err != nil || recovering.Status != "restoring" || recovering.Recovery == nil || recovering.Recovery.ArtifactID != artifactID || recovering.Recovery.JobID != accepted.ID || recovering.Recovery.SourceID != source.ManagedDatabaseID || recovering.Recovery.SourceRevision != 1 {
		t.Fatal("accepted restore did not bind recovery state", recovering.Status, recovering.Recovery, err)
	}
}

func TestManagedBackupAdmissionUsesDurableState(t *testing.T) {
	db, dsn := database(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "managed-backup-admission")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('foreign'); INSERT INTO environments(project,name) VALUES('foreign','production')"); err != nil {
		t.Fatal(err)
	}
	_, token, err := db.CreateKey(ctx, admin, store.KeyInput{Name: "managed-backup-admission", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "deployments:write", "agent:credentials"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &managedAdmissionRuntime{}
	management := &api.Server{Store: db, Auth: api.AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{17}, 32))}}
	management.ConfigureBackups(api.BackupConfig{DatabaseURL: dsn, StateDir: t.TempDir()})
	management.Backups.Runtime = runtime
	server := httptest.NewServer(management.Handler())
	defer server.Close()
	client := backupRequestClient{t: t, server: server, token: token}
	var destination struct {
		Destination backup.Destination `json:"destination"`
	}
	input := backup.DestinationInput{Name: "managed admission", Endpoint: "https://storage.invalid", Region: "us-east-1", Bucket: "managed-admission", PathStyle: true, AccessKeyID: "test-access", SecretAccessKey: "test-secret"}
	if status := client.request("POST", "/backup-destinations", input, &destination, ""); status != http.StatusCreated {
		t.Fatal("destination", status)
	}
	insertDatabase := func(project, environment, status string) string {
		t.Helper()
		id := store.NewID()
		specification := managed.Spec{SchemaVersion: 1, Name: "db-" + id[:8], Engine: "postgresql", Version: "17", Mode: "standalone", Shards: 1, CPU: "100m", Memory: "256Mi", StorageGiB: 1}
		if _, insertErr := db.Pool.Exec(ctx, "INSERT INTO managed_databases(id,project,environment,name,revision,spec,status,credentials) VALUES($1,$2,$3,$4,1,$5,$6,$7)", id, project, environment, specification.Name, store.JSON(specification), status, []byte("fixture")); insertErr != nil {
			t.Fatal(insertErr)
		}
		return id
	}
	ready := insertDatabase("demo", "development", "ready")
	notReady := insertDatabase("demo", "development", "pending")
	foreign := insertDatabase("foreign", "production", "ready")
	request := func(id, engine, idem string, output any) (int, string) {
		return client.requestCode("POST", "/backups", map[string]any{"destination_id": destination.Destination.ID, "source": backup.Source{Kind: "managed_database", ManagedDatabaseID: id, Engine: engine}}, output, idem)
	}
	var accepted, replay backup.Job
	if status, code := request(ready, "postgresql", "managed-admission-ready", &accepted); status != http.StatusAccepted || code != "" {
		t.Fatal("ready managed database was not accepted", status, code)
	}
	if status, code := request(ready, "postgresql", "managed-admission-ready", &replay); status != http.StatusAccepted || code != "" || replay.ID != accepted.ID {
		t.Fatal("managed backup idempotency changed", status, code)
	}
	if status, _ := request(ready, "mysql", "managed-admission-engine", nil); status != http.StatusConflict {
		t.Fatal("wrong managed database engine was accepted", status)
	}
	if status, _ := request(notReady, "postgresql", "managed-admission-status", nil); status != http.StatusConflict {
		t.Fatal("non-ready managed database was accepted", status)
	}
	if status, _ := request(foreign, "postgresql", "managed-admission-foreign", nil); status != http.StatusNotFound {
		t.Fatal("foreign managed database was accepted", status)
	}
	if runtime.resolveCalls != 0 {
		t.Fatal("managed backup admission called the live runtime", runtime.resolveCalls)
	}
	if err = management.Backups.RunOnce(ctx); err != nil {
		t.Fatal("backup worker", err)
	}
	failed, err := db.BackupJob(ctx, accepted.ID)
	if err != nil || failed.Status != "failed" || runtime.resolveCalls != 1 {
		t.Fatal("backup worker did not recheck live runtime state", failed.Status, runtime.resolveCalls, err)
	}
	var schedule backup.Schedule
	scheduleRequest := map[string]any{"name": "managed admission", "destination_id": destination.Destination.ID, "source": backup.Source{Kind: "managed_database", ManagedDatabaseID: ready, Engine: "postgresql"}, "interval_hours": 24, "retention_count": 2, "enabled": true, "expected_revision": 0}
	if status := client.request("POST", "/backup-schedules", scheduleRequest, nil, ""); status != http.StatusForbidden {
		t.Fatal("schedule without durable identity authority was accepted", status)
	}
	var scheduleCount int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM backup_schedules").Scan(&scheduleCount); err != nil || scheduleCount != 0 {
		t.Fatal("rejected schedule changed durable state", scheduleCount, err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE identities SET permissions=ARRAY['deployments:read','deployments:write'] WHERE id=$1", admin.ID); err != nil {
		t.Fatal(err)
	}
	if status := client.request("POST", "/backup-schedules", scheduleRequest, &schedule, ""); status != http.StatusCreated || schedule.ID == "" {
		t.Fatal("managed backup schedule was not accepted", status)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE backup_schedules SET next_run_at=now()-interval '1 second' WHERE id=$1", schedule.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueueDueBackups(ctx); err != nil {
		t.Fatal("member-backed schedule did not survive keyless worker reauthorization", err)
	}
	var enabled bool
	var scheduledJobs int
	if err = db.Pool.QueryRow(ctx, "SELECT enabled,(SELECT count(*) FROM backup_jobs WHERE schedule_id=$1) FROM backup_schedules WHERE id=$1", schedule.ID).Scan(&enabled, &scheduledJobs); err != nil || !enabled || scheduledJobs != 1 {
		t.Fatal("member-backed schedule was disabled or did not enqueue", enabled, scheduledJobs, err)
	}
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
	_, credentialToken, err := db.CreateKey(ctx, principal, store.KeyInput{Name: "Backup credential fixture", Permissions: []string{"admin", "agent:admin", "agent:credentials"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	client := backupRequestClient{t, server, credentialToken}
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
	var scopedDestinations struct {
		Items []backup.Destination `json:"items"`
	}
	if status := unauthorized.request("GET", "/backup-destinations", nil, &scopedDestinations, ""); status != 200 || len(scopedDestinations.Items) != 0 {
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
	targetSpec, err := spec.Normalize(spec.Application{Name: "restore-target", Services: map[string]spec.Service{"db": {Image: "postgres:17"}}})
	if err != nil {
		t.Fatal(err)
	}
	targetDeployment, err := db.Accept(ctx, principal, "demo", "development", targetSpec, 0, "restore-target-fixture")
	if err != nil {
		t.Fatal(err)
	}
	plan := backup.RestorePlan{ID: store.NewID(), ArtifactID: artifactID, Target: backup.Target{Source: backup.Source{Kind: "database", ApplicationID: targetDeployment.ApplicationID, Service: "db", Engine: "postgresql", Database: "hp_restore_12345678901234567890"}, Revision: targetDeployment.Revision}, Confirmation: "hp_restore_12345678901234567890", ExpiresAt: time.Now().Add(time.Minute)}
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
