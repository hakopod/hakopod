package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const liveMinioImage = "quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"
const liveBackupPostgres = "docker.io/library/postgres:17.11-alpine@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73"
const liveBackupMySQL = "docker.io/library/mysql:8.4.11@sha256:85b9bf2e29cf836ecb8c2a15a935d4ba0c606631dff1dd79531a11983c638f2a"

func liveBackupObjectStore(t *testing.T, ctx context.Context) (string, string, string, *s3.Client) {
	t.Helper()
	name := "hakopod-backup-smoke-" + store.NewID()[:10]
	access := "fixture-access"
	secret := store.NewID() + store.NewID()
	command := exec.CommandContext(ctx, "docker", "run", "--rm", "-d", "--name", name, "--label", "com.hakopod.test=backups", "--memory=384m", "--cpus=1", "--pids-limit=128", "-p", "127.0.0.1::9000", "-e", "MINIO_ROOT_USER="+access, "-e", "MINIO_ROOT_PASSWORD="+secret, "-e", "GOMEMLIMIT=256MiB", liveMinioImage, "server", "/data", "--console-address", ":9001")
	if err := command.Run(); err != nil {
		t.Fatal("start disposable S3 server", err)
	}
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := exec.CommandContext(clean, "docker", "rm", "--force", name).Run(); err != nil {
			t.Error("remove disposable S3 container", err)
		}
	})
	port, err := exec.CommandContext(ctx, "docker", "port", name, "9000/tcp").Output()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + strings.TrimSpace(string(port))
	httpClient := &http.Client{Timeout: time.Second}
	for {
		response, err := httpClient.Get(endpoint + "/minio/health/live")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("S3 readiness timed out")
		case <-time.After(250 * time.Millisecond):
		}
	}
	client := s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(endpoint), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(access, secret, ""), RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
	if _, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("hakopod-backup-tests")}); err != nil {
		t.Fatal("create disposable S3 bucket", err)
	}
	return endpoint, access, secret, client
}

type liveBackupDatabase struct {
	application store.Application
	target      cluster.Target
	source      backup.Source
	pod         string
}

func createLiveBackupDatabase(t *testing.T, ctx context.Context, db *store.Store, kube kubernetes.Interface, engine string) liveBackupDatabase {
	t.Helper()
	id := store.NewID()
	name := "backup-" + engine
	image := liveBackupPostgres
	uid := int64(70)
	memory := "256Mi"
	password := store.NewID()
	env := map[string]string{"POSTGRES_USER": "fixture", "POSTGRES_DB": "app", "PGDATA": "/data/pgdata"}
	secrets := map[string]spec.SecretRef{"POSTGRES_PASSWORD": {Ref: "fixture-password"}}
	variables := []corev1.EnvVar{{Name: "POSTGRES_USER", Value: "fixture"}, {Name: "POSTGRES_DB", Value: "app"}, {Name: "PGDATA", Value: "/data/pgdata"}, {Name: "POSTGRES_PASSWORD", Value: password}}
	args := []string{"-c", "shared_buffers=16MB", "-c", "max_connections=12", "-c", "work_mem=1MB"}
	mount := "/data"
	if engine == "mysql" {
		image = liveBackupMySQL
		uid = 999
		memory = "512Mi"
		env = map[string]string{"MYSQL_USER": "fixture", "MYSQL_DATABASE": "app"}
		secrets = map[string]spec.SecretRef{"MYSQL_PASSWORD": {Ref: "fixture-password"}, "MYSQL_ROOT_PASSWORD": {Ref: "fixture-root"}}
		variables = []corev1.EnvVar{{Name: "MYSQL_USER", Value: "fixture"}, {Name: "MYSQL_DATABASE", Value: "app"}, {Name: "MYSQL_PASSWORD", Value: password}, {Name: "MYSQL_ROOT_PASSWORD", Value: password}}
		args = []string{"--socket=/tmp/mysql.sock", "--pid-file=/tmp/mysql.pid", "--innodb-buffer-pool-size=32M", "--innodb-log-buffer-size=4M", "--max-connections=12", "--performance-schema=OFF", "--key-buffer-size=8M", "--temptable-max-ram=16M"}
		mount = "/var/lib/mysql"
	}
	application := store.Application{ID: id, Name: name, Project: "demo", Environment: "development", Revision: 1, Status: "healthy", Spec: spec.Application{SchemaVersion: 1, Name: name, Services: map[string]spec.Service{"db": {Image: image, Env: env, Secrets: secrets, Replicas: 1}}}}
	if _, err := db.Pool.Exec(ctx, "INSERT INTO applications(id,project,environment,name,revision,status,spec) VALUES($1,$2,$3,$4,1,'healthy',$5)", id, application.Project, application.Environment, name, store.JSON(application.Spec)); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(id))
	labels := map[string]string{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/application-id": fmt.Sprintf("%x", hash[:16]), "hakopod.io/acceptance": "backups"}
	ns, err := kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: cluster.Namespace(id), Labels: labels}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		current, err := kube.CoreV1().Namespaces().Get(clean, ns.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return
		}
		if err != nil || current.UID != ns.UID || current.Labels["hakopod.io/acceptance"] != "backups" {
			t.Error("fixture namespace ownership changed; not deleting")
			return
		}
		if err = kube.CoreV1().Namespaces().Delete(clean, ns.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &ns.UID}}); err != nil {
			t.Error(err)
		}
	})
	podLabels := map[string]string{}
	for k, v := range labels {
		podLabels[k] = v
	}
	podLabels["hakopod.io/service"] = "db"
	no := false
	yes := true
	grace := int64(5)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: ns.Name, Labels: podLabels}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &no, TerminationGracePeriodSeconds: &grace, SecurityContext: &corev1.PodSecurityContext{RunAsUser: &uid, RunAsGroup: &uid, RunAsNonRoot: &yes, FSGroup: &uid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{{Name: "app", Image: image, Args: args, Env: variables, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("96Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse(memory)}}, VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: mount}}}}, Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resource.NewQuantity(256<<20, resource.BinarySI)}}}}}}
	if _, err = kube.CoreV1().Pods(ns.Name).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	return liveBackupDatabase{application: application, target: cluster.Target{ApplicationID: id, Project: application.Project, Environment: application.Environment, Revision: 1, Spec: application.Spec}, source: backup.Source{Kind: "database", Engine: engine, ApplicationID: id, Service: "db", Database: "app"}, pod: pod.Name}
}
func liveBackupSQL(t *testing.T, ctx context.Context, c *cluster.Client, d liveBackupDatabase, database, sql string) (string, error) {
	t.Helper()
	options, uid, err := c.BackupPod(ctx, d.target, "db")
	if err != nil {
		return "", err
	}
	script := `export PGPASSWORD="$POSTGRES_PASSWORD"; exec psql --no-psqlrc --set=ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$1" -Atc "$2"`
	if d.source.Engine == "mysql" {
		script = `export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql --protocol=TCP -h 127.0.0.1 -u root --batch --skip-column-names --database="$1" --execute="$2"`
	}
	options.Command = []string{"sh", "-c", script, "fixture-sql", database, sql}
	var output, diagnostics bytes.Buffer
	err = c.BackupExec(ctx, d.target, "db", options, uid, nil, &output, &diagnostics)
	if err != nil {
		err = fmt.Errorf("%w: %s", err, diagnostics.String())
	}
	if output.Len() > 64<<10 {
		t.Fatal("SQL fixture output exceeded limit")
	}
	return strings.TrimSpace(output.String()), err
}

func TestLiveEncryptedDatabaseBackupsAndRestores(t *testing.T) {
	if os.Getenv("HAKOPOD_BACKUP_TEST") != "1" {
		t.Skip("set HAKOPOD_BACKUP_TEST=1, HAKOPOD_TEST_DATABASE_URL and named dev kubeconfig for disposable S3/PG/MySQL acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("backup acceptance requires explicit k3d-hakopod-dev context")
	}
	restConfig, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	kube, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := cluster.New(path, cluster.Options{AppDomain: "127.0.0.1.sslip.io"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	db, dsn := database(t)
	raw, err := db.Bootstrap(ctx, "backup-live")
	if err != nil {
		t.Fatal(err)
	}
	endpoint, access, secret, s3client := liveBackupObjectStore(t, ctx)
	management := &api.Server{Store: db, Cluster: runtime, Auth: api.AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{11}, 32))}}
	state := t.TempDir()
	if err = os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	management.ConfigureBackups(api.BackupConfig{DatabaseURL: dsn, StateDir: state, MaxBytes: 64 << 20})
	server := httptest.NewServer(management.Handler())
	defer server.Close()
	client := backupRequestClient{t, server, raw}
	var created struct {
		Destination backup.Destination `json:"destination"`
		RecoveryKey string             `json:"recovery_key"`
	}
	if status := client.request("POST", "/backup-destinations", backup.DestinationInput{Name: "disposable-minio", Endpoint: endpoint, Region: "us-east-1", Bucket: "hakopod-backup-tests", Prefix: "acceptance", PathStyle: true, AllowHTTP: true, AccessKeyID: access, SecretAccessKey: secret}, &created, ""); status != 201 {
		t.Fatal("create real destination", status)
	}
	if status := client.request("POST", "/backup-destinations/"+created.Destination.ID+"/test", map[string]any{}, nil, ""); status != 200 {
		t.Fatal("real S3 write/read/delete test", status)
	}
	fixtures := []liveBackupDatabase{createLiveBackupDatabase(t, ctx, db, kube, "postgresql"), createLiveBackupDatabase(t, ctx, db, kube, "mysql")}
	var postgresArtifact backup.Artifact
	var postgresRestored string
	for _, fixture := range fixtures {
		ready, done := context.WithTimeout(ctx, 4*time.Minute)
		for {
			_, err = liveBackupSQL(t, ready, runtime, fixture, "app", "SELECT 1")
			if err == nil {
				break
			}
			select {
			case <-ready.Done():
				done()
				t.Fatalf("%s non-root database never became ready", fixture.source.Engine)
			case <-time.After(time.Second):
			}
		}
		done()
		sql := "CREATE TABLE backup_rows(id integer PRIMARY KEY, payload text); INSERT INTO backup_rows SELECT i,repeat(md5(i::text),300) FROM generate_series(1,1200) AS i"
		if fixture.source.Engine == "mysql" {
			sql = "CREATE TABLE backup_rows(id integer PRIMARY KEY,payload text); INSERT INTO backup_rows VALUES (1,'mysql-backed-up'),(2,'second-row')"
		}
		if _, err = liveBackupSQL(t, ctx, runtime, fixture, "app", sql); err != nil {
			t.Fatal("seed real fixture", fixture.source.Engine, err)
		}
		var job backup.Job
		if status := client.request("POST", "/backups", map[string]any{"destination_id": created.Destination.ID, "source": fixture.source}, &job, "live-backup-"+fixture.source.Engine); status != 202 {
			t.Fatal("queue database backup", fixture.source.Engine, status)
		}
		if err = management.Backups.RunOnce(ctx); err != nil {
			t.Fatal("run backup", err)
		}
		job, err = db.BackupJob(ctx, job.ID)
		if err != nil || job.Status != "succeeded" {
			t.Fatal("real backup did not succeed", fixture.source.Engine, job.Status, job.Error, err)
		}
		artifact, err := db.BackupArtifact(ctx, job.ArtifactID)
		if err != nil {
			t.Fatal(err)
		}
		if fixture.source.Engine == "postgresql" && artifact.Bytes < backup.PartSize {
			t.Fatal("PostgreSQL fixture did not exercise multipart upload")
		}
		if _, err = s3client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(created.Destination.Bucket), Key: aws.String(artifact.ObjectKey + ".json")}); err != nil {
			t.Fatal("standalone recovery manifest missing", err)
		}
		var plan backup.RestorePlan
		if status := client.request("POST", "/backup-artifacts/"+artifact.ID+"/restore-plan", map[string]string{"application_id": fixture.application.ID, "service": "db"}, &plan, ""); status != 200 {
			t.Fatal("review restore", status)
		}
		if status := client.request("POST", "/backup-artifacts/"+artifact.ID+"/restore", map[string]string{"plan_id": plan.ID, "confirmation": "wrong"}, nil, "wrong-confirmation"); status != 400 {
			t.Fatal("restore accepted wrong confirmation", status)
		}
		var restore backup.Job
		if status := client.request("POST", "/backup-artifacts/"+artifact.ID+"/restore", map[string]string{"plan_id": plan.ID, "confirmation": plan.Confirmation}, &restore, "live-restore-"+fixture.source.Engine); status != 202 {
			t.Fatal("queue restore", status)
		}
		if err = management.Backups.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		restore, err = db.BackupJob(ctx, restore.ID)
		if err != nil || restore.Status != "succeeded" {
			t.Fatal("real restore did not succeed", fixture.source.Engine, restore.Status, restore.Error, err)
		}
		count, err := liveBackupSQL(t, ctx, runtime, fixture, plan.Target.Database, "SELECT count(*) FROM backup_rows")
		expected := 2
		if fixture.source.Engine == "postgresql" {
			expected = 1200
		}
		if err != nil || count != strconv.Itoa(expected) {
			t.Fatal("restored data mismatch", count, err)
		}
		original, err := liveBackupSQL(t, ctx, runtime, fixture, "app", "SELECT count(*) FROM backup_rows")
		if err != nil || original != count {
			t.Fatal("source database was changed by restore", err)
		}
		if fixture.source.Engine == "postgresql" {
			postgresArtifact = artifact
			postgresRestored = plan.Target.Database
		}
		t.Logf("%s: actual encrypted object %d bytes, fresh database restore %d rows; original database unchanged", fixture.source.Engine, artifact.Bytes, expected)
	}
	// The management database is external to Kubernetes in this test. pg_dump
	// reads that real temporary database; restore targets only the new fixture DB.
	var managementJob backup.Job
	if status := client.request("POST", "/backups", map[string]any{"destination_id": created.Destination.ID, "source": backup.Source{Kind: "management", Engine: "postgresql"}}, &managementJob, "live-management-backup"); status != 202 {
		t.Fatal("management backup queue", status)
	}
	if err = management.Backups.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	managementJob, err = db.BackupJob(ctx, managementJob.ID)
	if err != nil || managementJob.Status != "succeeded" {
		t.Fatal("external management dump", managementJob.Error, err)
	}
	var managementPlan backup.RestorePlan
	if status := client.request("POST", "/backup-artifacts/"+managementJob.ArtifactID+"/restore-plan", map[string]string{"application_id": fixtures[0].application.ID, "service": "db"}, &managementPlan, ""); status != 200 {
		t.Fatal(status)
	}
	var managementRestore backup.Job
	if status := client.request("POST", "/backup-artifacts/"+managementJob.ArtifactID+"/restore", map[string]string{"plan_id": managementPlan.ID, "confirmation": managementPlan.Confirmation}, &managementRestore, "live-management-restore"); status != 202 {
		t.Fatal(status)
	}
	if err = management.Backups.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	managementRestore, err = db.BackupJob(ctx, managementRestore.ID)
	if err != nil || managementRestore.Status != "succeeded" {
		t.Fatal("management restore", managementRestore.Error, err)
	}
	count, err := liveBackupSQL(t, ctx, runtime, fixtures[0], managementPlan.Target.Database, "SELECT count(*) FROM identities WHERE name='backup-live'")
	if err != nil || count != "1" {
		t.Fatal("management accounts not restored into isolated target", count, err)
	}
	t.Log("external management PostgreSQL backup/restore preserved the fixture account in a separate database; active management connection unchanged")
	for _, scenario := range []string{"expired", "replaced-pod", "existing-database", "corrupt-object"} {
		t.Log("checking restore refusal:", scenario)
		var plan backup.RestorePlan
		if status := client.request("POST", "/backup-artifacts/"+postgresArtifact.ID+"/restore-plan", map[string]string{"application_id": fixtures[0].application.ID, "service": "db"}, &plan, ""); status != 200 {
			t.Fatal("negative restore review", scenario, status)
		}
		switch scenario {
		case "expired":
			if _, err = db.Pool.Exec(ctx, "UPDATE backup_restore_plans SET expires_at=now()-interval '1 second' WHERE id=$1", plan.ID); err != nil {
				t.Fatal(err)
			}
		case "replaced-pod":
			plan.Target.PodUID = "no-longer-the-reviewed-pod"
			if _, err = db.Pool.Exec(ctx, "UPDATE backup_restore_plans SET plan=$2 WHERE id=$1", plan.ID, store.JSON(plan)); err != nil {
				t.Fatal(err)
			}
		case "existing-database":
			plan.Target.Database = postgresRestored
			plan.Confirmation = postgresRestored
			if _, err = db.Pool.Exec(ctx, "UPDATE backup_restore_plans SET plan=$2 WHERE id=$1", plan.ID, store.JSON(plan)); err != nil {
				t.Fatal(err)
			}
		case "corrupt-object":
			object, e := s3client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(created.Destination.Bucket), Key: aws.String(postgresArtifact.ObjectKey)})
			if e != nil {
				t.Fatal(e)
			}
			file, e := os.CreateTemp(t.TempDir(), "corrupt-encrypted-backup")
			if e != nil {
				t.Fatal(e)
			}
			n, e := io.CopyBuffer(file, object.Body, make([]byte, 64<<10))
			object.Body.Close()
			if e != nil {
				file.Close()
				t.Fatal(e)
			}
			if _, e = file.Seek(-1, io.SeekEnd); e != nil {
				t.Fatal(e)
			}
			last := []byte{0}
			if _, e = file.Read(last); e != nil {
				t.Fatal(e)
			}
			last[0] ^= 1
			if _, e = file.Seek(-1, io.SeekEnd); e != nil {
				t.Fatal(e)
			}
			if _, e = file.Write(last); e != nil {
				t.Fatal(e)
			}
			if _, e = file.Seek(0, io.SeekStart); e != nil {
				t.Fatal(e)
			}
			_, e = s3client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(created.Destination.Bucket), Key: aws.String(postgresArtifact.ObjectKey), Body: file, ContentLength: aws.Int64(n)})
			file.Close()
			if e != nil {
				t.Fatal(e)
			}
		}
		var job backup.Job
		status := client.request("POST", "/backup-artifacts/"+postgresArtifact.ID+"/restore", map[string]string{"plan_id": plan.ID, "confirmation": plan.Confirmation}, &job, "refuse-restore-"+scenario)
		if scenario == "expired" {
			if status != 409 {
				t.Fatal("expired review accepted", status)
			}
			t.Log("restore refusal verified:", scenario)
			continue
		}
		if status != 202 {
			t.Fatal("negative restore enqueue", scenario, status)
		}
		if err = management.Backups.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		job, err = db.BackupJob(ctx, job.ID)
		if err != nil || job.Status != "failed" {
			t.Fatal("unsafe restore did not fail", scenario, job.Status, err)
		}
		if scenario == "existing-database" {
			count, err = liveBackupSQL(t, ctx, runtime, fixtures[0], postgresRestored, "SELECT count(*) FROM backup_rows")
			if err != nil || count != "1200" {
				t.Fatal("existing database was modified", err)
			}
		} else {
			count, err = liveBackupSQL(t, ctx, runtime, fixtures[0], "app", "SELECT count(*) FROM pg_database WHERE datname='"+plan.Target.Database+"'")
			if err != nil || count != "0" {
				t.Fatal("failed restore created an unexpected database", scenario, count, err)
			}
		}
		t.Log("restore refusal verified:", scenario)
	}
	t.Log("real restore refuses expired review, stale pod UID, existing database and corrupted S3 object before unintended database mutation")
	files, err := os.ReadDir(state)
	if err != nil || len(files) != 0 {
		t.Fatal("encrypted restore staging was not cleaned", err)
	}
}
