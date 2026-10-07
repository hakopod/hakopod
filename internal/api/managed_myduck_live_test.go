package api_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	mysqlclient "github.com/go-sql-driver/mysql"
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/cluster"
	managed "github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func myduckAPIPortForward(t *testing.T, ctx context.Context, kubeconfig, namespace string, port int) (string, func()) {
	t.Helper()
	forward, stop := context.WithCancel(ctx)
	command := exec.CommandContext(forward, "kubectl", "--kubeconfig", kubeconfig, "--context", "k3d-hakopod-dev", "--cache-dir", t.TempDir(), "-n", namespace, "port-forward", "service/database", fmt.Sprintf(":%d", port), "--address=127.0.0.1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		stop()
		t.Fatal(err)
	}
	command.Stderr = io.Discard
	if err = command.Start(); err != nil {
		stop()
		t.Fatal(err)
	}
	line := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			line <- scanner.Text()
		} else {
			line <- ""
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	var address string
	select {
	case value := <-line:
		fields := strings.Fields(value)
		if len(fields) >= 3 && strings.HasPrefix(value, "Forwarding from ") {
			address = fields[2]
		}
	case <-time.After(20 * time.Second):
	}
	if address == "" {
		stop()
		_ = command.Wait()
		t.Fatal("MyDuck port forwarding did not start")
	}
	return address, func() { stop(); _ = command.Wait() }
}

func myduckAPITLS(t *testing.T, trust managed.PublicTrust, host string) *tls.Config {
	t.Helper()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(trust.CertificatePEM)) {
		t.Fatal("MyDuck trust is invalid")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: host}
}

func TestManagedMyDuckHTTPLive(t *testing.T) {
	if os.Getenv("HAKOPOD_MYDUCK_API_RECOVERY_TEST") != "1" {
		t.Skip("set HAKOPOD_MYDUCK_API_RECOVERY_TEST=1 only in the qualified development-cluster runner")
	}
	kubeconfig := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires k3d-hakopod-dev")
	}
	nodes := strings.FieldsFunc(os.Getenv("HAKOPOD_DATABASE_FIXTURE_NODES"), func(r rune) bool { return r == ',' || r == ' ' })
	if len(nodes) == 0 {
		t.Fatal("requires HAKOPOD_DATABASE_FIXTURE_NODES")
	}
	rest, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	kube, err := kubernetes.NewForConfig(rest)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := cluster.New(kubeconfig, cluster.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	db, dsn := database(t)
	token, err := db.Bootstrap(ctx, "myduck-api-recovery")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('myduck-foreign'); INSERT INTO environments(project,name) VALUES('myduck-foreign','development')"); err != nil {
		t.Fatal(err)
	}
	_, scopedToken, err := db.CreateKey(ctx, principal, store.KeyInput{Name: "myduck-api-scope", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "deployments:write"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	foreignID := store.NewID()
	foreignSpec := managed.Spec{SchemaVersion: 1, Name: "myduck-foreign", Engine: "duckdb", Version: managed.MyDuckVersion, Mode: "standalone", Shards: 1, CPU: "500m", Memory: "512Mi", StorageGiB: 1, TLS: &managed.TLSConfig{Mode: "required"}}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO managed_databases(id,project,environment,name,revision,spec,status,credentials) VALUES($1,'myduck-foreign','development',$2,1,$3,'ready',$4)", foreignID, foreignSpec.Name, store.JSON(foreignSpec), []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	endpoint, access, secret, _ := liveBackupObjectStore(t, ctx)
	server := &api.Server{Store: db, Cluster: runtime, Auth: api.AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{29}, 32))}}
	server.ConfigureBackups(api.BackupConfig{DatabaseURL: dsn, StateDir: t.TempDir(), MaxBytes: 64 << 20})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client := backupRequestClient{t: t, server: httpServer, token: token}
	var destination struct {
		Destination backup.Destination `json:"destination"`
	}
	if status := client.request("POST", "/backup-destinations", backup.DestinationInput{Name: "myduck-api-recovery", Endpoint: endpoint, Region: "us-east-1", Bucket: "hakopod-backup-tests", Prefix: "myduck-api", PathStyle: true, AllowHTTP: true, AccessKeyID: access, SecretAccessKey: secret}, &destination, ""); status != 201 {
		t.Fatal("destination", status)
	}
	if !t.Run("authorization", func(t *testing.T) {
		if status := (backupRequestClient{t: t, server: httpServer}).request("GET", "/databases?project=demo&environment=development", nil, nil, ""); status != http.StatusUnauthorized {
			t.Fatal("missing token was not refused", status)
		}
		scoped := backupRequestClient{t: t, server: httpServer, token: scopedToken}
		if status := scoped.request("POST", "/databases", map[string]any{"project": "myduck-foreign", "environment": "development", "spec": foreignSpec}, nil, store.NewID()); status != http.StatusForbidden {
			t.Fatal("scoped key created a cross-project database", status)
		}
		if status := scoped.request("POST", "/databases/"+foreignID+"/credentials", map[string]any{}, nil, ""); status != http.StatusNotFound {
			t.Fatal("scoped key accessed cross-project credentials", status)
		}
		if status := scoped.request("POST", "/backups", map[string]any{"destination_id": destination.Destination.ID, "source": backup.Source{Kind: "managed_database", ManagedDatabaseID: foreignID, Engine: "duckdb"}}, nil, store.NewID()); status != http.StatusNotFound {
			t.Fatal("scoped key accepted a cross-project backup", status)
		}
		if status := client.request("GET", "/databases?project=demo&environment=development", nil, &struct {
			Items []managed.Resource `json:"items"`
		}{}, ""); status != 200 {
			t.Fatal("authorized database catalog", status)
		}
	}) {
		t.FailNow()
	}
	runCtx, stopWorkers := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); server.RunManagedDatabases(runCtx) }()
	defer func() { stopWorkers(); <-done }()
	created := []managed.Resource{}
	defer func() {
		for _, item := range created {
			clean, stop := context.WithTimeout(context.Background(), 3*time.Minute)
			for clean.Err() == nil {
				current, e := db.DatabaseInternal(clean, item.ID)
				if e != nil {
					break
				}
				complete, e := runtime.DeleteDatabase(clean, current, func() error { return clean.Err() })
				if e != nil || complete {
					break
				}
				time.Sleep(time.Second)
			}
			stop()
		}
	}()
	create := func(name string) managed.Resource {
		spec := managed.Spec{SchemaVersion: 1, Name: name, Engine: "duckdb", Version: managed.MyDuckVersion, Mode: "standalone", Shards: 1, CPU: "500m", Memory: "512Mi", StorageGiB: 1, Placement: managed.Placement{NodeNames: append([]string(nil), nodes...)}, TLS: &managed.TLSConfig{Mode: "required"}}
		var operation managed.Operation
		if status := client.request("POST", "/databases", map[string]any{"project": "demo", "environment": "development", "spec": spec}, &operation, store.NewID()); status != 202 {
			t.Fatal("create MyDuck", status)
		}
		created = append(created, managed.Resource{ID: operation.DatabaseID, Spec: spec})
		t.Logf("Development MyDuck namespace %s", cluster.DatabaseNamespace(operation.DatabaseID))
		for ctx.Err() == nil {
			current, e := db.DatabaseInternal(ctx, operation.DatabaseID)
			if e == nil && current.Status == "ready" && current.Observation.Status == "ready" && current.Observation.TLS != nil && current.Observation.TLS.Verified {
				created[len(created)-1] = current
				return current
			}
			if e == nil && current.Status == "failed" {
				t.Fatal("MyDuck creation failed", current.Observation.Message)
			}
			time.Sleep(time.Second)
		}
		t.Fatal("MyDuck creation timed out")
		return managed.Resource{}
	}
	source, target := create("myduck-api-source"), create("myduck-api-target")
	t.Run("lifecycle", func(t *testing.T) {
		if source.Status != "ready" || target.Status != "ready" || len(source.Observation.Members) != 1 || len(target.Observation.Members) != 1 {
			t.Fatal("MyDuck lifecycle did not produce two ready standalone databases")
		}
	})
	query := func(d managed.Resource, protocol, statement string) string {
		var credentials map[string]string
		if status := client.request("POST", "/databases/"+d.ID+"/credentials", map[string]any{}, &credentials, ""); status != 200 {
			t.Fatal("credentials", status)
		}
		if credentials["username"] != "root" || credentials["database"] != "app" || credentials["password"] == "" {
			t.Fatal("MyDuck credentials returned unexpected defaults")
		}
		trust, e := runtime.DatabaseTrust(ctx, d)
		if e != nil {
			t.Fatal(e)
		}
		host := "database." + cluster.DatabaseNamespace(d.ID) + ".svc"
		port := 3306
		if protocol == "postgresql" {
			port = 5432
		}
		address, closeForward := myduckAPIPortForward(t, ctx, kubeconfig, cluster.DatabaseNamespace(d.ID), port)
		defer closeForward()
		if protocol == "mysql" {
			settings := mysqlclient.NewConfig()
			settings.User, settings.Passwd, settings.Net, settings.Addr, settings.DBName, settings.TLS, settings.MultiStatements = credentials["username"], credentials["password"], "tcp", address, credentials["database"], myduckAPITLS(t, trust, host), true
			connector, e := mysqlclient.NewConnector(settings)
			if e != nil {
				t.Fatal(e)
			}
			sqlDB := sql.OpenDB(connector)
			defer sqlDB.Close()
			if strings.HasPrefix(statement, "CREATE ") || strings.HasPrefix(statement, "INSERT ") {
				if _, e = sqlDB.ExecContext(ctx, statement); e != nil {
					t.Fatal(e)
				}
				return ""
			}
			var value string
			if e = sqlDB.QueryRowContext(ctx, statement).Scan(&value); e != nil {
				t.Fatal(e)
			}
			return value
		}
		settings, e := pgx.ParseConfig("host=" + host + " port=5432 user=postgres dbname=app connect_timeout=5")
		if e != nil {
			t.Fatal(e)
		}
		settings.Password, settings.TLSConfig = credentials["password"], myduckAPITLS(t, trust, host)
		settings.Fallbacks = nil
		settings.DialFunc = func(step context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(step, "tcp", address)
		}
		// The port-forward fixes the connection target; TLS still verifies host.
		settings.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
		connection, e := pgx.ConnectConfig(ctx, settings)
		if e != nil {
			t.Fatal(e)
		}
		defer connection.Close(ctx)
		var value string
		if e = connection.QueryRow(ctx, statement).Scan(&value); e != nil {
			t.Fatal(e)
		}
		return value
	}
	query(source, "mysql", "CREATE TABLE recovery_rows(id INTEGER PRIMARY KEY, value VARCHAR(255)); INSERT INTO recovery_rows VALUES(1,'captured')")
	if query(source, "postgresql", "SELECT value FROM recovery_rows WHERE id=1") != "captured" {
		t.Fatal("dual-protocol read differs")
	}
	var interrupted backup.Job
	if status := client.request("POST", "/backups", map[string]any{"destination_id": destination.Destination.ID, "source": backup.Source{Kind: "managed_database", ManagedDatabaseID: source.ID, Engine: "duckdb"}}, &interrupted, store.NewID()); status != 202 {
		t.Fatal("interrupted backup acceptance", status)
	}
	claimed, err := db.ClaimBackupJob(ctx, "myduck-interrupted-worker")
	if err != nil || claimed.ID != interrupted.ID {
		t.Fatal("claim interrupted backup", err)
	}
	observed, err := runtime.ObserveDatabase(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AcquireDatabaseColdStorageFence(ctx, source.ID, source.Revision, claimed.ID, claimed.Lease, claimed.Kind); err != nil {
		t.Fatal(err)
	}
	checks := 0
	interruptedErr := runtime.WithMyDuckColdStorage(ctx, source, observed, claimed.ID, false, func() error {
		checks++
		if checks == 1 {
			return db.CheckDatabaseColdStorageWorker(ctx, source.ID, source.Revision, claimed.ID, claimed.Lease, claimed.Kind)
		}
		_, _ = db.Pool.Exec(ctx, `UPDATE backup_jobs SET cancel_requested=true,lease_until=clock_timestamp()-interval '1 second' WHERE id=$1 AND lease=$2`, claimed.ID, claimed.Lease)
		return store.ErrClaimLost
	}, nil, io.Discard)
	if interruptedErr == nil || checks < 2 {
		t.Fatal("worker loss did not interrupt cold capture after fencing")
	}
	if err = server.Backups.RunOnce(ctx); err != nil {
		t.Fatal("reclaimed cold cleanup", err)
	}
	interrupted, err = db.BackupJob(ctx, interrupted.ID)
	if err != nil || interrupted.Status != "cancelled" {
		t.Fatal("reclaimed cold job did not end cancelled", interrupted.Status, err)
	}
	var fenced bool
	if err = db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_database_cold_storage_fences WHERE database_id=$1)`, source.ID).Scan(&fenced); err != nil || fenced {
		t.Fatal("reclaimed cold fence was not cleared", err)
	}
	t.Run("worker_loss", func(t *testing.T) {
		if checks < 2 || fenced {
			t.Fatal("worker loss did not preserve and reclaim the durable fence")
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		if interrupted.Status != "cancelled" {
			t.Fatal("interrupted cold backup was not cancelled after cleanup")
		}
	})
	for ctx.Err() == nil {
		source, err = db.DatabaseInternal(ctx, source.ID)
		if err == nil && source.Status == "ready" && source.Observation.Status == "ready" {
			break
		}
		time.Sleep(time.Second)
	}
	var job backup.Job
	if status := client.request("POST", "/backups", map[string]any{"destination_id": destination.Destination.ID, "source": backup.Source{Kind: "managed_database", ManagedDatabaseID: source.ID, Engine: "duckdb"}}, &job, store.NewID()); status != 202 {
		t.Fatal("backup acceptance", status)
	}
	if err = server.Backups.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	job, err = db.BackupJob(ctx, job.ID)
	if err != nil || job.Status != "succeeded" {
		t.Fatal("backup", job.Status, job.Error, err)
	}
	artifact, err := db.BackupArtifact(ctx, job.ArtifactID)
	if err != nil || artifact.Format != "age-v1+myduck-cold-v1" || artifact.VerifiedAt == nil {
		t.Fatal("cold archive evidence", err)
	}
	query(source, "mysql", "INSERT INTO recovery_rows VALUES(2,'after')")
	var plan backup.RestorePlan
	if status := client.request("POST", "/databases/"+target.ID+"/restore-plan", map[string]string{"artifact_id": artifact.ID}, &plan, ""); status != 200 {
		t.Fatal("restore plan", status)
	}
	var recovery backup.Job
	if status := client.request("POST", "/backup-artifacts/"+artifact.ID+"/restore", map[string]string{"plan_id": plan.ID, "confirmation": target.Spec.Name}, &recovery, store.NewID()); status != 202 {
		t.Fatal("restore acceptance", status)
	}
	if err = server.Backups.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	recovery, err = db.BackupJob(ctx, recovery.ID)
	if err != nil || recovery.Status != "succeeded" {
		t.Fatal("restore", recovery.Status, recovery.Error, err)
	}
	if query(target, "postgresql", "SELECT CAST(count(*) AS VARCHAR) FROM recovery_rows") != "1" || query(source, "mysql", "SELECT CAST(count(*) AS CHAR) FROM recovery_rows") != "2" {
		t.Fatal("recovery point or source changed")
	}
	t.Run("backup_restore", func(t *testing.T) {
		if job.Status != "succeeded" || recovery.Status != "succeeded" || artifact.VerifiedAt == nil {
			t.Fatal("encrypted cold backup and restore did not complete")
		}
	})
	var inspected managed.Resource
	if status := client.request("POST", "/databases/"+target.ID+"/inspect", map[string]any{"job_id": recovery.ID, "expected_revision": target.Revision, "confirm_name": target.Spec.Name, "inspected": true}, &inspected, ""); status != 200 || inspected.Recovery == nil || inspected.Recovery.InspectedAt == nil {
		t.Fatal("inspection", status)
	}
	t.Run("deletion", func(t *testing.T) {
		var operation managed.Operation
		if status := client.request("DELETE", "/databases/"+target.ID, map[string]any{"expected_revision": inspected.Revision, "confirm_name": inspected.Spec.Name}, &operation, store.NewID()); status != 202 || operation.Kind != "delete" {
			t.Fatal("delete acceptance", status)
		}
		for ctx.Err() == nil {
			if status := client.request("GET", "/database-operations/"+operation.ID, nil, &operation, ""); status != http.StatusOK {
				t.Fatal("delete operation", status)
			}
			if operation.Status == "succeeded" {
				break
			}
			if operation.Status == "failed" || operation.Status == "cancelled" {
				t.Fatal("delete operation did not succeed", operation.Status, operation.Message)
			}
			time.Sleep(time.Second)
		}
		if operation.Status != "succeeded" {
			t.Fatal("delete operation timed out")
		}
		namespace := cluster.DatabaseNamespace(target.ID)
		for ctx.Err() == nil {
			_, namespaceErr := kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
			claims, claimErr := kube.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
			namespaceGone := apierrors.IsNotFound(namespaceErr)
			claimsGone := apierrors.IsNotFound(claimErr) || claimErr == nil && len(claims.Items) == 0
			if namespaceGone && claimsGone {
				return
			}
			if namespaceErr != nil && !namespaceGone || claimErr != nil && !apierrors.IsNotFound(claimErr) {
				t.Fatal("inspect deleted MyDuck resources", namespaceErr, claimErr)
			}
			time.Sleep(time.Second)
		}
		t.Fatal("deleted MyDuck namespace or persistent volume claims remain")
	})
}
