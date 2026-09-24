package api_test

import (
	"context"
	"encoding/base64"
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBackupWorkspaceIsolationAndWorkerRevocation(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "backup-scope")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('other'); INSERT INTO environments(project,name) VALUES('other','production'); UPDATE identities SET permissions=ARRAY['deployments:read','deployments:write']")
	if err != nil {
		t.Fatal(err)
	}
	application := func(project, environment, name string) store.Application {
		id := store.NewID()
		app := spec.Application{Name: name, Services: map[string]spec.Service{"db": {Image: "postgres:17", Env: map[string]string{"POSTGRES_DB": "app"}}}}
		if _, err = db.Pool.Exec(ctx, "INSERT INTO applications(id,project,environment,name,spec) VALUES($1,$2,$3,$4,$5)", id, project, environment, name, store.JSON(app)); err != nil {
			t.Fatal(err)
		}
		return store.Application{ID: id, Project: project, Environment: environment, Spec: app}
	}
	own := application("demo", "development", "own")
	foreign := application("other", "production", "foreign")
	server := &api.Server{Store: db, Auth: api.AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32))}}
	server.ConfigureBackups(api.BackupConfig{})
	server.Backups.Runtime = transactionBackupRuntime{}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	scoped := func(project, environment string) backupRequestClient {
		_, token, e := db.CreateKey(ctx, admin, store.KeyInput{Name: "scope", Project: project, Environment: environment, Permissions: []string{"deployments:read", "deployments:write"}, ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		return backupRequestClient{t: t, server: httpServer, token: token}
	}
	a := scoped(own.Project, own.Environment)
	b := scoped(foreign.Project, foreign.Environment)
	input := backup.DestinationInput{Name: "Customer bucket", Endpoint: "https://r2.example.com", Region: "auto", Bucket: "customer-backups", Prefix: "db", AccessKeyID: "fixture-access", SecretAccessKey: "fixture-secret"}
	var da, dbb struct {
		Destination backup.Destination `json:"destination"`
	}
	if code := a.request("POST", "/backup-destinations", input, &da, ""); code != 201 {
		t.Fatal("create scoped destination", code)
	}
	if code := b.request("POST", "/backup-destinations", input, &dbb, ""); code != 201 {
		t.Fatal(code)
	}
	var list struct {
		Items []backup.Destination `json:"items"`
	}
	if code := a.request("GET", "/backup-destinations", nil, &list, ""); code != 200 || len(list.Items) != 1 || list.Items[0].ID != da.Destination.ID {
		t.Fatal("foreign destination visible", code, list)
	}
	input.ExpectedRevision = 1
	if code := a.request("PUT", "/backup-destinations/"+dbb.Destination.ID, input, nil, ""); code != 404 {
		t.Fatal("foreign credentials rotated", code)
	}
	for _, route := range []struct {
		method, path string
		body         any
	}{{"POST", "/backup-destinations/" + dbb.Destination.ID + "/test", map[string]any{}}, {"DELETE", "/backup-destinations/" + dbb.Destination.ID, map[string]any{"expected_revision": 1}}} {
		if code := a.request(route.method, route.path, route.body, nil, ""); code != 404 {
			t.Fatal("foreign destination action", route.path, code)
		}
	}
	ownSource := backup.Source{Kind: "database", ApplicationID: own.ID, Service: "db", Engine: "postgresql", Database: "app"}
	otherSource := ownSource
	otherSource.ApplicationID = foreign.ID
	var job backup.Job
	for _, source := range []backup.Source{otherSource, {Kind: "management", Engine: "postgresql"}} {
		if code := a.request("POST", "/backups", map[string]any{"destination_id": da.Destination.ID, "source": source}, nil, store.NewID()); code != 404 {
			t.Fatal("foreign backup source", code)
		}
	}
	if code := a.request("POST", "/backups", map[string]any{"destination_id": dbb.Destination.ID, "source": ownSource}, nil, store.NewID()); code != 404 {
		t.Fatal("foreign destination accepted", code)
	}
	if code := a.request("POST", "/backups", map[string]any{"destination_id": da.Destination.ID, "source": ownSource}, &job, store.NewID()); code != 202 {
		t.Fatal("scoped backup", code)
	}
	for _, path := range []string{"/backups/" + job.ID, "/backups?cursor=" + job.ID} {
		if code := b.request("GET", path, nil, nil, ""); code != 404 {
			t.Fatal("foreign job visible", path, code)
		}
	}
	if code := b.request("POST", "/backups/"+job.ID+"/cancel", map[string]any{}, nil, ""); code != 404 {
		t.Fatal("foreign job cancelled", code)
	}
	claimed, e := db.ClaimBackupJob(ctx, "scoped-worker")
	if e != nil {
		t.Fatal(e)
	}
	if claimed.Authority == nil || claimed.Authority.Project != own.Project {
		t.Fatal("worker scope not persisted")
	}
	artifact := backup.Artifact{ID: store.NewID(), DestinationID: da.Destination.ID, Source: ownSource, ObjectKey: "test.age", SHA256: strings.Repeat("a", 64), Bytes: 100, Format: "postgresql-custom", Scope: "database"}
	if err = db.FinishBackupJob(ctx, claimed, "succeeded", "", &artifact); err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/backup-artifacts/" + artifact.ID, nil},
		{"GET", "/backup-artifacts?cursor=" + artifact.ID, nil},
		{"DELETE", "/backup-artifacts/" + artifact.ID, map[string]any{"confirmation": artifact.ID}},
		{"POST", "/backup-artifacts/" + artifact.ID + "/restore-plan", map[string]any{"application_id": foreign.ID, "service": "db"}},
	} {
		if code := b.request(route.method, route.path, route.body, nil, ""); code != 404 {
			t.Fatal("foreign artifact action", route.path, code)
		}
	}
	if code := a.request("POST", "/backup-artifacts/"+artifact.ID+"/restore-plan", map[string]any{"application_id": foreign.ID, "service": "db"}, nil, ""); code != 404 {
		t.Fatal("cross-workspace restore target", code)
	}
	var schedule backup.Schedule
	scheduleInput := map[string]any{"name": "Daily", "destination_id": da.Destination.ID, "source": ownSource, "interval_hours": 24, "retention_count": 3, "enabled": true}
	if code := a.request("POST", "/backup-schedules", scheduleInput, &schedule, ""); code != 201 {
		t.Fatal("scoped schedule", code)
	}
	for _, method := range []string{"PUT", "DELETE"} {
		body := map[string]any{"expected_revision": 1}
		if method == "PUT" {
			body = map[string]any{"name": "Stolen", "destination_id": dbb.Destination.ID, "source": otherSource, "interval_hours": 24, "retention_count": 3, "enabled": true, "expected_revision": 1}
		}
		if code := b.request(method, "/backup-schedules/"+schedule.ID, body, nil, ""); code != 404 {
			t.Fatal("foreign schedule mutation", method, code)
		}
	}
	var schedules struct {
		Items []backup.Schedule `json:"items"`
	}
	if code := b.request("GET", "/backup-schedules", nil, &schedules, ""); code != 200 || len(schedules.Items) != 0 {
		t.Fatal("foreign schedule visible", code)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE backup_schedules SET next_run_at=now()-interval '1 hour' WHERE id=$1", schedule.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueueDueBackups(ctx); err != nil {
		t.Fatal(err)
	}
	claimed, err = db.ClaimBackupJob(ctx, "scoped-scheduler")
	if err != nil || claimed.Authority == nil || claimed.Authority.Project != own.Project || claimed.ScheduleID != schedule.ID {
		t.Fatal("schedule did not preserve authority", claimed, err)
	}
	db.AuthorizeBackup = func(context.Context, string, string, string) error { return store.ErrForbidden }
	if cancelled, e := db.HeartbeatBackupJob(ctx, claimed.ID, claimed.Lease); !cancelled || e == nil {
		t.Fatal("revoked work continued")
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE backup_schedules SET next_run_at=now()-interval '1 hour' WHERE id=$1", schedule.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueueDueBackups(ctx); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	if err = db.Pool.QueryRow(ctx, "SELECT enabled FROM backup_schedules WHERE id=$1", schedule.ID).Scan(&enabled); err != nil || enabled {
		t.Fatal("revoked schedule still enabled", err)
	}
	if err = db.FinishBackupJob(ctx, claimed, "cancelled", "revoked", nil); err != nil {
		t.Fatal(err)
	}
	db.AuthorizeBackup = nil
	restoreApp := application(own.Project, own.Environment, "replacement")
	if _, err = db.Pool.Exec(ctx, "DELETE FROM applications WHERE id=$1", own.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE backup_schedules SET enabled=true,next_run_at=now()-interval '1 hour' WHERE id=$1", schedule.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueueDueBackups(ctx); err != nil {
		t.Fatal("deleted source stalled scheduling", err)
	}
	if err = db.Pool.QueryRow(ctx, "SELECT enabled FROM backup_schedules WHERE id=$1", schedule.ID).Scan(&enabled); err != nil || enabled {
		t.Fatal("deleted-source schedule still active", err)
	}
	var plan backup.RestorePlan
	if code := a.request("POST", "/backup-artifacts/"+artifact.ID+"/restore-plan", map[string]any{"application_id": restoreApp.ID, "service": "db"}, &plan, ""); code != 200 {
		t.Fatal("review deleted-source recovery", code)
	}
	if code := a.request("POST", "/backup-artifacts/"+artifact.ID+"/restore", map[string]any{"plan_id": plan.ID, "confirmation": plan.Confirmation}, &job, store.NewID()); code != 202 {
		t.Fatal("recover deleted source", code)
	}
	claimed, err = db.ClaimBackupJob(ctx, "scoped-restore")
	if err != nil {
		t.Fatal(err)
	}
	if stopped, err := db.HeartbeatBackupJob(ctx, claimed.ID, claimed.Lease); stopped || err != nil {
		t.Fatal("deleted source blocked scoped restore", err)
	}

}
