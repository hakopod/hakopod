package cluster

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func copyLookup(t *testing.T, addrs ...string) {
	t.Helper()
	previous := databaseCopyLookup
	t.Cleanup(func() { databaseCopyLookup = previous })
	databaseCopyLookup = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		out := []netip.Addr{}
		for _, a := range addrs {
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}
}

func copyRequest(engine, version, source string) CopyRequest {
	d := database.Resource{ID: "db1", Project: "p", Environment: "production", Revision: 3, Spec: database.Spec{Engine: engine, Version: version, Mode: "standalone"}}
	host, port := "database.hdb-db1.svc", 6379
	if engine == "postgresql" {
		d.Spec.Postgres = &database.PostgresConfig{Database: "shop", Username: "shop_owner"}
		host, port = "database-rw.hdb-db1.svc", 5432
	}
	return CopyRequest{ApplicationID: "app1", Database: d, Observation: database.Observation{Status: "ready", Revision: 3, Endpoints: []database.Endpoint{{Purpose: "read_write", Host: host, Port: port}}}, SourceURL: source, SourceVersion: "16"}
}

func TestStartDatabaseCopyPostgresShapes(t *testing.T) {
	copyLookup(t, "34.120.1.2", "34.120.1.3")
	kube := fake.NewClientset()
	c := &Client{kube: kube}
	id, err := c.StartDatabaseCopy(context.Background(), copyRequest("postgresql", "17", "postgresql://neondb_owner:s3cret@ep-cool.eu-central-1.aws.neon.tech/neondb?sslmode=require&channel_binding=require&host=10.0.0.5&sslrootcert=/etc/passwd"))
	if err != nil {
		t.Fatal(err)
	}
	ns, name, _ := strings.Cut(string(id), "/")
	if ns != "db1" || !strings.HasPrefix(name, "copy-") {
		t.Fatal("unexpected id", id)
	}
	job, err := kube.BatchV1().Jobs("hdb-db1").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if job.Spec.Suspend == nil || *job.Spec.Suspend {
		t.Fatal("job was not started after its secret and policy existed")
	}
	if *job.Spec.BackoffLimit != 0 || *job.Spec.ActiveDeadlineSeconds != 6*3600 || job.Spec.TTLSecondsAfterFinished == nil || job.Annotations[databaseCopyApp] != "app1" {
		t.Fatal("job limits", job.Spec)
	}
	pod := job.Spec.Template.Spec
	if job.Spec.Template.Labels[databaseRecoveryHelper] != "true" || job.Spec.Template.Labels[databaseCopyLabel] != name || job.Spec.Template.Labels[databaseOwner] != "db1" {
		t.Fatal("pod labels", job.Spec.Template.Labels)
	}
	if *pod.AutomountServiceAccountToken || !*pod.SecurityContext.RunAsNonRoot || *pod.SecurityContext.RunAsUser != 26 || pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatal("pod security", pod.SecurityContext)
	}
	if len(pod.HostAliases) != 1 || pod.HostAliases[0].IP != "34.120.1.2" || pod.HostAliases[0].Hostnames[0] != "ep-cool.eu-central-1.aws.neon.tech" {
		t.Fatal("source host is not pinned to the validated address", pod.HostAliases)
	}
	container := pod.Containers[0]
	if container.Image != databaseImages["postgresql:17"] || !strings.Contains(container.Image, "@sha256:") {
		t.Fatal("image", container.Image)
	}
	sc := container.SecurityContext
	if !*sc.ReadOnlyRootFilesystem || *sc.AllowPrivilegeEscalation || sc.Capabilities.Drop[0] != "ALL" || container.Resources.Limits.Cpu().IsZero() || container.Resources.Limits.Memory().IsZero() {
		t.Fatal("container security or limits", sc, container.Resources)
	}
	for _, arg := range append(append([]string{}, container.Command...), container.Args...) {
		if strings.Contains(arg, "s3cret") || strings.Contains(arg, "neon.tech") {
			t.Fatal("source URL in container arguments")
		}
	}
	if !strings.Contains(container.Command[2], "pg_dump --dbname=\"$SOURCE_URL\" --format=custom") || !strings.Contains(container.Command[2], "pg_restore") {
		t.Fatal("copy command", container.Command)
	}
	env := map[string]corev1.EnvVar{}
	for _, e := range container.Env {
		env[e.Name] = e
		if strings.Contains(e.Value, "s3cret") {
			t.Fatal("source credential in a plain environment value")
		}
	}
	if ref := env["SOURCE_URL"].ValueFrom.SecretKeyRef; ref.Name != name || ref.Key != "SOURCE_URL" {
		t.Fatal("source URL reference", ref)
	}
	if ref := env["TARGET_PASSWORD"].ValueFrom.SecretKeyRef; ref.Name != "database-credentials" || ref.Key != "password" {
		t.Fatal("target password reference", ref)
	}
	if env["TARGET_HOST"].Value != "database-rw.hdb-db1.svc" || env["TARGET_USER"].Value != "shop_owner" || env["TARGET_DATABASE"].Value != "shop" {
		t.Fatal("target environment", env)
	}

	secret, err := kube.CoreV1().Secrets("hdb-db1").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := secret.StringData["SOURCE_URL"]; got != "postgresql://neondb_owner:s3cret@ep-cool.eu-central-1.aws.neon.tech/neondb?channel_binding=require&sslmode=require" {
		t.Fatal("source URL must keep TLS parameters and drop redirects", got)
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Name != name || secret.OwnerReferences[0].Kind != "Job" {
		t.Fatal("secret is not owned by the job", secret.OwnerReferences)
	}

	policy, err := kube.NetworkingV1().NetworkPolicies("hdb-db1").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Spec.PodSelector.MatchLabels[databaseCopyLabel] != name || len(policy.Spec.PolicyTypes) != 2 || len(policy.Spec.Ingress) != 0 || policy.OwnerReferences[0].Name != name {
		t.Fatal("policy selection", policy.Spec)
	}
	egress := policy.Spec.Egress
	if len(egress) != 3 || egress[0].Ports[0].Port.IntValue() != 53 || egress[1].To[0].PodSelector.MatchExpressions[0].Key != databaseRecoveryHelper || egress[1].Ports[0].Port.IntValue() != 5432 {
		t.Fatal("policy DNS and target rules", egress)
	}
	if egress[2].To[0].IPBlock.CIDR != "34.120.1.2/32" || egress[2].Ports[0].Port.IntValue() != 5432 {
		t.Fatal("policy source rule", egress[2])
	}
}

func TestStartDatabaseCopyRedisUpstash(t *testing.T) {
	copyLookup(t, "2600:1f18::10")
	kube := fake.NewClientset()
	c := &Client{kube: kube}
	id, err := c.StartDatabaseCopy(context.Background(), copyRequest("redis", "8", "rediss://default:upstash-pw@eu1-fine-cat-1234.upstash.io:6380"))
	if err != nil {
		t.Fatal(err)
	}
	_, name, _ := strings.Cut(string(id), "/")
	job, _ := kube.BatchV1().Jobs("hdb-db1").Get(context.Background(), name, metav1.GetOptions{})
	container := job.Spec.Template.Spec.Containers[0]
	if container.Image != databaseImages["redis:8"] || *job.Spec.Template.Spec.SecurityContext.RunAsUser != 1000 {
		t.Fatal("redis image", container.Image)
	}
	env := map[string]string{}
	for _, e := range container.Env {
		env[e.Name] = e.Value
	}
	if env["SOURCE_HOST"] != "eu1-fine-cat-1234.upstash.io" || env["SOURCE_PORT"] != "6380" || env["SOURCE_USER"] != "default" || env["SOURCE_TLS"] != "true" || env["TARGET_CLUSTER"] != "false" {
		t.Fatal("redis environment", env)
	}
	if !strings.Contains(container.Command[2], "DUMP") || !strings.Contains(container.Command[2], "RESTORE") {
		t.Fatal("redis copy command")
	}
	secret, _ := kube.CoreV1().Secrets("hdb-db1").Get(context.Background(), name, metav1.GetOptions{})
	if len(secret.StringData) != 1 || secret.StringData["SOURCE_PASSWORD"] != "upstash-pw" {
		t.Fatal("redis secret", secret.StringData)
	}
	policy, _ := kube.NetworkingV1().NetworkPolicies("hdb-db1").Get(context.Background(), name, metav1.GetOptions{})
	if policy.Spec.Egress[1].Ports[0].Port.IntValue() != 6379 || policy.Spec.Egress[2].To[0].IPBlock.CIDR != "2600:1f18::10/128" || policy.Spec.Egress[2].Ports[0].Port.IntValue() != 6380 {
		t.Fatal("redis policy", policy.Spec.Egress)
	}
}

func TestCopyPostgresImageVersion(t *testing.T) {
	for _, tc := range []struct {
		source, target, image, err string
	}{
		{"16", "17", databaseImages["postgresql:17"], ""},
		{"17.4", "17", databaseImages["postgresql:17"], ""},
		{"", "18", databaseImages["postgresql:18"], ""},
		{"14", "18", databaseImages["postgresql:18"], ""},
		{"18", "17", "", "newer than the target"},
		{"16", "15", "", "not available"},
		{"sixteen", "17", "", "invalid"},
	} {
		image, err := copyPostgresImage(tc.source, tc.target)
		if image != tc.image || tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
			t.Fatalf("%s->%s: %q %v", tc.source, tc.target, image, err)
		}
	}
}

func TestStartDatabaseCopyRefusesPrivateSources(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		addrs     []string
	}{
		{"private", "postgresql://u:p@db.internal.example/x", []string{"10.0.0.5"}},
		{"one private among public", "postgresql://u:p@db.example/x", []string{"34.1.2.3", "172.16.0.9"}},
		{"cluster service", "postgresql://u:p@database-rw.hdb-other.svc/x", []string{"100.64.0.10"}},
		{"loopback literal", "postgresql://u:p@127.0.0.1:5432/x", nil},
		{"metadata literal", "redis://169.254.169.254:6379", nil},
		{"wrong scheme", "mysql://u:p@db.example/x", []string{"34.1.2.3"}},
		{"multi host", "postgresql://u:p@a.example,b.example/x", []string{"34.1.2.3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copyLookup(t, tc.addrs...)
			kube := fake.NewClientset()
			c := &Client{kube: kube}
			engine, version := "postgresql", "17"
			if strings.HasPrefix(tc.url, "redis") {
				engine, version = "redis", "8"
			}
			_, err := c.StartDatabaseCopy(context.Background(), copyRequest(engine, version, tc.url))
			if err == nil {
				t.Fatal("accepted", tc.url)
			}
			t.Log(err)
			jobs, _ := kube.BatchV1().Jobs("hdb-db1").List(context.Background(), metav1.ListOptions{})
			if len(jobs.Items) != 0 {
				t.Fatal("created a job for a refused source")
			}
		})
	}
}

func TestStartDatabaseCopyRequiresReadyTarget(t *testing.T) {
	copyLookup(t, "34.1.2.3")
	req := copyRequest("postgresql", "17", "postgresql://u:p@db.example/x")
	req.Observation.Status = "provisioning"
	if _, err := (&Client{kube: fake.NewClientset()}).StartDatabaseCopy(context.Background(), req); err == nil {
		t.Fatal("copied into a database that is not ready")
	}
}

func copyJobFixture(name string, conditions ...batchv1.JobCondition) *batchv1.Job {
	labels := map[string]string{databaseOwner: "db1", databaseCopyLabel: name}
	start := metav1.NewTime(time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC))
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "hdb-db1", Labels: labels, UID: "uid-" + "x"}, Status: batchv1.JobStatus{StartTime: &start, Conditions: conditions}}
}

func copyPodFixture(job, message string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: job + "-pod", Namespace: "hdb-db1", Labels: map[string]string{databaseCopyLabel: job}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "copy", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: message}}}}}}
}

func TestDatabaseCopyStatusMapping(t *testing.T) {
	done := metav1.NewTime(time.Date(2026, 11, 13, 11, 0, 0, 0, time.UTC))
	ready := int32(1)
	running := copyJobFixture("copy-run")
	running.Status.Ready = &ready
	succeeded := copyJobFixture("copy-ok", batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: done})
	succeeded.Status.CompletionTime = &done
	failed := copyJobFixture("copy-bad", batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, LastTransitionTime: done, Reason: "BackoffLimitExceeded"})
	deadline := copyJobFixture("copy-slow", batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, LastTransitionTime: done, Reason: "DeadlineExceeded"})
	pending := copyJobFixture("copy-new")
	pending.Status.StartTime = nil
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "copy-bad", Namespace: "hdb-db1"}, Data: map[string][]byte{"SOURCE_URL": []byte("postgresql://owner:hunter2pw@ep.neon.tech/db")}}
	kube := fake.NewClientset(running, succeeded, failed, deadline, pending, secret,
		copyPodFixture("copy-ok", "keys=207\n"),
		copyPodFixture("copy-bad", "pg_restore: error: could not connect: password hunter2pw rejected\npg_restore: error: extension \"neon\" is not available"))
	c := &Client{kube: kube}
	for id, want := range map[CopyID]string{"db1/copy-run": "running", "db1/copy-ok": "succeeded", "db1/copy-bad": "failed", "db1/copy-slow": "failed", "db1/copy-new": "pending"} {
		status, err := c.DatabaseCopyStatus(context.Background(), id)
		if err != nil || status.Phase != want {
			t.Fatal(id, status, err)
		}
		if want == "succeeded" && (status.Keys != 207 || !status.FinishedAt.Equal(done.Time) || status.StartedAt == nil) {
			t.Fatal("success details", status)
		}
		if want == "failed" && (status.FinishedAt == nil || status.Error == "") {
			t.Fatal("failure details", status)
		}
		if want == "pending" && status.StartedAt != nil {
			t.Fatal("pending copy has a start time")
		}
	}
	status, _ := c.DatabaseCopyStatus(context.Background(), "db1/copy-bad")
	if strings.Contains(status.Error, "hunter2pw") || !strings.Contains(status.Error, `extension "neon" is not available`) {
		t.Fatal("error summary", status.Error)
	}
	status, _ = c.DatabaseCopyStatus(context.Background(), "db1/copy-slow")
	if !strings.Contains(status.Error, "6 hours") {
		t.Fatal("deadline error", status.Error)
	}
	if _, err := c.DatabaseCopyStatus(context.Background(), "db1/copy-gone"); !apierrors.IsNotFound(err) {
		t.Fatal("missing copy", err)
	}
	for _, bad := range []CopyID{"db1", "db1/not-a-copy", "../x/copy-a", "DB1/copy-a"} {
		if _, err := c.DatabaseCopyStatus(context.Background(), bad); err == nil {
			t.Fatal("accepted id", bad)
		}
	}
}

func TestDatabaseCopyStatusRefusesForeignJob(t *testing.T) {
	job := copyJobFixture("copy-x")
	job.Labels[databaseOwner] = "other"
	c := &Client{kube: fake.NewClientset(job)}
	if _, err := c.DatabaseCopyStatus(context.Background(), "db1/copy-x"); err == nil {
		t.Fatal("read a job owned by another database")
	}
}

func TestCancelAndCleanupDatabaseCopies(t *testing.T) {
	done := metav1.Now()
	active := copyJobFixture("copy-active")
	finished := copyJobFixture("copy-done", batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: done})
	failed := copyJobFixture("copy-failed", batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, LastTransitionTime: done})
	unrelated := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "recovery-1", Namespace: "hdb-db1", Labels: map[string]string{databaseOwner: "db1"}}}
	kube := fake.NewClientset(active, finished, failed, unrelated)
	c := &Client{kube: kube}
	jobs := kube.BatchV1().Jobs("hdb-db1")

	if err := c.CleanupDatabaseCopies(context.Background(), "db1"); err != nil {
		t.Fatal(err)
	}
	list, _ := jobs.List(context.Background(), metav1.ListOptions{})
	names := []string{}
	for _, j := range list.Items {
		names = append(names, j.Name)
	}
	if fmt.Sprint(names) != "[copy-active recovery-1]" {
		t.Fatal("cleanup removed the wrong jobs", names)
	}
	if err := c.CancelDatabaseCopy(context.Background(), "db1/copy-active"); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Get(context.Background(), "copy-active", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("cancel left the job", err)
	}
	if err := c.CancelDatabaseCopy(context.Background(), "db1/copy-active"); err != nil {
		t.Fatal("cancel is not idempotent", err)
	}
	if err := c.CancelDatabaseCopy(context.Background(), "db1/recovery-1"); err == nil {
		t.Fatal("cancel accepted a non-copy job")
	}
}

func TestScrubCopyError(t *testing.T) {
	in := "line1\nline2\nconnecting to rediss://default:AbCd1234@eu1.upstash.io:6379\npg_dump: error: postgresql://owner:pw@ep.neon.tech/db failed\nPGPASSWORD=topsecret password: other\nextra secretvalue here"
	out := scrubCopyError(in, "secretvalue")
	for _, leak := range []string{"AbCd1234", "owner:pw", "topsecret", "other", "secretvalue", "line1"} {
		if strings.Contains(out, leak) {
			t.Fatalf("leaked %q in %q", leak, out)
		}
	}
	if !strings.Contains(out, "rediss://***@eu1.upstash.io") || !strings.Contains(out, "ep.neon.tech/db failed") {
		t.Fatal("over-scrubbed", out)
	}
	if long := scrubCopyError(strings.Repeat("x", 5000)); len(long) != 1000 {
		t.Fatal("not truncated", len(long))
	}
}
