//go:build hakopod_native_acceptance && linux

package vitess_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/cluster"
	managed "github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/hakopod/hakopod/internal/worker"
	"github.com/jackc/pgx/v5"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	project     = "demo"
	environment = "development"
)

type storageFixture struct {
	DatabaseID            string             `json:"database_id"`
	Dedicated             bool               `json:"dedicated"`
	Destination           backup.Destination `json:"destination"`
	Credentials           backup.Credentials `json:"credentials"`
	ApprovedEndpointCIDRs []netip.Prefix     `json:"approved_endpoint_cidrs"`
}

type httpClient struct {
	t      *testing.T
	ctx    context.Context
	server *httptest.Server
	token  string
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	if len(value) > b.limit-b.Len() {
		return 0, fmt.Errorf("output exceeds %d bytes", b.limit)
	}
	return b.Buffer.Write(value)
}

func (c httpClient) request(method, path string, input, output any, idempotency string) (int, string) {
	c.t.Helper()
	var body io.Reader
	if input != nil {
		body = bytes.NewReader(store.JSON(input))
	}
	request, err := http.NewRequestWithContext(c.ctx, method, c.server.URL+"/api/v1"+path, body)
	if err != nil {
		c.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	if idempotency != "" {
		request.Header.Set("Idempotency-Key", idempotency)
	}
	response, err := c.server.Client().Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 400 {
		if output != nil {
			if err = json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(output); err != nil {
				c.t.Fatal("decode API response", err)
			}
		}
		return response.StatusCode, ""
	}
	var problem struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&problem) != nil || !regexp.MustCompile(`^[a-z_]{1,64}$`).MatchString(problem.Error.Code) {
		return response.StatusCode, "unavailable"
	}
	return response.StatusCode, problem.Error.Code
}

func disposableStore(t *testing.T, ctx context.Context) (*store.Store, string) {
	t.Helper()
	dsn := os.Getenv("HAKOPOD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("HAKOPOD_TEST_DATABASE_URL is required")
	}
	connection, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "hakopod_vitess_http_" + store.NewID()
	if _, err = connection.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		connection.Close(ctx)
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	testDSN := parsed.String()
	database, err := store.Open(ctx, testDSN)
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		_, _ = connection.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		connection.Close(context.Background())
	})
	return database, testDSN
}

func loadFixtures(t *testing.T) map[string]storageFixture {
	t.Helper()
	path := os.Getenv("HAKOPOD_VITESS_NATIVE_FIXTURE_CONFIG")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal("protected Vitess fixture is required")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
		t.Fatal("Vitess fixture must be a protected bounded regular file")
	}
	var config struct {
		Fixtures map[string]storageFixture `json:"fixtures"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, (64<<10)+1))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatal("Vitess fixture is invalid")
	}
	result := map[string]storageFixture{}
	for _, name := range []string{"standalone", "recovery-source", "recovery-target"} {
		fixture, ok := config.Fixtures[name]
		if !ok || !fixture.Dedicated || fixture.Destination.ID == "" || len(fixture.ApprovedEndpointCIDRs) == 0 {
			t.Fatalf("Vitess fixture %q is incomplete", name)
		}
		fixture.Destination.Prefix += "/http-" + store.NewID()[:16]
		result[name] = fixture
	}
	return result
}

func waitOperation(t *testing.T, ctx context.Context, client httpClient, operation managed.Operation) managed.Resource {
	t.Helper()
	for ctx.Err() == nil {
		var current managed.Operation
		status, _ := client.request("GET", "/database-operations/"+operation.ID, nil, &current, "")
		if status != http.StatusOK {
			t.Fatal("database operation lookup", status)
		}
		if current.Status == "failed" || current.Status == "cancelled" {
			t.Fatal("database operation failed", current.Kind, current.Phase)
		}
		if current.Status == "succeeded" {
			if current.Kind == "delete" {
				return managed.Resource{}
			}
			var database managed.Resource
			status, _ = client.request("GET", "/databases/"+operation.DatabaseID, nil, &database, "")
			if status != http.StatusOK || database.Status != "ready" || !database.Observation.Fresh(time.Now(), database.Revision) || database.Observation.TLS == nil || !database.Observation.TLS.Verified || len(database.Observation.Members) != 4 || database.Observation.Routing == nil || !database.Observation.Routing.Ready || len(database.Observation.Routing.Members) != 2 {
				t.Fatal("database lookup", status)
			}
			return database
		}
		time.Sleep(time.Second)
	}
	t.Fatal("database operation timed out")
	return managed.Resource{}
}

func waitDeployment(t *testing.T, ctx context.Context, client httpClient, deployment store.Deployment) store.Deployment {
	t.Helper()
	for ctx.Err() == nil {
		var current store.Deployment
		status, _ := client.request("GET", "/deployments/"+deployment.ID, nil, &current, "")
		if status != http.StatusOK {
			t.Fatal("deployment lookup", status)
		}
		if current.Status == "succeeded" {
			return current
		}
		if current.Status != "queued" && current.Status != "running" {
			t.Fatal("deployment failed", current.Status, current.Error)
		}
		time.Sleep(time.Second)
	}
	t.Fatal("deployment timed out")
	return store.Deployment{}
}

func vitessQuery(t *testing.T, ctx context.Context, runtime *cluster.Client, database managed.Resource, password, trust, query string) string {
	t.Helper()
	if database.Observation.Routing == nil || len(database.Observation.Routing.Members) == 0 {
		t.Fatal("Vitess gateway is unavailable")
	}
	host := ""
	for _, endpoint := range database.Observation.Endpoints {
		if endpoint.Purpose == "read_write" && endpoint.Port == 3306 {
			host = endpoint.Host
		}
	}
	if host == "" || trust == "" {
		t.Fatal("verified Vitess endpoint is unavailable")
	}
	const script = `set -eu
IFS= read -r MYSQL_PWD
export MYSQL_PWD
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cat > "$work/ca.crt"
exec mysql --no-defaults --protocol=TCP --host="$1" --port=3306 --user=app --database=app --connect-timeout=5 --ssl-mode=VERIFY_IDENTITY --ssl-ca="$work/ca.crt" --batch --raw --skip-column-names --binary-mode=1 --local-infile=0 --execute="$2"`
	output := &boundedBuffer{limit: 16 << 10}
	if err := runtime.DatabaseExec(ctx, database, database.Observation.Routing.Members[0], []string{"sh", "-c", script, "vitess-http-query", host, query}, strings.NewReader(password+"\n"+trust), output); err != nil {
		t.Fatal("Vitess application query failed", err)
	}
	return strings.TrimSpace(output.String())
}

func TestVitessHTTPVerticalSlice(t *testing.T) {
	if os.Getenv("HAKOPOD_VITESS_HTTP_ACCEPTANCE_TEST") != "1" {
		t.Skip("set HAKOPOD_VITESS_HTTP_ACCEPTANCE_TEST=1 on the named development VM")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()
	kubeconfig := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("Vitess HTTP acceptance requires k3d-hakopod-dev")
	}
	fixtures := loadFixtures(t)
	nodes := []string{"k3d-hakopod-vitess-worker-0", "k3d-hakopod-vitess-worker-1", "k3d-hakopod-vitess-worker-2"}
	pool, storageClass := os.Getenv("HAKOPOD_VITESS_ACCEPTANCE_POOL"), os.Getenv("HAKOPOD_VITESS_ACCEPTANCE_STORAGE_CLASS")
	if pool != "vitess-acceptance" || storageClass != "local-path" {
		t.Fatal("trusted Vitess acceptance placement policy is required")
	}
	runtime, err := cluster.New(kubeconfig, cluster.Options{
		DatabasePolicy: func(_ context.Context, selectedProject, selectedEnvironment string, _ managed.Spec) (cluster.DatabasePolicy, error) {
			if selectedProject != project || selectedEnvironment != environment {
				return cluster.DatabasePolicy{}, fmt.Errorf("unexpected database placement scope")
			}
			return cluster.DatabasePolicy{NodeNames: append([]string(nil), nodes...), Pool: pool, RuntimeClass: "runsc", StorageClass: storageClass}, nil
		},
		WorkloadPolicy: func(_ context.Context, selectedProject, selectedEnvironment string, _ spec.Application) (cluster.WorkloadPolicy, error) {
			if selectedProject != project || selectedEnvironment != environment {
				return cluster.WorkloadPolicy{}, fmt.Errorf("unexpected application placement scope")
			}
			return cluster.WorkloadPolicy{NodeName: nodes[0], Pool: pool, RuntimeClass: "runsc", Recreate: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	database, dsn := disposableStore(t, ctx)
	rootToken, err := database.Bootstrap(ctx, "vitess-http-acceptance")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := database.Authenticate(ctx, rootToken)
	if err != nil {
		t.Fatal(err)
	}
	authKey := bytes.Repeat([]byte{41}, 32)
	approvals := make([]backup.VitessBackupApproval, 0, 2)
	for _, binding := range []struct{ fixture, database string }{{"standalone", ""}, {"recovery-source", "vitess-http-source"}, {"recovery-target", "vitess-http-target"}} {
		name, databaseName := binding.fixture, binding.database
		fixture := fixtures[name]
		identity, recipient, identityErr := backup.NewEncryptionIdentity("")
		if identityErr != nil {
			t.Fatal(identityErr)
		}
		fixture.Credentials.EncryptionIdentity = identity
		fixture.Destination.EncryptionRecipient = recipient
		fixture.Destination.EncryptedCredentials, err = backup.SealCredentials(authKey, fixture.Destination.ID, fixture.Credentials)
		if err != nil {
			t.Fatal(err)
		}
		principal := owner
		principal.Project, principal.Environment, principal.CanManageDatabaseBackups = project, environment, true
		fixture.Destination.Project, fixture.Destination.Environment = project, environment
		if _, err = database.PutBackupDestination(ctx, principal, fixture.Destination, 0); err != nil {
			t.Fatal("store protected backup destination", err)
		}
		if databaseName != "" {
			cidrs := make([]string, len(fixture.ApprovedEndpointCIDRs))
			for index, prefix := range fixture.ApprovedEndpointCIDRs {
				cidrs[index] = prefix.String()
			}
			approvals = append(approvals, backup.VitessBackupApproval{DestinationID: fixture.Destination.ID, Revision: 1, Project: project, Environment: environment, Database: databaseName, DedicatedCredentials: true, EndpointCIDRs: cidrs})
		}
		fixtures[name] = fixture
	}
	server := &api.Server{Store: database, Cluster: runtime, Auth: api.AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(authKey)}}
	if err = server.ConfigureBackups(api.BackupConfig{VitessApprovals: approvals, DatabaseURL: dsn, StateDir: t.TempDir(), MaxBytes: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	server.ConfigureDatabaseBindings()
	httpServer := httptest.NewServer(server.Handler())
	httpServer.Client().Timeout = 30 * time.Second
	defer httpServer.Close()
	_, scopedToken, err := database.CreateKey(ctx, owner, store.KeyInput{Name: "vitess-http", Project: project, Environment: environment, Permissions: []string{"deployments:read", "deployments:write", "logs:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	administrator := httpClient{t: t, ctx: ctx, server: httpServer, token: rootToken}
	if status, body := administrator.request(http.MethodPost, "/projects/"+project+"/environments", map[string]string{"name": "production"}, nil, ""); status != http.StatusCreated {
		t.Fatalf("create wrong-scope environment: status=%d body=%s", status, body)
	}
	_, wrongToken, err := database.CreateKey(ctx, owner, store.KeyInput{Name: "vitess-http-wrong-scope", Project: project, Environment: "production", Permissions: []string{"deployments:read", "deployments:write"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	client := httpClient{t: t, ctx: ctx, server: httpServer, token: scopedToken}
	workers, stopWorkers := context.WithCancel(ctx)
	databaseDone, applicationDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(databaseDone); server.RunManagedDatabases(workers) }()
	go func() {
		defer close(applicationDone)
		(&worker.Worker{Store: database, Cluster: runtime, Concurrency: 1, Timeout: 10 * time.Minute}).Run(workers)
	}()
	var stopOnce sync.Once
	stopDatabaseWorkers := func() {
		stopOnce.Do(func() {
			stopWorkers()
			select {
			case <-databaseDone:
			case <-time.After(10 * time.Second):
				t.Error("database workers did not stop")
			}
			select {
			case <-applicationDone:
			case <-time.After(10 * time.Second):
				t.Error("application worker did not stop")
			}
		})
	}
	t.Cleanup(stopDatabaseWorkers)

	created := []managed.Resource{}
	t.Cleanup(func() {
		stopDatabaseWorkers()
		for _, item := range created {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Minute)
			if current, loadErr := database.DatabaseInternal(cleanup, item.ID); loadErr == nil {
				item = current
			}
			completed := false
			for cleanup.Err() == nil {
				done, deleteErr := runtime.DeleteDatabase(cleanup, item, func() error { return cleanup.Err() })
				if deleteErr != nil || done {
					if deleteErr != nil {
						t.Error("cleanup database", deleteErr)
					}
					completed = done
					break
				}
				time.Sleep(2 * time.Second)
			}
			if !completed {
				t.Error("cleanup database did not finish", item.ID)
			}
			stop()
		}
	})
	create := func(name, fixtureName string) managed.Resource {
		specification := managed.Spec{SchemaVersion: 1, Name: name, Engine: "vitess", Version: "23", Mode: "cluster", Shards: 2, Replicas: 1, CPU: "500m", Memory: "1Gi", StorageGiB: 1, Placement: managed.Placement{NodeNames: nodes}, TLS: &managed.TLSConfig{Mode: "required"}, Vitess: &managed.VitessConfig{BackupDestinationID: fixtures[fixtureName].Destination.ID, BackupDestinationRevision: 1, Tables: []managed.VitessTable{{Name: "records", ShardingColumn: "id"}}}}
		var operation managed.Operation
		status, code := client.request("POST", "/databases", map[string]any{"project": project, "environment": environment, "spec": specification}, &operation, "create-"+name)
		if status != http.StatusAccepted {
			t.Fatal("create database", status, code)
		}
		created = append(created, managed.Resource{ID: operation.DatabaseID, Spec: specification})
		return waitOperation(t, ctx, client, operation)
	}
	wrong := httpClient{t: t, ctx: ctx, server: httpServer, token: wrongToken}
	unauthorizedSpec := managed.Spec{SchemaVersion: 1, Name: "vitess-http-denied", Engine: "vitess", Version: "23", Mode: "cluster", Shards: 2, Replicas: 1, CPU: "500m", Memory: "1Gi", StorageGiB: 1, TLS: &managed.TLSConfig{Mode: "required"}, Vitess: &managed.VitessConfig{BackupDestinationID: fixtures["recovery-source"].Destination.ID, BackupDestinationRevision: 1, Tables: []managed.VitessTable{{Name: "records", ShardingColumn: "id"}}}}
	if status, _ := wrong.request("POST", "/databases", map[string]any{"project": project, "environment": environment, "spec": unauthorizedSpec}, nil, "denied"); status != http.StatusForbidden {
		t.Fatal("cross-scope database creation was not denied", status)
	}

	source := create("vitess-http-source", "recovery-source")
	target := create("vitess-http-target", "recovery-target")
	if status, _ := wrong.request("GET", "/databases/"+source.ID, nil, nil, ""); status != http.StatusNotFound {
		t.Fatal("cross-scope database read was not denied", status)
	}
	if status, _ := wrong.request("POST", "/databases/"+source.ID+"/credentials", map[string]any{}, nil, ""); status != http.StatusNotFound {
		t.Fatal("cross-scope database credentials were not denied", status)
	}
	if status, _ := wrong.request("GET", "/databases/"+source.ID+"/trust", nil, nil, ""); status != http.StatusNotFound {
		t.Fatal("cross-scope database trust was not denied", status)
	}
	var credentials map[string]string
	if status, _ := client.request("POST", "/databases/"+source.ID+"/credentials", map[string]any{}, &credentials, ""); status != http.StatusOK || credentials["password"] == "" {
		t.Fatal("credentials were not returned", status)
	}
	var trust managed.PublicTrust
	if status, _ := client.request("GET", "/databases/"+source.ID+"/trust", nil, &trust, ""); status != http.StatusOK || trust.CertificatePEM == "" {
		t.Fatal("public trust was not returned", status)
	}
	vitessQuery(t, ctx, runtime, source, credentials["password"], trust.CertificatePEM, "CREATE TABLE records (id BIGINT NOT NULL PRIMARY KEY, payload VARBINARY(16) NOT NULL, label VARCHAR(80) CHARACTER SET utf8mb4 NOT NULL)")
	vitessQuery(t, ctx, runtime, source, credentials["password"], trust.CertificatePEM, "INSERT INTO records(id,payload,label) VALUES (1,0x000AFF01,'नमस्ते / 東京'),(2,0x0080FF02,'source')")
	if got := vitessQuery(t, ctx, runtime, source, credentials["password"], trust.CertificatePEM, "SELECT id,HEX(payload),label FROM records ORDER BY id"); got != "1\t000AFF01\tनमस्ते / 東京\n2\t0080FF02\tsource" {
		t.Fatal("Vitess source data differs before backup")
	}

	var job backup.Job
	if status, code := client.request("POST", "/backups", map[string]any{"destination_id": fixtures["standalone"].Destination.ID, "source": backup.Source{Kind: "managed_database", ManagedDatabaseID: source.ID, Engine: "vitess"}}, &job, "vitess-http-backup"); status != http.StatusAccepted {
		t.Fatal("backup acceptance", status, code)
	}
	for ctx.Err() == nil && job.Status != "succeeded" {
		if err = server.Backups.RunOnce(ctx); err != nil {
			t.Fatal("backup worker", err)
		}
		job, err = database.BackupJob(ctx, job.ID)
		if err != nil || job.Status == "failed" || job.Status == "cancelled" {
			t.Fatal("backup failed", job.Status)
		}
		time.Sleep(time.Second)
	}
	artifact, err := database.BackupArtifact(ctx, job.ArtifactID)
	if err != nil || artifact.VerifiedAt == nil || artifact.Format != "age-v1+vitess-logical-v1" {
		t.Fatal("verified Vitess artifact was not recorded")
	}
	vitessQuery(t, ctx, runtime, source, credentials["password"], trust.CertificatePEM, "INSERT INTO records(id,payload,label) VALUES (99,0xCAFE,'after-backup')")
	var plan backup.RestorePlan
	if status, _ := client.request("POST", "/databases/"+target.ID+"/restore-plan", map[string]string{"artifact_id": artifact.ID}, &plan, ""); status != http.StatusOK {
		t.Fatal("restore plan", status)
	}
	var recovery backup.Job
	if status, code := client.request("POST", "/backup-artifacts/"+artifact.ID+"/restore", map[string]string{"plan_id": plan.ID, "confirmation": target.Spec.Name}, &recovery, "vitess-http-restore"); status != http.StatusAccepted {
		t.Fatal("restore acceptance", status, code)
	}
	for ctx.Err() == nil && recovery.Status != "succeeded" {
		if err = server.Backups.RunOnce(ctx); err != nil {
			t.Fatal("restore worker", err)
		}
		recovery, err = database.BackupJob(ctx, recovery.ID)
		if err != nil || recovery.Status == "failed" || recovery.Status == "cancelled" {
			t.Fatal("restore failed", recovery.Status)
		}
		time.Sleep(time.Second)
	}
	if err = database.RefreshDatabaseRecoveries(ctx); err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		if status, _ := client.request("GET", "/databases/"+target.ID, nil, &target, ""); status != http.StatusOK {
			t.Fatal("restored target lookup", status)
		}
		if target.Recovery != nil && target.Recovery.RestoredAt != nil && target.Status == "ready" {
			break
		}
		time.Sleep(time.Second)
	}
	var targetCredentials map[string]string
	if status, _ := client.request("POST", "/databases/"+target.ID+"/credentials", map[string]any{}, &targetCredentials, ""); status != http.StatusOK || targetCredentials["password"] == "" {
		t.Fatal("target credentials were not returned", status)
	}
	var targetTrust managed.PublicTrust
	if status, _ := client.request("GET", "/databases/"+target.ID+"/trust", nil, &targetTrust, ""); status != http.StatusOK || targetTrust.CertificatePEM == "" {
		t.Fatal("target public trust was not returned", status)
	}
	if got := vitessQuery(t, ctx, runtime, target, targetCredentials["password"], targetTrust.CertificatePEM, "SELECT id,HEX(payload),label FROM records ORDER BY id"); got != "1\t000AFF01\tनमस्ते / 東京\n2\t0080FF02\tsource" {
		t.Fatal("Vitess restored data differs from the captured recovery point")
	}
	if got := vitessQuery(t, ctx, runtime, source, credentials["password"], trust.CertificatePEM, "SELECT COUNT(*) FROM records WHERE id=99"); got != "1" {
		t.Fatal("Vitess recovery changed the source")
	}
	var inspected managed.Resource
	if status, _ := client.request("POST", "/databases/"+target.ID+"/inspect", map[string]any{"job_id": recovery.ID, "expected_revision": target.Revision, "confirm_name": target.Spec.Name, "inspected": true}, &inspected, ""); status != http.StatusOK || inspected.Recovery == nil || inspected.Recovery.InspectedAt == nil {
		t.Fatal("inspection acknowledgement", status)
	}

	applicationImage := os.Getenv("HAKOPOD_VITESS_ACCEPTANCE_APPLICATION_IMAGE")
	if !regexp.MustCompile(`^[A-Za-z0-9._:/-]+@sha256:[a-f0-9]{64}$`).MatchString(applicationImage) {
		t.Fatal("immutable Vitess acceptance application image is required")
	}
	appScript := `set -eu
if [ -z "${DATABASE_URL:-}" ]; then exec sleep 86400; fi
MYSQL_PWD=${DATABASE_URL#mysql://app:}
MYSQL_PWD=${MYSQL_PWD%%@*}
test "$DATABASE_URL" = "mysql://app:${MYSQL_PWD}@$1:3306/app@primary"
export MYSQL_PWD
result=$(mysql --no-defaults --protocol=TCP --host="$1" --port=3306 --user=app --database=app --connect-timeout=5 --ssl-mode=VERIFY_IDENTITY --ssl-ca="$2" --batch --raw --skip-column-names --execute='SELECT label FROM records WHERE id=1')
test "$result" = 'नमस्ते / 東京'
printf 'VITESS_BOUND_TLS_QUERY_OK\n'
exec sleep 86400`
	sourceHost := ""
	for _, endpoint := range source.Observation.Endpoints {
		if endpoint.Purpose == "read_write" && endpoint.Port == 3306 {
			sourceHost = endpoint.Host
		}
	}
	if sourceHost == "" {
		t.Fatal("Vitess application endpoint is unavailable")
	}
	application := spec.Application{SchemaVersion: 1, Name: "vitess-http-client", Services: map[string]spec.Service{"web": {Image: applicationImage, Command: []string{"sh", "-c"}, Args: []string{appScript, "vitess-http-client", sourceHost, cluster.DatabaseTrustPath(source.ID)}}}}
	var deployment store.Deployment
	if status, code := client.request("POST", "/deployments", map[string]any{"project": project, "environment": environment, "spec": application, "expected_revision": 0}, &deployment, "vitess-http-client-create"); status != http.StatusAccepted {
		t.Fatal("application deployment", status, code)
	}
	deployment = waitDeployment(t, ctx, client, deployment)
	var connectionPlan store.DatabaseConnectionPlan
	if status, _ := client.request("POST", "/databases/"+source.ID+"/connection-plan", map[string]any{"application_id": deployment.ApplicationID, "service": "web", "variable": "DATABASE_URL", "endpoint": "read_write", "cluster_aware": false}, &connectionPlan, ""); status != http.StatusOK {
		t.Fatal("connection plan", status)
	}
	var redeploy store.Deployment
	if status, _ := client.request("POST", "/databases/"+source.ID+"/connect", map[string]string{"review_id": connectionPlan.ID, "confirm_application": application.Name}, &redeploy, "vitess-http-connect"); status != http.StatusAccepted || redeploy.ApplicationID != deployment.ApplicationID || redeploy.Revision <= deployment.Revision {
		t.Fatal("connection deployment", status)
	}
	redeploy = waitDeployment(t, ctx, client, redeploy)
	logStream, err := runtime.Logs(ctx, cluster.Namespace(redeploy.ApplicationID), "web", 100, false)
	if err != nil {
		t.Fatal("bound application logs", err)
	}
	logData, readErr := io.ReadAll(io.LimitReader(logStream, (64<<10)+1))
	logStream.Close()
	if readErr != nil || len(logData) > 64<<10 || !bytes.Contains(logData, []byte("VITESS_BOUND_TLS_QUERY_OK")) {
		t.Fatal("bound application did not prove its TLS query")
	}
	empty := spec.Application{SchemaVersion: 1, Name: application.Name, Services: map[string]spec.Service{}}
	var emptyDeployment store.Deployment
	if status, code := client.request("POST", "/deployments", map[string]any{"project": project, "environment": environment, "spec": empty, "expected_revision": redeploy.Revision}, &emptyDeployment, "vitess-http-client-empty"); status != http.StatusAccepted {
		t.Fatal("empty application deployment", status, code)
	}
	emptyDeployment = waitDeployment(t, ctx, client, emptyDeployment)
	if status, code := client.request("DELETE", "/applications/"+redeploy.ApplicationID, map[string]any{"expected_revision": emptyDeployment.Revision, "confirm_name": application.Name, "delete_data": true}, nil, ""); status != http.StatusOK {
		t.Fatal("application deletion", status, code)
	}

	deleteDatabase := func(item managed.Resource) {
		t.Helper()
		var deletion managed.Operation
		if status, _ := client.request("DELETE", "/databases/"+item.ID, map[string]any{"expected_revision": item.Revision, "confirm_name": item.Spec.Name}, &deletion, "vitess-http-delete-"+item.ID); status != http.StatusAccepted {
			t.Fatal("database deletion", status)
		}
		_ = waitOperation(t, ctx, client, deletion)
	}
	deleteDatabase(target)
	deleteDatabase(source)
	t.Log("Vitess HTTP create, scope, credentials, trust, backup, restore, inspection, bound application query and deletion passed")
}

func TestFixtureNamesRemainBounded(t *testing.T) {
	for _, value := range []string{project, environment, "vitess-http-source", "vitess-http-target"} {
		if len(value) > 63 || !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(value) || strings.Contains(value, "--") {
			t.Fatal(fmt.Sprintf("invalid fixture name %q", value))
		}
	}
}
