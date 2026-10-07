package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/cluster"
	managed "github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/hakopod/hakopod/internal/worker"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type oracleHTTPOutput struct{ buffer bytes.Buffer }

func (w *oracleHTTPOutput) Write(p []byte) (int, error) {
	if len(p) > (64<<10)-w.buffer.Len() {
		return 0, fmt.Errorf("Oracle output exceeded its bound")
	}
	return w.buffer.Write(p)
}
func (w *oracleHTTPOutput) String() string { return w.buffer.String() }

const oracleHTTPQuery = `set -eu
umask 077
IFS= read -r password
[[ "$password" =~ ^[a-f0-9]{64}$ ]]
export TNS_ADMIN=$(mktemp -d)
trap 'rm -rf "$TNS_ADMIN"' EXIT
mkdir "$TNS_ADMIN/wallet"
printf '%s\n%s\n' "$password" "$password" | orapki wallet create -wallet "$TNS_ADMIN/wallet" -auto_login >/dev/null 2>&1
printf '%s\n' "$password" | orapki wallet add -wallet "$TNS_ADMIN/wallet" -trusted_cert -cert /etc/hakopod-tls/ca.crt >/dev/null 2>&1
cat > "$TNS_ADMIN/sqlnet.ora" <<EOF
WALLET_LOCATION=(SOURCE=(METHOD=FILE)(METHOD_DATA=(DIRECTORY=$TNS_ADMIN/wallet)))
SSL_SERVER_DN_MATCH=YES
SSL_CLIENT_AUTHENTICATION=FALSE
SSL_VERSION=1.2
SQLNET.OUTBOUND_CONNECT_TIMEOUT=5
SQLNET.RECV_TIMEOUT=20
EOF
{ printf 'whenever sqlerror exit failure\nwhenever oserror exit failure\nset echo off verify off feedback off heading off pagesize 0 trimspool on\n'; printf 'connect APP/"%s"@"(DESCRIPTION=(ADDRESS=(PROTOCOL=TCPS)(HOST=%s)(PORT=2484))(CONNECT_DATA=(SERVICE_NAME=FREEPDB1)))"\n' "$password" "$1"; cat; printf '\nexit\n'; } | sqlplus -s /nolog 2>/dev/null`

const oracleHTTPHeldSession = `set -eu
umask 077
IFS= read -r password
[[ "$password" =~ ^[a-f0-9]{64}$ ]]
export TNS_ADMIN=$(mktemp -d)
trap 'rm -rf "$TNS_ADMIN"' EXIT
mkdir "$TNS_ADMIN/wallet"
printf '%s\n%s\n' "$password" "$password" | orapki wallet create -wallet "$TNS_ADMIN/wallet" -auto_login >/dev/null 2>&1
printf '%s\n' "$password" | orapki wallet add -wallet "$TNS_ADMIN/wallet" -trusted_cert -cert /etc/hakopod-tls/ca.crt >/dev/null 2>&1
cat > "$TNS_ADMIN/sqlnet.ora" <<EOF
WALLET_LOCATION=(SOURCE=(METHOD=FILE)(METHOD_DATA=(DIRECTORY=$TNS_ADMIN/wallet)))
SSL_SERVER_DN_MATCH=YES
SSL_CLIENT_AUTHENTICATION=FALSE
SSL_VERSION=1.2
SQLNET.OUTBOUND_CONNECT_TIMEOUT=5
SQLNET.RECV_TIMEOUT=7200
EOF
{ printf 'whenever sqlerror exit failure\nwhenever oserror exit failure\nset echo off verify off feedback off heading off pagesize 0 trimspool on\n'; printf 'connect APP/"%s"@"(DESCRIPTION=(ADDRESS=(PROTOCOL=TCPS)(HOST=%s)(PORT=2484))(CONNECT_DATA=(SERVICE_NAME=FREEPDB1)))"\n' "$password" "$1"; printf "BEGIN DBMS_SESSION.SLEEP(1); END;\n/\nSELECT 'SESSION_READY' FROM dual;\nBEGIN DBMS_SESSION.SLEEP(5400); END;\n/\nexit\n"; } | sqlplus -s /nolog 2>/dev/null`

type oracleHTTPSessionOutput struct {
	buffer bytes.Buffer
	ready  chan struct{}
}

func (w *oracleHTTPSessionOutput) Write(p []byte) (int, error) {
	if len(p) > (64<<10)-w.buffer.Len() {
		return 0, fmt.Errorf("Oracle session output exceeded its bound")
	}
	n, err := w.buffer.Write(p)
	if bytes.Contains(w.buffer.Bytes(), []byte("SESSION_READY")) {
		select {
		case <-w.ready:
		default:
			close(w.ready)
		}
	}
	return n, err
}

func TestOracleHTTPOutputBoundsAndSignalsReadiness(t *testing.T) {
	var output oracleHTTPOutput
	if _, err := output.Write(bytes.Repeat([]byte{'x'}, (64<<10)+1)); err == nil {
		t.Fatal("oversized Oracle output was accepted")
	}
	session := &oracleHTTPSessionOutput{ready: make(chan struct{})}
	if _, err := session.Write([]byte("SESSION_READY\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.ready:
	default:
		t.Fatal("Oracle session readiness was not signaled")
	}
}

func oracleHTTPIngressOpen(t *testing.T, ctx context.Context, kube kubernetes.Interface, id string) bool {
	t.Helper()
	policy, err := kube.NetworkingV1().NetworkPolicies(cluster.DatabaseNamespace(id)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["hakopod.io/database-access-"+id] == "true" {
				return true
			}
		}
	}
	return false
}

func oracleHTTPWaitIngress(t *testing.T, ctx context.Context, kube kubernetes.Interface, id string, want bool) {
	t.Helper()
	wait, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for wait.Err() == nil {
		if oracleHTTPIngressOpen(t, wait, kube, id) == want {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("Oracle recovery application ingress did not reach expected state", want)
}

func oracleHTTPWaitDeployment(t *testing.T, ctx context.Context, client backupRequestClient, deployment store.Deployment) store.Deployment {
	t.Helper()
	for ctx.Err() == nil {
		var current store.Deployment
		if status := client.request("GET", "/deployments/"+deployment.ID, nil, &current, ""); status != http.StatusOK {
			t.Fatal("application deployment lookup", status)
		}
		if current.Status == "succeeded" {
			return current
		}
		if current.Status != "queued" && current.Status != "running" {
			t.Fatal("application deployment failed", current.Status, current.Error)
		}
		time.Sleep(time.Second)
	}
	t.Fatal("application deployment timed out")
	return store.Deployment{}
}

func oracleHTTPApplication() spec.Application {
	service := spec.Service{Image: managed.OracleFreeImage, NodeName: "k3d-hakopod-dev-server-0", Command: []string{"sleep", "7200"}, ReadOnlyRootFilesystem: true, TemporaryMounts: []spec.TemporaryMount{{MountPath: "/tmp", SizeMiB: 32, Memory: true}}, Resources: &spec.Resources{CPURequest: "20m", CPULimit: "300m", MemoryRequest: "64Mi", MemoryLimit: "512Mi"}}
	app, _ := spec.Normalize(spec.Application{Name: "oracle-http-binding", Services: map[string]spec.Service{"allowed": service, "unbound": service}})
	return app
}

func oracleHTTPApplicationExec(ctx context.Context, kubeconfig, namespace, service, script string, output io.Writer) error {
	cache, err := os.MkdirTemp("", "oracle-http-kubectl-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(cache)
	command := exec.CommandContext(ctx, "kubectl", "--cache-dir", cache, "--kubeconfig", kubeconfig, "--context", "k3d-hakopod-dev", "-n", namespace, "exec", "deployment/"+service, "--", "bash", "-c", script)
	command.Stdout, command.Stderr = output, io.Discard
	return command.Run()
}

func oracleHTTPBinding(t *testing.T, ctx context.Context, client backupRequestClient, runtime *cluster.Client, d managed.Resource, kubeconfig string) (string, string, int64) {
	t.Helper()
	endpoint := d.Observation.Endpoints[0]
	app := oracleHTTPApplication()
	deploy := func(next spec.Application, revision int64, idem string) store.Deployment {
		var plan map[string]any
		if status := client.request("POST", "/plan", map[string]any{"project": d.Project, "environment": d.Environment, "spec": next}, &plan, ""); status != http.StatusOK {
			t.Fatal("application plan", status)
		}
		var deployment store.Deployment
		if status, code := client.requestCode("POST", "/deployments", map[string]any{"project": d.Project, "environment": d.Environment, "spec": next, "expected_revision": revision}, &deployment, idem); status != http.StatusAccepted {
			t.Fatal("application deployment", status, code)
		}
		return oracleHTTPWaitDeployment(t, ctx, client, deployment)
	}
	deployment := deploy(app, 0, "oracle-http-binding-create")
	namespace := cluster.Namespace(deployment.ApplicationID)
	t.Log("Oracle HTTP owned namespace", namespace)
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") == "1" {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := runtime.RemoveShowcase(cleanup, deployment.ApplicationID); err != nil {
			t.Error("cleanup Oracle application", err)
		}
	})
	probe := func(service, script, want string) {
		step, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		var out oracleHTTPOutput
		if err := oracleHTTPApplicationExec(step, kubeconfig, namespace, service, script, &out); err != nil || strings.TrimSpace(out.String()) != want {
			t.Fatalf("Oracle %s binding probe failed (%v)", service, err)
		}
	}
	blocked := fmt.Sprintf(`if timeout 4 bash -c 'exec 3<>/dev/tcp/%s/2484' >/dev/null 2>&1; then echo reachable; else echo blocked; fi`, endpoint.Host)
	probe("allowed", blocked, "blocked")
	probe("unbound", blocked, "blocked")
	var plan store.DatabaseConnectionPlan
	if status := client.request("POST", "/databases/"+d.ID+"/connection-plan", map[string]any{"application_id": deployment.ApplicationID, "service": "allowed", "variable": "DATABASE_URL", "endpoint": "read_write", "cluster_aware": false}, &plan, ""); status != http.StatusOK {
		t.Fatal("connection plan", status)
	}
	var connected store.Deployment
	if status := client.request("POST", "/databases/"+d.ID+"/connect", map[string]string{"review_id": plan.ID, "confirm_application": app.Name}, &connected, "oracle-http-binding-connect"); status != http.StatusAccepted {
		t.Fatal("connection deployment", status)
	}
	connected = oracleHTTPWaitDeployment(t, ctx, client, connected)
	native := `set -eu
uri=${DATABASE_URL#oracle://APP:}; password=${uri%%@*}; address=${uri#*@}; host=${address%%:*}
export TNS_ADMIN=$(mktemp -d); trap 'rm -rf "$TNS_ADMIN"' EXIT; mkdir "$TNS_ADMIN/wallet"
printf '%s\n%s\n' "$password" "$password" | orapki wallet create -wallet "$TNS_ADMIN/wallet" -auto_login >/dev/null 2>&1
printf '%s\n' "$password" | orapki wallet add -wallet "$TNS_ADMIN/wallet" -trusted_cert -cert ` + cluster.DatabaseTrustPath(d.ID) + ` >/dev/null 2>&1
cat > "$TNS_ADMIN/sqlnet.ora" <<EOF
WALLET_LOCATION=(SOURCE=(METHOD=FILE)(METHOD_DATA=(DIRECTORY=$TNS_ADMIN/wallet)))
SSL_SERVER_DN_MATCH=YES
SSL_CLIENT_AUTHENTICATION=FALSE
SSL_VERSION=1.2
EOF
{ printf 'whenever sqlerror exit failure\nset heading off feedback off pagesize 0\n'; printf 'connect APP/"%s"@"(DESCRIPTION=(ADDRESS=(PROTOCOL=TCPS)(HOST=%s)(PORT=2484))(CONNECT_DATA=(SERVICE_NAME=FREEPDB1)))"\n' "$password" "$host"; printf "SELECT SYS_CONTEXT('USERENV','SESSION_USER') FROM dual;\nexit\n"; } | sqlplus -s /nolog 2>/dev/null`
	probe("allowed", native, "APP")
	probe("unbound", blocked, "blocked")
	revoked := deploy(app, connected.Revision, "oracle-http-binding-revoke")
	probe("allowed", blocked, "blocked")
	return deployment.ApplicationID, namespace, revoked.Revision
}

func TestManagedOracleFreeHTTPApplicationDeploymentAdmission(t *testing.T) {
	if os.Getenv("HAKOPOD_ORACLE_FREE_HTTP_ADMISSION_TEST") != "1" {
		t.Skip("set HAKOPOD_ORACLE_FREE_HTTP_ADMISSION_TEST=1 for the development admission check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("Oracle Free application admission requires k3d-hakopod-dev")
	}
	restConfig, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	kube, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatal(err)
	}
	inventory := func() []byte {
		namespaces, listErr := kube.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
		if listErr != nil {
			t.Fatal(listErr)
		}
		volumes, listErr := kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
		if listErr != nil {
			t.Fatal(listErr)
		}
		result := map[string]map[string]string{"namespaces": {}, "persistent_volumes": {}}
		for _, namespace := range namespaces.Items {
			result["namespaces"][namespace.Name] = string(namespace.UID)
		}
		for _, volume := range volumes.Items {
			result["persistent_volumes"][volume.Name] = string(volume.UID)
		}
		return store.JSON(result)
	}
	before := inventory()
	runtime, err := cluster.New(path, cluster.Options{})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := database(t)
	token, err := db.Bootstrap(ctx, "oracle-free-http-admission")
	if err != nil {
		t.Fatal(err)
	}
	db.ValidateDeployment = func(ctx context.Context, application store.Application, next spec.Application) error {
		return runtime.ValidateDelivery(ctx, cluster.Target{ApplicationID: application.ID, Project: application.Project, Environment: application.Environment, Revision: application.Revision, Spec: next})
	}
	server := httptest.NewServer((&api.Server{Store: db, Cluster: runtime}).Handler())
	defer server.Close()
	server.Client().Timeout = 30 * time.Second
	client := backupRequestClient{t: t, server: server, token: token}
	input := map[string]any{"project": "demo", "environment": "development", "spec": oracleHTTPApplication()}
	var plan map[string]any
	if status, code := client.requestCode("POST", "/plan", input, &plan, ""); status != http.StatusOK {
		t.Fatal("application plan", status, code)
	}
	input["expected_revision"] = int64(0)
	var deployment store.Deployment
	if status, code := client.requestCode("POST", "/deployments", input, &deployment, "oracle-http-admission"); status != http.StatusAccepted || deployment.ID == "" || deployment.ApplicationID == "" {
		t.Fatal("application deployment admission", status, code)
	}
	if after := inventory(); !bytes.Equal(after, before) {
		t.Fatal("application admission mutated the development cluster")
	}
}

func TestManagedOracleFreeHTTPLive(t *testing.T) {
	if os.Getenv("HAKOPOD_ORACLE_FREE_HTTP_TEST") != "1" {
		t.Skip("set HAKOPOD_ORACLE_FREE_HTTP_TEST=1 for the owned development acceptance environment")
	}
	if os.Getenv("HAKOPOD_TEST_DATABASE_URL") == "" {
		t.Fatal("HAKOPOD_TEST_DATABASE_URL is required for disposable control-plane PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 95*time.Minute)
	defer cancel()
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("Oracle Free API recovery requires k3d-hakopod-dev")
	}
	rest, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	kube, err := kubernetes.NewForConfig(rest)
	if err != nil {
		t.Fatal(err)
	}
	nodes := strings.Split(os.Getenv("HAKOPOD_ORACLE_FREE_HTTP_NODES"), ",")
	if len(nodes) == 0 || len(nodes) > 2 || nodes[0] == "" {
		t.Fatal("exact Oracle node inventory is required")
	}
	runtime, err := cluster.New(path, cluster.Options{})
	if err != nil {
		t.Fatal(err)
	}
	db, dsn := database(t)
	token, err := db.Bootstrap(ctx, "oracle-free-http-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('other'); INSERT INTO environments(project,name) VALUES('other','production')"); err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	_, scopedToken, err := db.CreateKey(ctx, principal, store.KeyInput{Name: "oracle-http-foreign", Project: "other", Environment: "production", Permissions: []string{"deployments:read", "deployments:write"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, access, secret, objectClient := liveBackupObjectStore(t, ctx)
	server := &api.Server{Store: db, Cluster: runtime, Auth: api.AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{23}, 32))}}
	db.ValidateDeployment = func(ctx context.Context, application store.Application, next spec.Application) error {
		return runtime.ValidateDelivery(ctx, cluster.Target{ApplicationID: application.ID, Project: application.Project, Environment: application.Environment, Revision: application.Revision, Spec: next})
	}
	state := t.TempDir()
	if err = os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	server.ConfigureDatabaseBindings()
	server.ConfigureBackups(api.BackupConfig{DatabaseURL: dsn, StateDir: state, MaxBytes: 64 << 20})
	httpServer := httptest.NewServer(server.Handler())
	httpServer.Client().Timeout = 30 * time.Second
	defer httpServer.Close()
	client := backupRequestClient{t, httpServer, token}
	runCtx, stopWorkers := context.WithCancel(ctx)
	workersDone := make(chan struct{})
	go func() { defer close(workersDone); server.RunManagedDatabases(runCtx) }()
	applicationDone := make(chan struct{})
	go func() {
		defer close(applicationDone)
		(&worker.Worker{Store: db, Cluster: runtime, Concurrency: 1, Timeout: 10 * time.Minute}).Run(runCtx)
	}()
	defer func() {
		stopWorkers()
		select {
		case <-workersDone:
		case <-time.After(10 * time.Second):
			t.Error("database workers did not stop")
		}
		select {
		case <-applicationDone:
		case <-time.After(10 * time.Second):
			t.Error("application worker did not stop")
		}
	}()

	var destination struct {
		Destination backup.Destination `json:"destination"`
	}
	if status := client.request("POST", "/backup-destinations", backup.DestinationInput{Name: "oracle-free-http-recovery", Endpoint: endpoint, Region: "us-east-1", Bucket: "hakopod-backup-tests", Prefix: "oracle-free-http", PathStyle: true, AllowHTTP: true, AccessKeyID: access, SecretAccessKey: secret}, &destination, ""); status != 201 {
		t.Fatal("destination", status)
	}

	var created []managed.Resource
	cleanup := func() {
		if t.Failed() && os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") == "1" {
			return
		}
		for _, d := range created {
			clean, stop := context.WithTimeout(context.Background(), 3*time.Minute)
			completed := false
			for clean.Err() == nil {
				current, loadErr := db.DatabaseInternal(clean, d.ID)
				if loadErr != nil {
					t.Errorf("reload database %s for cleanup: %v", d.ID, loadErr)
					break
				}
				d = current
				done, cleanupErr := runtime.DeleteDatabase(clean, d, func() error { return clean.Err() })
				if cleanupErr != nil {
					t.Errorf("cleanup database %s: %v", d.ID, cleanupErr)
					break
				}
				if done {
					completed = true
					break
				}
				time.Sleep(2 * time.Second)
			}
			if !completed && clean.Err() != nil {
				t.Errorf("cleanup database %s timed out", d.ID)
			}
			stop()
		}
	}
	t.Cleanup(cleanup)

	create := func(name, idem string) (managed.Resource, managed.Operation) {
		spec := managed.Spec{SchemaVersion: 1, Name: name, Engine: "oracle", Version: "23.26", Mode: "standalone", Shards: 1, CPU: "1", Memory: "4Gi", StorageGiB: 10, Placement: managed.Placement{NodeNames: append([]string(nil), nodes...)}, TLS: &managed.TLSConfig{Mode: "required"}, Oracle: &managed.OracleConfig{Edition: "free"}}
		var operation managed.Operation
		if status := client.request("POST", "/databases", map[string]any{"project": "demo", "environment": "development", "spec": spec}, &operation, idem); status != 202 {
			t.Fatal("create database", status)
		}
		created = append(created, managed.Resource{ID: operation.DatabaseID, Spec: spec})
		var replay managed.Operation
		if status := client.request("POST", "/databases", map[string]any{"project": "demo", "environment": "development", "spec": spec}, &replay, idem); status != 202 || replay.ID != operation.ID || replay.DatabaseID != operation.DatabaseID {
			t.Fatal("database create was not idempotent")
		}
		for ctx.Err() == nil {
			var current managed.Operation
			if status := client.request("GET", "/database-operations/"+operation.ID, nil, &current, ""); status != 200 {
				t.Fatal("database operation", status)
			}
			if current.Status == "failed" || current.Status == "cancelled" {
				t.Fatal("database provisioning failed", current.ID, current.Status, current.Phase)
			}
			if current.Status == "succeeded" {
				var resource managed.Resource
				if status := client.request("GET", "/databases/"+operation.DatabaseID, nil, &resource, ""); status != 200 || resource.Status != "ready" || !resource.Observation.Fresh(time.Now(), resource.Revision) || resource.Observation.TLS == nil || !resource.Observation.TLS.Verified || !resource.Observation.TLS.PlaintextRejected || len(resource.Observation.Members) != 1 {
					t.Fatal("database readiness was not observed")
				}
				t.Log("Oracle HTTP owned namespace", cluster.DatabaseNamespace(resource.ID))
				return resource, operation
			}
			time.Sleep(time.Second)
		}
		t.Fatal("database creation timed out")
		return managed.Resource{}, managed.Operation{}
	}

	source, _ := create("oracle-free-http-source", "oracle-free-http-source-create")
	target, _ := create("oracle-free-http-target", "oracle-free-http-target-create")
	t.Run("authorization", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/v1/databases/"+source.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := httpServer.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatal("database route accepted an unauthenticated request", response.StatusCode)
		}
		foreign := backupRequestClient{t: t, server: httpServer, token: scopedToken}
		var hidden managed.Resource
		if status := foreign.request("GET", "/databases/"+source.ID, nil, &hidden, ""); status != http.StatusNotFound && status != http.StatusForbidden {
			t.Fatal("cross-project key accessed Oracle database", status)
		}
		for _, request := range []struct {
			method, path string
			body         any
		}{{"GET", "/databases/" + source.ID + "/trust", nil}, {"POST", "/databases/" + source.ID + "/credentials", map[string]any{}}} {
			var protected map[string]any
			if status := foreign.request(request.method, request.path, request.body, &protected, ""); status != http.StatusNotFound && status != http.StatusForbidden {
				t.Fatal("cross-project key accessed protected Oracle material", request.path, status)
			}
		}
	})
	var trust managed.PublicTrust
	if status := client.request("GET", "/databases/"+source.ID+"/trust", nil, &trust, ""); status != 200 || trust.CertificatePEM == "" {
		t.Fatal("Oracle public trust unavailable", status)
	}
	runtimeTrust, err := runtime.DatabaseTrust(ctx, source)
	if err != nil || trust.CertificatePEM != runtimeTrust.CertificatePEM {
		t.Fatal("Oracle API trust did not match the verified runtime identity")
	}
	var bindingCredentials map[string]string
	if status := client.request("POST", "/databases/"+source.ID+"/credentials", map[string]any{}, &bindingCredentials, ""); status != 200 || bindingCredentials["password"] == "" {
		t.Fatal("Oracle credentials unavailable", status)
	}
	appID, appNamespace, appRevision := oracleHTTPBinding(t, ctx, client, runtime, source, path)
	appNamespaceObject, err := kube.CoreV1().Namespaces().Get(ctx, appNamespace, metav1.GetOptions{})
	if err != nil || appNamespaceObject.UID == "" || appNamespaceObject.Labels["app.kubernetes.io/managed-by"] != "hakopod" || appNamespaceObject.Labels["hakopod.io/application-id"] == "" {
		t.Fatal("Oracle HTTP application namespace identity is unavailable", err)
	}
	appNamespaceUID := appNamespaceObject.UID
	appNamespaceApplicationLabel := appNamespaceObject.Labels["hakopod.io/application-id"]

	query := func(d managed.Resource, stage, sql string) string {
		var credentials map[string]string
		if status := client.request("POST", "/databases/"+d.ID+"/credentials", map[string]any{}, &credentials, ""); status != 200 {
			t.Fatal("credentials", status)
		}
		member := d.Observation.Members[0]
		host := "database." + cluster.DatabaseNamespace(d.ID) + ".svc.cluster.local"
		input := bytes.NewBufferString(credentials["password"] + "\n" + sql + "\n")
		var output oracleHTTPOutput
		step, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		if err := runtime.DatabaseExec(step, d, member, []string{"bash", "-c", oracleHTTPQuery, "oracle-free-http-query", host}, input, &output); err != nil {
			diagnostic := "credential-safe output withheld"
			t.Fatalf("Oracle Free app query failed at %s (%v; %s)", stage, err, diagnostic)
		}
		return strings.TrimSpace(output.String())
	}
	query(source, "source_create", "CREATE TABLE api_recovery (id NUMBER PRIMARY KEY, value RAW(16));")
	query(source, "source_insert_initial", "INSERT INTO api_recovery VALUES (1, HEXTORAW('0080FF0D0A'));\nCOMMIT;")
	jobFailure := func(stage string, job backup.Job, databaseID string) {
		t.Helper()
		var databaseStatus, observedStatus string
		var maintenanceActive bool
		code := clickhouseAPIJobFailureCode(job.Error)
		if stage == "backup" && code == "s3_object_upload" {
			check, cancelCheck := context.WithTimeout(context.Background(), 5*time.Second)
			_, headErr := objectClient.HeadObject(check, &s3.HeadObjectInput{Bucket: aws.String(destination.Destination.Bucket), Key: aws.String(backup.ObjectKey(destination.Destination, job.ID))})
			cancelCheck()
			if headErr == nil {
				t.Log("backup object exists after reported upload failure")
			} else {
				logLiveBackupAWSFailure(t, "failed backup object check", headErr)
			}
		}
		diagnostic, stop := context.WithTimeout(ctx, 3*time.Second)
		defer stop()
		if err := db.Pool.QueryRow(diagnostic, `SELECT status,COALESCE(observation->>'status',''),COALESCE(maintenance_lease_until>clock_timestamp(),false) FROM managed_databases WHERE id=$1`, databaseID).Scan(&databaseStatus, &observedStatus, &maintenanceActive); err != nil {
			t.Fatalf("Oracle Free %s failed code=%s state=unavailable", stage, code)
		}
		t.Fatalf("Oracle Free %s failed code=%s database=%s observation=%s maintenance_active=%t", stage, code, databaseStatus, observedStatus, maintenanceActive)
	}

	var job backup.Job
	if status, code := client.requestCode("POST", "/backups", map[string]any{"destination_id": destination.Destination.ID, "source": backup.Source{Kind: "managed_database", ManagedDatabaseID: source.ID, Engine: "oracle"}}, &job, "oracle-free-http-backup"); status != 202 {
		var databaseStatus, observedStatus string
		var maintenanceActive bool
		diagnostic, stop := context.WithTimeout(ctx, 3*time.Second)
		defer stop()
		if err := db.Pool.QueryRow(diagnostic, `SELECT status,COALESCE(observation->>'status',''),COALESCE(maintenance_lease_until>clock_timestamp(),false) FROM managed_databases WHERE id=$1`, source.ID).Scan(&databaseStatus, &observedStatus, &maintenanceActive); err != nil {
			t.Fatal("backup acceptance", status, code, "state unavailable")
		}
		t.Fatalf("backup acceptance status=%d code=%s database=%s observation=%s maintenance_active=%t", status, code, databaseStatus, observedStatus, maintenanceActive)
	}
	for ctx.Err() == nil {
		if err = server.Backups.RunOnce(ctx); err != nil {
			t.Fatal("backup worker", err)
		}
		job, err = db.BackupJob(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == "failed" {
			jobFailure("backup", job, source.ID)
		}
		if job.Status == "succeeded" {
			break
		}
		time.Sleep(time.Second)
	}
	if job.Status != "succeeded" {
		t.Fatal("Oracle Free backup timed out", job.ID, job.Status)
	}
	artifact, err := db.BackupArtifact(ctx, job.ArtifactID)
	if err != nil || artifact.Source.Engine != "oracle" || artifact.Source.ManagedDatabaseID != source.ID || artifact.SourceRevision != source.Revision || artifact.VerifiedAt == nil {
		t.Fatal("Oracle Free backup artifact is incomplete")
	}
	object, err := objectClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(destination.Destination.Bucket), Key: aws.String(backup.ObjectKey(destination.Destination, job.ID))})
	if err != nil {
		t.Fatal("read encrypted Oracle backup", err)
	}
	ciphertext, readErr := io.ReadAll(io.LimitReader(object.Body, 1<<20))
	closeErr := object.Body.Close()
	if readErr != nil || closeErr != nil || !bytes.HasPrefix(ciphertext, []byte("age-encryption.org/v1")) || bytes.Contains(ciphertext, []byte("0080FF0D0A")) {
		t.Fatal("Oracle backup object was not verified as encrypted")
	}
	query(source, "source_insert_after_backup", "INSERT INTO api_recovery VALUES (2, HEXTORAW('01027F'));\nCOMMIT;")

	var plan backup.RestorePlan
	if status := client.request("POST", "/databases/"+target.ID+"/restore-plan", map[string]string{"artifact_id": artifact.ID}, &plan, ""); status != 200 {
		t.Fatal("restore plan", status)
	}
	var preRestorePlan store.DatabaseConnectionPlan
	if status := client.request("POST", "/databases/"+target.ID+"/connection-plan", map[string]any{"application_id": appID, "service": "allowed", "variable": "DATABASE_URL", "endpoint": "read_write", "cluster_aware": false}, &preRestorePlan, ""); status != http.StatusOK {
		t.Fatal("pre-restore target connection plan", status)
	}
	var preRestoreDeployment store.Deployment
	if status := client.request("POST", "/databases/"+target.ID+"/connect", map[string]string{"review_id": preRestorePlan.ID, "confirm_application": "oracle-http-binding"}, &preRestoreDeployment, "oracle-http-target-session"); status != http.StatusAccepted {
		t.Fatal("pre-restore target connection", status)
	}
	preRestoreDeployment = oracleHTTPWaitDeployment(t, ctx, client, preRestoreDeployment)
	appRevision = preRestoreDeployment.Revision
	if status, code := client.requestCode("POST", "/backup-artifacts/"+artifact.ID+"/restore", map[string]string{"plan_id": plan.ID, "confirmation": target.Spec.Name}, nil, "oracle-free-http-bound-restore-refusal"); status != http.StatusConflict || code != "conflict" {
		t.Fatal("restore into bound Oracle target was not refused", status, code)
	}
	var revoked store.Deployment
	if status := client.request("POST", "/deployments", map[string]any{"project": target.Project, "environment": target.Environment, "spec": oracleHTTPApplication(), "expected_revision": appRevision}, &revoked, "oracle-http-target-revoke"); status != http.StatusAccepted {
		t.Fatal("target binding revocation", status)
	}
	revoked = oracleHTTPWaitDeployment(t, ctx, client, revoked)
	appRevision = revoked.Revision
	pods, err := kube.CoreV1().Pods(appNamespace).List(ctx, metav1.ListOptions{LabelSelector: "hakopod.io/service=allowed", Limit: 2})
	if err != nil || len(pods.Items) != 1 || pods.Items[0].UID == "" || len(pods.Items[0].Status.ContainerStatuses) != 1 || pods.Items[0].Status.ContainerStatuses[0].ContainerID == "" {
		t.Fatal("unbound application pod identity is unavailable", err)
	}
	applicationPodUID := pods.Items[0].UID
	applicationContainerID := pods.Items[0].Status.ContainerStatuses[0].ContainerID
	applicationRestartCount := pods.Items[0].Status.ContainerStatuses[0].RestartCount
	if status := client.request("POST", "/databases/"+target.ID+"/restore-plan", map[string]string{"artifact_id": artifact.ID}, &plan, ""); status != http.StatusOK {
		t.Fatal("fresh restore plan after target binding revocation", status)
	}
	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	sessionOutput := &oracleHTTPSessionOutput{ready: make(chan struct{})}
	sessionDone := make(chan error, 1)
	var sessionCredentials map[string]string
	if status := client.request("POST", "/databases/"+target.ID+"/credentials", map[string]any{}, &sessionCredentials, ""); status != http.StatusOK {
		t.Fatal("target credentials for held recovery session", status)
	}
	sessionInput := bytes.NewBufferString(sessionCredentials["password"] + "\n")
	sessionHost := "database." + cluster.DatabaseNamespace(target.ID) + ".svc.cluster.local"
	go func() {
		sessionDone <- runtime.DatabaseExec(sessionCtx, target, target.Observation.Members[0], []string{"bash", "-c", oracleHTTPHeldSession, "oracle-free-http-held-session", sessionHost}, sessionInput, sessionOutput)
	}()
	select {
	case <-sessionOutput.ready:
	case err := <-sessionDone:
		t.Fatal("held Oracle session ended before recovery", err)
	case <-time.After(30 * time.Second):
		t.Fatal("held Oracle session did not authenticate")
	}
	select {
	case err := <-sessionDone:
		t.Fatal("held Oracle session ended before restore enqueue", err)
	default:
	}
	var recovery backup.Job
	if status := client.request("POST", "/backup-artifacts/"+artifact.ID+"/restore", map[string]string{"plan_id": plan.ID, "confirmation": target.Spec.Name}, &recovery, "oracle-free-http-restore"); status != 202 {
		t.Fatal("restore acceptance", status)
	}
	for ctx.Err() == nil {
		if err = server.Backups.RunOnce(ctx); err != nil {
			t.Fatal("restore worker", err)
		}
		recovery, err = db.BackupJob(ctx, recovery.ID)
		if err != nil {
			t.Fatal(err)
		}
		if recovery.Status == "failed" {
			jobFailure("restore", recovery, target.ID)
		}
		if recovery.Status == "succeeded" {
			break
		}
		time.Sleep(time.Second)
	}
	if recovery.Status != "succeeded" {
		t.Fatal("Oracle Free restore timed out", recovery.ID, recovery.Status)
	}
	if err = db.RefreshDatabaseRecoveries(ctx); err != nil {
		t.Fatal(err)
	}
	// Restore replaces data pods to revoke sessions. Wait for the API to report
	// the replacement instead of reusing the member identity from creation.
	previousUID := target.Observation.Members[0].UID
	observed, stopObserved := context.WithTimeout(ctx, 90*time.Second)
	defer stopObserved()
	for {
		var current managed.Resource
		if status := client.request("GET", "/databases/"+target.ID, nil, &current, ""); status != 200 {
			t.Fatal("restored database observation", status)
		}
		if current.ID != target.ID || current.Revision != target.Revision || current.Project != target.Project || current.Environment != target.Environment {
			t.Fatal("restored database identity changed")
		}
		if current.Status == "ready" && current.Observation.Status == "ready" && current.Observation.Fresh(time.Now(), current.Revision) && current.Observation.TLS != nil && current.Observation.TLS.Verified && len(current.Observation.Members) == 1 && current.Observation.Members[0].UID != "" && current.Observation.Members[0].UID != previousUID && current.Recovery != nil && current.Recovery.JobID == recovery.ID && current.Recovery.RestoredAt != nil {
			target = current
			break
		}
		select {
		case <-observed.Done():
			t.Fatal("restored database replacement was not observed through the API")
		case <-time.After(time.Second):
		}
	}
	select {
	case err := <-sessionDone:
		if err == nil {
			t.Fatal("recovery did not revoke held Oracle session")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("held Oracle session survived replacement")
	}
	pods, err = kube.CoreV1().Pods(appNamespace).List(ctx, metav1.ListOptions{LabelSelector: "hakopod.io/service=allowed", Limit: 2})
	if err != nil || len(pods.Items) != 1 || pods.Items[0].UID != applicationPodUID || len(pods.Items[0].Status.ContainerStatuses) != 1 || pods.Items[0].Status.ContainerStatuses[0].ContainerID != applicationContainerID || pods.Items[0].Status.ContainerStatuses[0].RestartCount != applicationRestartCount {
		t.Fatal("application pod or container changed during database recovery", err)
	}
	oracleHTTPWaitIngress(t, ctx, kube, target.ID, false)
	blocked := fmt.Sprintf(`if timeout 4 bash -c 'exec 3<>/dev/tcp/%s/2484' >/dev/null 2>&1; then echo reachable; else echo blocked; fi`, target.Observation.Endpoints[0].Host)
	var blockedOutput oracleHTTPOutput
	if err := oracleHTTPApplicationExec(ctx, path, appNamespace, "allowed", blocked, &blockedOutput); err != nil || strings.TrimSpace(blockedOutput.String()) != "blocked" {
		t.Fatal("unbound application reached uninspected recovery target")
	}
	var refusedConnectionPlan store.DatabaseConnectionPlan
	if status, code := client.requestCode("POST", "/databases/"+target.ID+"/connection-plan", map[string]any{"application_id": appID, "service": "allowed", "variable": "DATABASE_URL", "endpoint": "read_write", "cluster_aware": false}, &refusedConnectionPlan, ""); status != http.StatusConflict || code != "conflict" {
		t.Fatal("uninspected Oracle recovery accepted a connection review", status, code)
	}
	if query(target, "target_verify_restore", "SELECT id||CHR(9)||RAWTOHEX(value) FROM api_recovery ORDER BY id;") != "1\t0080FF0D0A" || query(source, "source_verify_preserved", "SELECT id||CHR(9)||RAWTOHEX(value) FROM api_recovery ORDER BY id;") != "1\t0080FF0D0A\n2\t01027F" {
		t.Fatal("Oracle Free recovery point or source preservation changed")
	}
	var inspected managed.Resource
	if status := client.request("POST", "/databases/"+target.ID+"/inspect", map[string]any{"job_id": recovery.ID, "expected_revision": target.Revision, "confirm_name": target.Spec.Name, "inspected": true}, &inspected, ""); status != 200 || inspected.Recovery == nil || inspected.Recovery.InspectedAt == nil {
		t.Fatal("recovery inspection", status)
	}
	var inspectedConnectionPlan store.DatabaseConnectionPlan
	if status := client.request("POST", "/databases/"+target.ID+"/connection-plan", map[string]any{"application_id": appID, "service": "allowed", "variable": "DATABASE_URL", "endpoint": "read_write", "cluster_aware": false}, &inspectedConnectionPlan, ""); status != http.StatusOK {
		t.Fatal("inspected Oracle recovery connection plan", status)
	}
	var rebound store.Deployment
	if status := client.request("POST", "/databases/"+target.ID+"/connect", map[string]string{"review_id": inspectedConnectionPlan.ID, "confirm_application": "oracle-http-binding"}, &rebound, "oracle-http-inspected-target-connect"); status != http.StatusAccepted {
		t.Fatal("inspected Oracle recovery connection", status)
	}
	rebound = oracleHTTPWaitDeployment(t, ctx, client, rebound)
	appRevision = rebound.Revision
	oracleHTTPWaitIngress(t, ctx, kube, target.ID, true)
	boundQuery := `set -eu
uri=${DATABASE_URL#oracle://APP:}; password=${uri%%@*}; address=${uri#*@}; host=${address%%:*}
export TNS_ADMIN=$(mktemp -d); trap 'rm -rf "$TNS_ADMIN"' EXIT; mkdir "$TNS_ADMIN/wallet"
printf '%s\n%s\n' "$password" "$password" | orapki wallet create -wallet "$TNS_ADMIN/wallet" -auto_login >/dev/null 2>&1
printf '%s\n' "$password" | orapki wallet add -wallet "$TNS_ADMIN/wallet" -trusted_cert -cert ` + cluster.DatabaseTrustPath(target.ID) + ` >/dev/null 2>&1
cat > "$TNS_ADMIN/sqlnet.ora" <<EOF
WALLET_LOCATION=(SOURCE=(METHOD=FILE)(METHOD_DATA=(DIRECTORY=$TNS_ADMIN/wallet)))
SSL_SERVER_DN_MATCH=YES
SSL_CLIENT_AUTHENTICATION=FALSE
SSL_VERSION=1.2
SQLNET.OUTBOUND_CONNECT_TIMEOUT=5
SQLNET.RECV_TIMEOUT=30
EOF
{ printf 'whenever sqlerror exit failure\nset heading off feedback off pagesize 0\n'; printf 'connect APP/"%s"@"(DESCRIPTION=(ADDRESS=(PROTOCOL=TCPS)(HOST=%s)(PORT=2484))(CONNECT_DATA=(SERVICE_NAME=FREEPDB1)))"\n' "$password" "$host"; printf "SELECT RAWTOHEX(value) FROM api_recovery WHERE id=1;\nexit\n"; } | sqlplus -s /nolog 2>/dev/null`
	var boundOutput oracleHTTPOutput
	if err := oracleHTTPApplicationExec(ctx, path, appNamespace, "allowed", boundQuery, &boundOutput); err != nil || strings.TrimSpace(boundOutput.String()) != "0080FF0D0A" {
		t.Fatal("bound application did not query inspected Oracle recovery", err)
	}
	var unboundOutput oracleHTTPOutput
	if err := oracleHTTPApplicationExec(ctx, path, appNamespace, "unbound", blocked, &unboundOutput); err != nil || strings.TrimSpace(unboundOutput.String()) != "blocked" {
		t.Fatal("unbound application reached inspected Oracle recovery")
	}
	var finalRevocation store.Deployment
	if status := client.request("POST", "/deployments", map[string]any{"project": target.Project, "environment": target.Environment, "spec": oracleHTTPApplication(), "expected_revision": appRevision}, &finalRevocation, "oracle-http-inspected-target-revoke"); status != http.StatusAccepted {
		t.Fatal("inspected target binding revocation", status)
	}
	finalRevocation = oracleHTTPWaitDeployment(t, ctx, client, finalRevocation)
	appRevision = finalRevocation.Revision
	type ownedVolume struct {
		name string
		uid  types.UID
	}
	deleteHTTP := func(d managed.Resource) {
		ns := cluster.DatabaseNamespace(d.ID)
		claims, e := kube.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{Limit: 8})
		if e != nil {
			t.Fatal(e)
		}
		if claims.Continue != "" || len(claims.Items) > 8 {
			t.Fatal("database claim list exceeded its bound")
		}
		volumes := make([]ownedVolume, 0, len(claims.Items))
		for _, claim := range claims.Items {
			if claim.Spec.VolumeName == "" || claim.UID == "" {
				t.Fatal("database claim identity is incomplete")
			}
			pv, e := kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
			if e != nil {
				t.Fatal(e)
			}
			if pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.Namespace != ns || pv.Spec.ClaimRef.Name != claim.Name || pv.Spec.ClaimRef.UID != claim.UID {
				t.Fatal("database volume ownership changed")
			}
			volumes = append(volumes, ownedVolume{pv.Name, pv.UID})
		}
		var operation managed.Operation
		if status := client.request("DELETE", "/databases/"+d.ID, map[string]any{"expected_revision": d.Revision, "confirm_name": d.Spec.Name}, &operation, "oracle-free-http-delete-"+d.ID); status != 202 {
			t.Fatal("database delete", status)
		}
		var current managed.Operation
		for ctx.Err() == nil {
			if status := client.request("GET", "/database-operations/"+operation.ID, nil, &current, ""); status != 200 {
				t.Fatal("delete operation", status)
			}
			if current.Status == "failed" || current.Status == "cancelled" {
				t.Fatal("database deletion failed", current.ID, current.Status, current.Phase)
			}
			if current.Status == "succeeded" {
				break
			}
			time.Sleep(time.Second)
		}
		if current.Status != "succeeded" {
			t.Fatal("database deletion timed out", current.ID, current.Status, current.Phase)
		}
		if _, e = kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); !apierrors.IsNotFound(e) {
			t.Fatal("owned namespace remains after deletion")
		}
		for _, volume := range volumes {
			pv, e := kube.CoreV1().PersistentVolumes().Get(ctx, volume.name, metav1.GetOptions{})
			if !apierrors.IsNotFound(e) && (e != nil || pv.UID == volume.uid) {
				t.Fatal("owned persistent volume remains after deletion")
			}
		}
	}
	deleteHTTP(target)
	deleteHTTP(source)
	empty, err := spec.Normalize(spec.Application{Name: "oracle-http-binding", Services: map[string]spec.Service{}})
	if err != nil {
		t.Fatal("normalize empty Oracle HTTP application", err)
	}
	var emptied store.Deployment
	if status := client.request("POST", "/deployments", map[string]any{"project": source.Project, "environment": source.Environment, "spec": empty, "expected_revision": appRevision}, &emptied, "oracle-http-empty-application"); status != http.StatusAccepted {
		t.Fatal("empty Oracle HTTP application deployment", status)
	}
	emptied = oracleHTTPWaitDeployment(t, ctx, client, emptied)
	appRevision = emptied.Revision
	if status := client.request("DELETE", "/applications/"+appID, map[string]any{"expected_revision": appRevision, "confirm_name": "oracle-http-binding", "delete_data": true}, nil, ""); status != http.StatusOK {
		t.Fatal("application deletion", status)
	}
	applicationDelete, cancelApplicationDelete := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelApplicationDelete()
	for applicationDelete.Err() == nil {
		current, e := kube.CoreV1().Namespaces().Get(applicationDelete, appNamespace, metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			break
		}
		if e != nil {
			t.Fatal("wait for Oracle HTTP application namespace deletion", e)
		}
		if current.UID != appNamespaceUID || current.Labels["app.kubernetes.io/managed-by"] != "hakopod" || current.Labels["hakopod.io/application-id"] != appNamespaceApplicationLabel {
			t.Fatal("Oracle HTTP application namespace was replaced or its ownership changed")
		}
		time.Sleep(time.Second)
	}
	if applicationDelete.Err() != nil {
		t.Fatal("Oracle HTTP application namespace deletion timed out")
	}
	created = nil
	t.Run("lifecycle", func(t *testing.T) {
		if source.Status != "ready" || target.Status != "ready" || source.Observation.TLS == nil || !source.Observation.TLS.Verified || !source.Observation.TLS.PlaintextRejected {
			t.Fatal("Oracle lifecycle evidence is incomplete")
		}
	})
	t.Run("backup_restore", func(t *testing.T) {
		if artifact.VerifiedAt == nil || recovery.Status != "succeeded" || inspected.Recovery == nil || inspected.Recovery.InspectedAt == nil {
			t.Fatal("Oracle backup and recovery evidence is incomplete")
		}
	})
	t.Run("binding_revocation", func(t *testing.T) {
		if appNamespace == "" {
			t.Fatal("Oracle application binding evidence is incomplete")
		}
	})
	t.Run("deletion", func(t *testing.T) {
		for _, name := range []string{cluster.DatabaseNamespace(source.ID), cluster.DatabaseNamespace(target.ID), appNamespace} {
			if _, err := kube.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatal("owned namespace remains", name)
			}
		}
	})
}
