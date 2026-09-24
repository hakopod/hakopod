package cluster

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// Runs only explicitly selected catalog workloads in the named disposable cluster.
// The Baserow S3 service is a real disposable MinIO fixture, not a provider mock.
func TestLiveCompleteCatalog(t *testing.T) {
	if os.Getenv("HAKOPOD_COMPLETE_CATALOG_TEST") != "1" {
		t.Skip("opt-in full catalog acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires k3d-hakopod-dev")
	}
	id := os.Getenv("HAKOPOD_CATALOG_TEMPLATE")
	if !strings.Contains("|dagu|couchdb|blinko|bytebase|calcom|baserow|", "|"+id+"|") || id == "" {
		t.Fatal("select one complete catalog template")
	}
	if id == "calcom" && runtime.GOARCH != "amd64" {
		t.Skip("upstream Cal.com image is AMD64 only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	c, err := New(path, Options{AppDomain: "127.0.0.1.sslip.io", RolloutTimeout: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	// Large upstream artifacts must not trigger disk-pressure evictions in fixtures.
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes.Items {
		raw, err := c.kube.CoreV1().RESTClient().Get().AbsPath("/api/v1/nodes/" + node.Name + "/proxy/stats/summary").DoRaw(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var summary struct {
			Node struct {
				Fs struct {
					AvailableBytes uint64 `json:"availableBytes"`
				} `json:"fs"`
			} `json:"node"`
		}
		if json.Unmarshal(raw, &summary) != nil || summary.Node.Fs.AvailableBytes < 12<<30 {
			t.Fatal("complete catalog acceptance requires 12 GiB free node disk")
		}
	}
	options := spec.TemplateOptions{Name: "catalog-" + id, StorageGiB: 1, SiteURL: func() string {
		if id == "blinko" || id == "baserow" || id == "calcom" {
			return "https://catalog.example.test"
		}
		return ""
	}(), Architecture: runtime.GOARCH}
	if id == "baserow" {
		options.Values = map[string]string{"storage-bucket": "catalog-media", "storage-region": "us-east-1", "storage-endpoint": "https://fixture.example.test"}
	}
	app, err := spec.PlanTemplate(id, options)
	if err != nil {
		t.Fatal(err)
	}
	if id == "baserow" {
		app.Services["s3"] = spec.Service{Image: "pgsty/minio:RELEASE.2026-06-18T00-00-00Z@sha256:dacff8306a6e0a734518533992dbdcca26bc1ca47f77cf47cb9945725f92b29b", RunAsUser: 1000, RunAsGroup: 1000, FSGroup: 1000, Size: "medium", Port: 9000, Command: []string{"minio"}, Args: []string{"server", "/tmp/minio", "--console-address", ":9001"}, Env: map[string]string{"MINIO_ROOT_USER": "fixture-access-key"}, Secrets: map[string]spec.SecretRef{"MINIO_ROOT_PASSWORD": {Ref: "storage-secret-key"}}, Healthcheck: "/minio/health/ready", Networks: []string{"default"}}
		for _, name := range []string{"backend", "worker", "beat"} {
			s := app.Services[name]
			s.Env["AWS_S3_ENDPOINT_URL"] = "http://s3:9000"
			s.Env["AWS_S3_USE_SSL"] = "false"
			if name == "backend" {
				s.DependsOn = append(s.DependsOn, "s3")
			}
			app.Services[name] = s
		}
		app, err = spec.Normalize(app)
		if err != nil {
			t.Fatal(err)
		}
	}
	app, err = c.Resolve(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Project: "catalog-test", Environment: "test", ApplicationID: fmt.Sprintf("complete-catalog-%s-%d", id, time.Now().UnixNano()), OperationID: "catalog-initial", Revision: 1, Spec: app}
	labels := labelsFor(target, "")
	labels["hakopod.io/acceptance"] = "complete-catalog"
	ns, err := c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labels}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	password := strings.Repeat("fixture-only-", 4)
	defer func() {
		clean, done := context.WithTimeout(context.Background(), 2*time.Minute)
		defer done()
		current, err := c.kube.CoreV1().Namespaces().Get(clean, ns.Name, metav1.GetOptions{})
		if err != nil || current.UID != ns.UID || owned(current, target) != nil || current.Labels["hakopod.io/acceptance"] != "complete-catalog" {
			t.Error("fixture identity changed; retaining namespace")
			return
		}
		if t.Failed() {
			// Capture bounded, fixture-only diagnostics before reclaiming the namespace.
			pods, err := c.kube.CoreV1().Pods(ns.Name).List(clean, metav1.ListOptions{})
			if err == nil {
				for _, pod := range pods.Items {
					t.Logf("fixture pod %s: phase=%s conditions=%v containers=%v", pod.Name, pod.Status.Phase, pod.Status.Conditions, pod.Status.ContainerStatuses)
					for _, container := range pod.Spec.Containers {
						data, err := c.kube.CoreV1().Pods(ns.Name).GetLogs(pod.Name, &corev1.PodLogOptions{Container: container.Name, TailLines: ptr(int64(60)), LimitBytes: ptr(int64(6000))}).DoRaw(clean)
						if err == nil {
							t.Logf("fixture logs %s/%s: %s", pod.Name, container.Name, strings.ReplaceAll(string(data), password, "[fixture credential]"))
						}
					}
				}
			}
		}
		claims, err := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).List(clean, metav1.ListOptions{})
		if err != nil {
			t.Error(err)
			return
		}
		for _, ref := range spec.TemplateSecretNames(app) {
			if err := c.DeleteWorkloadSecret(clean, target.Project, target.Environment, app.Name, ref); err != nil {
				t.Error(err)
			}
		}
		if err := c.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(current)); err != nil {
			t.Error(err)
			return
		}
		for clean.Err() == nil {
			_, err := c.kube.CoreV1().Namespaces().Get(clean, ns.Name, metav1.GetOptions{})
			gone := apierrors.IsNotFound(err)
			for _, claim := range claims.Items {
				if claim.Spec.VolumeName != "" {
					_, err := c.kube.CoreV1().PersistentVolumes().Get(clean, claim.Spec.VolumeName, metav1.GetOptions{})
					gone = gone && apierrors.IsNotFound(err)
				}
			}
			if gone {
				t.Log("owned fixture namespace, claims and dynamic volumes reclaimed")
				return
			}
			time.Sleep(time.Second)
		}
		t.Error("fixture cleanup did not complete")
	}()
	for _, ref := range spec.TemplateSecretNames(app) {
		value := password
		if ref == "encryption-key" {
			value = strings.Repeat("a", 32)
		}
		if ref == "storage-access-key" {
			value = "fixture-access-key"
		}
		if err := c.PutWorkloadSecret(ctx, target.Project, target.Environment, app.Name, ref, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Message) }); err != nil {
		t.Fatal(err)
	}
	run := func(service, input string, args ...string) string {
		t.Helper()
		base := []string{"--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", ns.Name, "exec", "-i", "deployment/" + service, "--"}
		command := exec.CommandContext(ctx, "kubectl", append(base, args...)...)
		command.Stdin = strings.NewReader(input)
		out, err := command.CombinedOutput()
		if err != nil {
			safe := strings.ReplaceAll(string(out), password, "[redacted]")
			if len(safe) > 3000 {
				safe = safe[:3000]
			}
			t.Fatalf("behavior probe failed: %v: %s", err, safe)
		}
		return strings.TrimSpace(string(out))
	}
	endpoint := completeCatalogForward(t, ctx, path, ns.Name, app.Services["main"].Port)
	request := func(method, path, body string, auth bool) (int, string) {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, method, endpoint+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Host = "catalog.example.test"
		r.Header.Set("Content-Type", "application/json")
		if auth {
			r.SetBasicAuth("admin", password)
		}
		response, err := (&http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(data)
	}
	protectedPath := "/"
	if id == "dagu" {
		// Dagu serves its static SPA publicly; workflow data is API-authenticated.
		protectedPath = "/api/v1/dags"
	}
	if id == "dagu" || id == "couchdb" {
		code, _ := request("GET", protectedPath, "", false)
		if code != 401 && code != 403 {
			t.Fatalf("unauthenticated request was not rejected: %d", code)
		}
	}
	code, body := request("GET", protectedPath, "", id == "dagu" || id == "couchdb")
	if id == "dagu" {
		var payload map[string]json.RawMessage
		if code != 200 || json.Unmarshal([]byte(body), &payload) != nil {
			t.Fatal("authenticated Dagu workflow API did not return JSON", code)
		}
	}
	if code < 200 || code >= 400 || (code < 300 && len(body) == 0) {
		t.Fatalf("application page unavailable: %d", code)
	}
	if id == "couchdb" {
		code, _ = request("PUT", "/catalog_probe", "", true)
		if code != 201 && code != 202 {
			t.Fatal("database create failed", code)
		}
		code, _ = request("PUT", "/catalog_probe/retained", `{"value":"survives-restart"}`, true)
		if code != 201 && code != 202 {
			t.Fatal("document write failed", code)
		}
	}
	if id == "baserow" {
		run("backend", `import os
import django
django.setup()
import boto3
from django.core.files.base import ContentFile
from django.core.files.storage import default_storage
from django.conf import settings
assert settings.AWS_DEFAULT_ACL is None
assert settings.AWS_QUERYSTRING_AUTH is True
client = boto3.client("s3", endpoint_url=os.environ["AWS_S3_ENDPOINT_URL"], region_name="us-east-1")
client.create_bucket(Bucket="catalog-media")
key = default_storage.save("catalog-probe.txt", ContentFile(b"catalog-media-survives"))
assert default_storage.open(key).read() == b"catalog-media-survives"
assert "X-Amz-Signature=" in default_storage.url(key)
default_storage.delete(key)
print("Private S3 media round trip verified")
`, "python", "-")
	}
	if app.Services["main"].Volume != nil {
		mount := app.Services["main"].Volume.MountPath
		run("main", "", "sh", "-ec", `printf '%s' catalog-persistent-data > "$1/.hakopod-catalog-fixture"`, "fixture", mount)
		before, err := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).Get(ctx, "main-data", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		s := target.Spec.Services["main"]
		s.RestartNonce = "catalog-restart"
		target.Spec.Services["main"] = s
		target.OperationID = "catalog-restart"
		target.Revision++
		if _, err := c.Deploy(ctx, target, nil); err != nil {
			t.Fatal(err)
		}
		after, err := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).Get(ctx, "main-data", metav1.GetOptions{})
		if err != nil || after.UID != before.UID {
			t.Fatal("persistent claim changed", err)
		}
		if run("main", "", "cat", mount+"/.hakopod-catalog-fixture") != "catalog-persistent-data" {
			t.Fatal("data lost on restart")
		}
		if id == "couchdb" {
			endpoint = completeCatalogForward(t, ctx, path, ns.Name, app.Services["main"].Port)
			code, body = request("GET", "/catalog_probe/retained", "", true)
			if code != 200 || !strings.Contains(body, "survives-restart") {
				t.Fatal("database document lost on restart")
			}
		}
	}
	t.Log("Exact native preset reached readiness; authenticated access, configured persistence and applicable provider operations verified")
}

func completeCatalogForward(t *testing.T, ctx context.Context, path, namespace string, port int32) string {
	t.Helper()
	forward, stop := context.WithCancel(ctx)
	command := exec.CommandContext(forward, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", namespace, "port-forward", "deployment/main", fmt.Sprintf(":%d", port), "--address=127.0.0.1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop(); _ = command.Wait() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("port forward did not start")
	}
	line := scanner.Text()
	fields := strings.Fields(line)
	if len(fields) < 3 || !strings.HasPrefix(line, "Forwarding from 127.0.0.1:") {
		t.Fatal("unexpected port forward response")
	}
	go func() { _, _ = io.Copy(io.Discard, stdout) }()
	return "http://" + fields[2]
}
