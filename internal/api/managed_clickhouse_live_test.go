package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/cluster"
	managed "github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type clickhouseAPIOutput struct {
	buffer bytes.Buffer
}

func (w *clickhouseAPIOutput) Write(p []byte) (int, error) {
	if len(p) > (64<<10)-w.buffer.Len() {
		return 0, fmt.Errorf("ClickHouse query output exceeded its bound")
	}
	return w.buffer.Write(p)
}

func (w *clickhouseAPIOutput) String() string {
	return w.buffer.String()
}

var clickhouseAPIDiagnosticPattern = regexp.MustCompile(`^stage=(password_read|password_format|query_stage|config_write|client) exit=[0-9]{1,3} engine_code=(none|[0-9]{1,6})$`)

func clickhouseAPISafeDiagnostic(output string) string {
	diagnostic := strings.TrimSpace(output)
	if !clickhouseAPIDiagnosticPattern.MatchString(diagnostic) {
		return "stage=exec diagnostic=unavailable"
	}
	return diagnostic
}

func clickhouseAPIJobFailureCode(message string) string {
	switch message {
	case "S3 object upload failed":
		return "s3_object_upload"
	case "S3 multipart creation failed", "S3 multipart part upload failed", "S3 multipart completion failed":
		return "s3_multipart_upload"
	case "S3 object download failed":
		return "s3_object_download"
	case "backup producer failed", "database dump did not complete":
		return "archive_producer"
	case "uploaded database archive could not be verified":
		return "archive_verification"
	case "backup manifest upload failed; data cleanup was attempted":
		return "manifest_upload"
	case "managed database is not ready for backup", "managed database health could not be verified":
		return "source_health"
	case "managed database changed before backup", "managed database topology changed before backup":
		return "source_changed"
	case "managed recovery target changed", "managed recovery is not owned by the active backup job", "managed recovery target is not healthy":
		return "recovery_target"
	case "backup exceeds configured object size limit":
		return "archive_size"
	default:
		return "unclassified"
	}
}

func TestClickhouseAPIJobFailureDiagnosticsAreCredentialSafe(t *testing.T) {
	if got := clickhouseAPIJobFailureCode("S3 object upload failed"); got != "s3_object_upload" {
		t.Fatal("known upload failure lost its classification")
	}
	for _, message := range []string{"password=private", "S3 object upload failed: secret", strings.Repeat("private", 10000)} {
		if got := clickhouseAPIJobFailureCode(message); got != "unclassified" {
			t.Fatal("unexpected job error was not redacted")
		}
	}
}

func TestClickhouseAPIOutputBound(t *testing.T) {
	var output clickhouseAPIOutput
	reader := io.LimitReader(strings.NewReader(strings.Repeat("x", (64<<10)+1)), (64<<10)+1)
	if _, err := io.Copy(&output, reader); err == nil {
		t.Fatal("oversized ClickHouse output was accepted")
	}
	if output.buffer.Len() > 64<<10 {
		t.Fatal("oversized ClickHouse output was buffered")
	}
}

func TestClickhouseAPIQueryDiagnosticsAreCredentialSafe(t *testing.T) {
	directory := t.TempDir()
	client := directory + "/clickhouse-client"
	script := `#!/bin/sh
set -eu
case "${CLICKHOUSE_TEST_RESULT:-}" in
 success) printf 'query-result\n' ;;
	 failure) printf 'Code: 516. DB::Exception: password=private query=secret\n' >&2; exit 17 ;;
	 oversize) head -c 70000 /dev/zero ;;
 *) exit 19 ;;
esac
`
	if err := os.WriteFile(client, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	run := func(result string) (string, error) {
		command := exec.Command("bash", "-c", clickhouseAPIQuery, "clickhouse-api-query", "database.example.test")
		command.Env = append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH"), "CLICKHOUSE_TEST_RESULT="+result)
		command.Stdin = strings.NewReader(strings.Repeat("a", 64) + "\nSELECT 'private-query'\n")
		var output clickhouseAPIOutput
		command.Stdout = &output
		command.Stderr = io.Discard
		err := command.Run()
		return strings.TrimSpace(output.String()), err
	}
	if output, err := run("success"); err != nil || output != "query-result" {
		t.Fatal("successful diagnostic fixture failed")
	}
	output, err := run("failure")
	if err == nil || output != "stage=client exit=17 engine_code=516" || strings.Contains(output, "private") || strings.Contains(output, "secret") {
		t.Fatal("failed query did not return the bounded credential-safe diagnostic")
	}
	output, err = run("oversize")
	if err == nil || len(output) > 128 || !clickhouseAPIDiagnosticPattern.MatchString(output) {
		t.Fatal("oversized client output was not reduced to a bounded diagnostic")
	}
	for _, unsafe := range []string{"query-row", "query-row\nstage=client exit=1 engine_code=516", "stage=other exit=1 engine_code=516", strings.Repeat("1", 65<<10)} {
		if got := clickhouseAPISafeDiagnostic(unsafe); got != "stage=exec diagnostic=unavailable" {
			t.Fatal("unexpected or partial output was not redacted")
		}
	}
}

const clickhouseAPIQuery = `set -eu
umask 077
if ! IFS= read -r password; then
  printf 'stage=password_read exit=1 engine_code=none\n'
  exit 1
fi
if [[ ! "$password" =~ ^[a-f0-9]{64}$ ]]; then
  printf 'stage=password_format exit=1 engine_code=none\n'
  exit 1
fi
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
ulimit -f 64
if ! cat > "$work/query.sql"; then
  printf 'stage=query_stage exit=1 engine_code=none\n'
  exit 1
fi
if ! cat > "$work/client.xml" <<EOF
<config><host>127.0.0.1</host><port>9440</port><tls-sni-override>$1</tls-sni-override><database>app</database><user>app</user><password>$password</password><secure>true</secure><connect_timeout>3</connect_timeout><receive_timeout>20</receive_timeout><send_timeout>20</send_timeout><send_logs_level>none</send_logs_level><openSSL><client><caConfig>/etc/hakopod-client-tls/ca.crt</caConfig><verificationMode>strict</verificationMode><extendedVerification>true</extendedVerification><loadDefaultCAFile>false</loadDefaultCAFile><invalidCertificateHandler><name>RejectCertificateHandler</name></invalidCertificateHandler></client></openSSL></config>
EOF
then
  printf 'stage=config_write exit=1 engine_code=none\n'
  exit 1
fi
unset password
if clickhouse-client --config-file="$work/client.xml" --queries-file="$work/query.sql" > "$work/result" 2> "$work/error"; then
  cat "$work/result"
else
  status=$?
  code=$(sed -n 's/.*Code: \([0-9][0-9]*\)\..*/\1/p' "$work/error" | head -n 1)
  printf 'stage=client exit=%s engine_code=%s\n' "$status" "${code:-none}"
  exit "$status"
fi
`

func clickhouseAPIPreflight(t *testing.T, ctx context.Context, kube kubernetes.Interface) []string {
	t.Helper()
	names := strings.Split(os.Getenv("HAKOPOD_CLICKHOUSE_FAULT_NODES"), ",")
	lease := os.Getenv("HAKOPOD_CLICKHOUSE_LEASE_ID")
	parsedLease, err := uuid.Parse(lease)
	if len(names) == 0 || len(names) > 4 || err != nil || parsedLease.String() != lease {
		t.Fatal("exact ClickHouse node and lease inventory is required")
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] || !regexp.MustCompile(`^k3d-hakopod-clickhouse-worker-[0-9]+$`).MatchString(name) {
			t.Fatal("invalid ClickHouse node inventory")
		}
		seen[name] = true
		node, err := kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal("ClickHouse node runtime attestation is unavailable")
		}
		ready := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if !ready || node.Spec.Unschedulable || node.Labels["hakopod.com/pool"] != "clickhouse-acceptance" || node.Labels["hakopod.com.node-restriction.kubernetes.io/clickhouse-runtime"] != "systrap-no-patching-v1" {
			t.Fatal("ClickHouse node runtime attestation is unavailable")
		}
		raw, err := exec.CommandContext(ctx, "sudo", "docker", "inspect", name, "--format", "{{json .Config.Labels}}").Output()
		var labels map[string]string
		if err != nil || json.Unmarshal(raw, &labels) != nil || labels["k3d.cluster"] != "hakopod-dev" || labels["k3d.role"] != "agent" || labels["com.hakopod.lease-id"] != lease {
			t.Fatal("ClickHouse node lease identity changed")
		}
	}
	runtime, err := kube.NodeV1().RuntimeClasses().Get(ctx, "hakopod-clickhouse", metav1.GetOptions{})
	if err != nil || runtime.Handler != "hakopod-clickhouse" || runtime.Annotations["hakopod.com.node-restriction.kubernetes.io/clickhouse-runtime"] != "systrap-no-patching-v1" {
		t.Fatal("ClickHouse RuntimeClass is unavailable")
	}
	return names
}

func TestLiveManagedClickHouseAPIRecovery(t *testing.T) {
	if os.Getenv("HAKOPOD_CLICKHOUSE_API_RECOVERY_TEST") != "1" {
		t.Skip("set HAKOPOD_CLICKHOUSE_API_RECOVERY_TEST=1 for the owned development acceptance environment")
	}
	if os.Getenv("HAKOPOD_TEST_DATABASE_URL") == "" {
		t.Fatal("HAKOPOD_TEST_DATABASE_URL is required for disposable control-plane PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("ClickHouse API recovery requires k3d-hakopod-dev")
	}
	rest, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	kube, err := kubernetes.NewForConfig(rest)
	if err != nil {
		t.Fatal(err)
	}
	nodes := clickhouseAPIPreflight(t, ctx, kube)
	runtime, err := cluster.New(path, cluster.Options{ClickHouseSandbox: true})
	if err != nil {
		t.Fatal(err)
	}
	db, dsn := database(t)
	token, err := db.Bootstrap(ctx, "clickhouse-api-recovery")
	if err != nil {
		t.Fatal(err)
	}
	endpoint, access, secret, objectClient := liveBackupObjectStore(t, ctx)
	server := &api.Server{Store: db, Cluster: runtime, Auth: api.AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{23}, 32))}}
	state := t.TempDir()
	if err = os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	server.ConfigureBackups(api.BackupConfig{DatabaseURL: dsn, StateDir: state, MaxBytes: 64 << 20})
	httpServer := httptest.NewServer(server.Handler())
	httpServer.Client().Timeout = 30 * time.Second
	defer httpServer.Close()
	client := backupRequestClient{t, httpServer, token}
	runCtx, stopWorkers := context.WithCancel(ctx)
	workersDone := make(chan struct{})
	go func() { defer close(workersDone); server.RunManagedDatabases(runCtx) }()
	defer func() {
		stopWorkers()
		select {
		case <-workersDone:
		case <-time.After(10 * time.Second):
			t.Error("database workers did not stop")
		}
	}()

	var destination struct {
		Destination backup.Destination `json:"destination"`
	}
	if status := client.request("POST", "/backup-destinations", backup.DestinationInput{Name: "clickhouse-api-recovery", Endpoint: endpoint, Region: "us-east-1", Bucket: "hakopod-backup-tests", Prefix: "clickhouse-api", PathStyle: true, AllowHTTP: true, AccessKeyID: access, SecretAccessKey: secret}, &destination, ""); status != 201 {
		t.Fatal("destination", status)
	}

	var created []managed.Resource
	cleanup := func() {
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
		spec := managed.Spec{SchemaVersion: 1, Name: name, Engine: "clickhouse", Version: "26.3", Mode: "standalone", Shards: 1, CPU: "500m", Memory: "2Gi", StorageGiB: 2, Placement: managed.Placement{NodeNames: append([]string(nil), nodes...)}, TLS: &managed.TLSConfig{Mode: "required"}}
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
				if status := client.request("GET", "/databases/"+operation.DatabaseID, nil, &resource, ""); status != 200 || resource.Status != "ready" || !resource.Observation.Fresh(time.Now(), resource.Revision) || resource.Observation.TLS == nil || !resource.Observation.TLS.Verified || len(resource.Observation.Members) != 1 {
					t.Fatal("database readiness was not observed")
				}
				return resource, operation
			}
			time.Sleep(time.Second)
		}
		t.Fatal("database creation timed out")
		return managed.Resource{}, managed.Operation{}
	}

	source, _ := create("clickhouse-api-source", "clickhouse-api-source-create")
	target, _ := create("clickhouse-api-target", "clickhouse-api-target-create")

	query := func(d managed.Resource, stage, sql string) string {
		var credentials map[string]string
		if status := client.request("POST", "/databases/"+d.ID+"/credentials", map[string]any{}, &credentials, ""); status != 200 {
			t.Fatal("credentials", status)
		}
		member := d.Observation.Members[0]
		host := strings.TrimSuffix(member.Name, "-0") + "." + cluster.DatabaseNamespace(d.ID) + ".svc.cluster.local"
		input := bytes.NewBufferString(credentials["password"] + "\n" + sql + "\n")
		var output clickhouseAPIOutput
		step, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		if err := runtime.DatabaseExec(step, d, member, []string{"bash", "-c", clickhouseAPIQuery, "clickhouse-api-query", host}, input, &output); err != nil {
			diagnostic := clickhouseAPISafeDiagnostic(output.String())
			t.Fatalf("ClickHouse app query failed at %s (%v; %s)", stage, err, diagnostic)
		}
		return strings.TrimSpace(output.String())
	}
	query(source, "source_create", "CREATE TABLE api_recovery (id UInt64, value String) ENGINE=MergeTree ORDER BY id")
	query(source, "source_insert_initial", "INSERT INTO api_recovery VALUES (1, unhex('0080FF0D0A'))")
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
			t.Fatalf("ClickHouse %s failed code=%s state=unavailable", stage, code)
		}
		t.Fatalf("ClickHouse %s failed code=%s database=%s observation=%s maintenance_active=%t", stage, code, databaseStatus, observedStatus, maintenanceActive)
	}

	var job backup.Job
	if status, code := client.requestCode("POST", "/backups", map[string]any{"destination_id": destination.Destination.ID, "source": backup.Source{Kind: "managed_database", ManagedDatabaseID: source.ID, Engine: "clickhouse"}}, &job, "clickhouse-api-backup"); status != 202 {
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
		t.Fatal("ClickHouse backup timed out", job.ID, job.Status)
	}
	artifact, err := db.BackupArtifact(ctx, job.ArtifactID)
	if err != nil || artifact.Source.Engine != "clickhouse" || artifact.Source.ManagedDatabaseID != source.ID || artifact.SourceRevision != source.Revision || artifact.VerifiedAt == nil {
		t.Fatal("ClickHouse backup artifact is incomplete")
	}
	query(source, "source_insert_after_backup", "INSERT INTO api_recovery VALUES (2, unhex('01027F'))")

	var plan backup.RestorePlan
	if status := client.request("POST", "/databases/"+target.ID+"/restore-plan", map[string]string{"artifact_id": artifact.ID}, &plan, ""); status != 200 {
		t.Fatal("restore plan", status)
	}
	var recovery backup.Job
	if status := client.request("POST", "/backup-artifacts/"+artifact.ID+"/restore", map[string]string{"plan_id": plan.ID, "confirmation": target.Spec.Name}, &recovery, "clickhouse-api-restore"); status != 202 {
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
		t.Fatal("ClickHouse restore timed out", recovery.ID, recovery.Status)
	}
	if err = db.RefreshDatabaseRecoveries(ctx); err != nil {
		t.Fatal(err)
	}
	if query(target, "target_verify_restore", "SELECT id, hex(value) FROM api_recovery ORDER BY id FORMAT TSV") != "1\t0080FF0D0A" || query(source, "source_verify_preserved", "SELECT id, hex(value) FROM api_recovery ORDER BY id FORMAT TSV") != "1\t0080FF0D0A\n2\t01027F" {
		t.Fatal("ClickHouse recovery point or source preservation changed")
	}
	var inspected managed.Resource
	if status := client.request("POST", "/databases/"+target.ID+"/inspect", map[string]any{"job_id": recovery.ID, "expected_revision": target.Revision, "confirm_name": target.Spec.Name, "inspected": true}, &inspected, ""); status != 200 || inspected.Recovery == nil || inspected.Recovery.InspectedAt == nil {
		t.Fatal("recovery inspection", status)
	}

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
		if status := client.request("DELETE", "/databases/"+d.ID, map[string]any{"expected_revision": d.Revision, "confirm_name": d.Spec.Name}, &operation, "clickhouse-api-delete-"+d.ID); status != 202 {
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
	created = nil
}
