package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/cluster"
	managed "github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveManagedDatabaseS3Recovery(t *testing.T) {
	if os.Getenv("HAKOPOD_MANAGED_DATABASE_BACKUP_TEST") != "1" {
		t.Skip("set HAKOPOD_MANAGED_DATABASE_BACKUP_TEST=1 with the named development kubeconfig and test PostgreSQL")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires k3d-hakopod-dev")
	}
	c, err := cluster.New(path, cluster.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	db, dsn := database(t)
	token, err := db.Bootstrap(ctx, "managed-backup-development-fixture")
	if err != nil {
		t.Fatal(err)
	}
	endpoint, access, secret, _ := liveBackupObjectStore(t, ctx)
	server := &api.Server{Store: db, Cluster: c, Auth: api.AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{19}, 32))}}
	state := t.TempDir()
	if err = os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	server.ConfigureBackups(api.BackupConfig{DatabaseURL: dsn, StateDir: state, MaxBytes: 16 << 20})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client := backupRequestClient{t, httpServer, token}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); server.RunManagedDatabases(runCtx) }()
	defer func() { stop(); <-done }()
	var destination struct {
		Destination backup.Destination `json:"destination"`
	}
	if status := client.request("POST", "/backup-destinations", backup.DestinationInput{Name: "managed-database-development-fixture", Endpoint: endpoint, Region: "us-east-1", Bucket: "hakopod-backup-tests", Prefix: "managed", PathStyle: true, AllowHTTP: true, AccessKeyID: access, SecretAccessKey: secret}, &destination, ""); status != 201 {
		t.Fatal("destination", status)
	}
	create := func(t *testing.T, engine, version, name string) managed.Resource {
		t.Helper()
		spec := managed.Spec{SchemaVersion: 1, Name: name + "-development-fixture", Engine: engine, Version: version, Mode: "standalone", Shards: 1, CPU: "100m", Memory: "256Mi", StorageGiB: 1}
		var operation managed.Operation
		if status := client.request("POST", "/databases", map[string]any{"project": "demo", "environment": "development", "spec": spec}, &operation, store.NewID()); status != 202 {
			t.Fatal("create database", status)
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			d, e := db.DatabaseInternal(cleanup, operation.DatabaseID)
			if e == nil {
				_, _ = c.DeleteDatabase(cleanup, d, func() error { return nil })
			}
		})
		for ctx.Err() == nil {
			d, e := db.DatabaseInternal(ctx, operation.DatabaseID)
			if e == nil && d.Status == "ready" {
				return d
			}
			if e == nil && d.Status == "failed" {
				t.Fatal("database provisioning failed:", d.Observation.Status, d.Observation.Message)
			}
			time.Sleep(time.Second)
		}
		t.Fatal("database readiness timeout")
		return managed.Resource{}
	}
	query := func(t *testing.T, d managed.Resource, sql string) string {
		t.Helper()
		o, err := c.ObserveDatabase(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		for _, m := range o.Members {
			if m.Name == o.Primary {
				if err = c.DatabaseExec(ctx, d, m, []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "app", "-c", sql}, nil, &out); err != nil {
					t.Fatal("fixture SQL", err)
				}
				return strings.TrimSpace(out.String())
			}
		}
		t.Fatal("no fixture primary")
		return ""
	}
	t.Run("postgresql-17-to-18", func(t *testing.T) {
		source := create(t, "postgresql", "17", "s3-source")
		target := create(t, "postgresql", "18", "s3-target")
		query(t, source, "CREATE TABLE recovered_rows(id integer PRIMARY KEY, value text); INSERT INTO recovered_rows VALUES(1,'captured')")
		var job backup.Job
		if status := client.request("POST", "/backups", map[string]any{"destination_id": destination.Destination.ID, "source": backup.Source{Kind: "managed_database", ManagedDatabaseID: source.ID, Engine: "postgresql"}}, &job, store.NewID()); status != 202 {
			t.Fatal("backup acceptance", status)
		}
		if err = server.Backups.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		job, err = db.BackupJob(ctx, job.ID)
		if err != nil || job.Status != "succeeded" {
			t.Fatal("backup", job.Status, job.Error, err)
		}
		archive, err := db.BackupArtifact(ctx, job.ArtifactID)
		if err != nil || archive.VerifiedAt == nil || archive.SourceVersion != "17" || archive.SourceRevision != 1 {
			t.Fatal("archive evidence", err)
		}
		query(t, source, "INSERT INTO recovered_rows VALUES(2,'after capture')")
		var plan backup.RestorePlan
		if status := client.request("POST", "/databases/"+target.ID+"/restore-plan", map[string]string{"artifact_id": archive.ID}, &plan, ""); status != 200 {
			t.Fatal("restore plan", status)
		}
		var recovery backup.Job
		if status := client.request("POST", "/backup-artifacts/"+archive.ID+"/restore", map[string]string{"plan_id": plan.ID, "confirmation": target.Spec.Name}, &recovery, store.NewID()); status != 202 {
			t.Fatal("restore acceptance", status)
		}
		if err = server.Backups.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		recovery, err = db.BackupJob(ctx, recovery.ID)
		if err != nil || recovery.Status != "succeeded" {
			t.Fatal("restore", recovery.Status, recovery.Error, err)
		}
		if err = db.RefreshDatabaseRecoveries(ctx); err != nil {
			t.Fatal(err)
		}
		if query(t, target, "SELECT count(*) FROM recovered_rows") != "1" || query(t, source, "SELECT count(*) FROM recovered_rows") != "2" {
			t.Fatal("recovery point/source preservation mismatch")
		}
		var inspected managed.Resource
		if status := client.request("POST", "/databases/"+target.ID+"/inspect", map[string]any{"job_id": recovery.ID, "expected_revision": 1, "confirm_name": target.Spec.Name, "inspected": true}, &inspected, ""); status != 200 || inspected.Recovery == nil || inspected.Recovery.InspectedAt == nil {
			t.Fatal("inspection", status)
		}
		t.Log("API creation, S3 encryption/readback verification, PG17-to-18 recovery review, durable restore, source preservation and inspection passed")
	})
}
