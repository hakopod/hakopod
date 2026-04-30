package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/hakopod/hakopod/internal/worker"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// This starts one real worker against an isolated temporary PostgreSQL database
// and a random application namespace. It never touches the operator's API or
// the lifecycle/restart acceptance application's records or resources.
func TestLivePartialGroupAndRegistryFailures(t *testing.T) {
	if os.Getenv("HAKOPOD_FAILURE_TEST") != "1" {
		t.Skip("set HAKOPOD_FAILURE_TEST=1 for isolated real-cluster failure acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	if path == "" {
		path = filepath.Join("..", "..", ".local", "kubeconfig")
	}
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatalf("refusing failure checks on context %q", configuration.CurrentContext)
	}
	config, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	config.Timeout = 10 * time.Second
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	clusterClient, err := cluster.New(path, cluster.Options{RolloutTimeout: 12 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	raw, err := db.Bootstrap(ctx, "failure-acceptance")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := db.CreateKey(ctx, principal, store.KeyInput{Name: "failure-check-deployer", Project: "demo", Environment: "development", Application: "failure-check", Permissions: []string{"deployments:write", "deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&api.Server{Store: db, Cluster: clusterClient}).Handler())
	defer server.Close()
	server.Client().Timeout = 5 * time.Second
	runner := &worker.Worker{Store: db, Cluster: clusterClient, Concurrency: 1, Timeout: 70 * time.Second}
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); runner.Run(workerCtx) }()
	defer func() { stopWorker(); <-workerDone }()
	call := func(method, path string, body any, want int, result any) {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, method, server.URL+"/api/v1"+path, strings.NewReader(string(store.JSON(body))))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		if method == http.MethodPost {
			request.Header.Set("Idempotency-Key", store.NewID())
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("%s %s returned HTTP%d", method, path, response.StatusCode)
		}
		if result != nil {
			if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(result); err != nil {
				t.Fatal(err)
			}
		}
	}
	deploy := func(app spec.Application, revision int64) store.Deployment {
		t.Helper()
		var accepted store.Deployment
		call("POST", "/deployments", map[string]any{"project": "demo", "environment": "development", "spec": app, "expected_revision": revision}, 202, &accepted)
		return accepted
	}
	wait := func(id, status string) store.Deployment {
		t.Helper()
		for attempt := 0; attempt < 70; attempt++ {
			var current store.Deployment
			call("GET", "/deployments/"+id, nil, 200, &current)
			if current.Status != "queued" && current.Status != "running" {
				if current.Status != status {
					t.Fatalf("operation expected %s, got %s: %s", status, current.Status, current.Error)
				}
				return current
			}
			select {
			case <-ctx.Done():
				t.Fatal("failure acceptance exceeded bounded deadline")
			case <-time.After(time.Second):
			}
		}
		t.Fatal("operation did not reach expected terminal state")
		return store.Deployment{}
	}
	image := "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a"
	code := "from http.server import BaseHTTPRequestHandler,HTTPServer\nclass Handler(BaseHTTPRequestHandler):\n def do_GET(self):\n  self.send_response(200 if self.path == '/readyz' else 404); self.end_headers(); self.wfile.write(b'healthy')\nHTTPServer(('0.0.0.0',8080),Handler).serve_forever()\n"
	service := spec.Service{Image: image, Port: 8080, Healthcheck: "/readyz", Size: "small", Command: []string{"python", "-u", "-c"}, Args: []string{code}, Env: map[string]string{"RELEASE": "original"}}
	baseline, err := spec.Normalize(spec.Application{Name: "failure-check", Services: map[string]spec.Service{"alpha": service, "omega": service}})
	if err != nil {
		t.Fatal(err)
	}
	initial := deploy(baseline, 0)
	namespace := cluster.Namespace(initial.ApplicationID)
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		ns, err := kube.CoreV1().Namespaces().Get(cleanupCtx, namespace, metav1.GetOptions{})
		if err != nil {
			return
		}
		if ns.Labels["app.kubernetes.io/managed-by"] != "hakopod" || ns.Labels["hakopod.io/application-id"] != strings.TrimPrefix(namespace, "hp-") {
			t.Error("refusing cleanup of namespace with mismatched ownership")
			return
		}
		uid := ns.UID
		if err := kube.CoreV1().Namespaces().Delete(cleanupCtx, namespace, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
			t.Error(err)
		}
	})
	healthy := wait(initial.ID, "succeeded")
	changed, err := spec.Normalize(baseline)
	if err != nil {
		t.Fatal(err)
	}
	alpha := changed.Services["alpha"]
	alpha.Env["RELEASE"] = "partial-update"
	changed.Services["alpha"] = alpha
	omega := changed.Services["omega"]
	omega.Healthcheck = "/not-ready"
	changed.Services["omega"] = omega
	partial := wait(deploy(changed, healthy.Revision).ID, "failed")
	alphaReady, omegaFailed, recovered := -1, -1, false
	for i, event := range partial.Events {
		if alphaReady < 0 && event.Type == "ready" && event.Service == "alpha" {
			alphaReady = i
		}
		if omegaFailed < 0 && event.Type == "failed" && event.Service == "omega" {
			omegaFailed = i
		}
		if event.Type == "recovered" {
			recovered = true
		}
	}
	if alphaReady < 0 || omegaFailed <= alphaReady || !recovered {
		t.Fatalf("missing ordered partial-success/failure/recovery evidence: alphaReady=%d omegaFailed=%d recovered=%v", alphaReady, omegaFailed, recovered)
	}
	if !strings.Contains(partial.Error, "readiness") {
		t.Fatalf("failure did not explain readiness cause: %s", partial.Error)
	}
	snapshots := make(map[string]*appsv1.Deployment)
	for _, name := range []string{"alpha", "omega"} {
		deployment, err := kube.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		container := deployment.Spec.Template.Spec.Containers[0]
		if name == "alpha" {
			found := false
			for _, env := range container.Env {
				if env.Name == "RELEASE" {
					found = env.Value == "original"
				}
			}
			if !found {
				t.Fatal("previous alpha configuration was not restored")
			}
		}
		if container.ReadinessProbe.HTTPGet.Path != "/readyz" {
			t.Fatal("previous omega readiness was not restored")
		}
		snapshots[name] = deployment
	}
	t.Log("real partial group verified: alpha updated and ready, omega failed readiness, worker restored both previous service configurations")
	invalid, err := spec.Normalize(baseline)
	if err != nil {
		t.Fatal(err)
	}
	missing := invalid.Services["alpha"]
	missing.Image = "docker.io/library/python:hakopod-no-such-tag-" + store.NewID()
	invalid.Services["alpha"] = missing
	rejected := wait(deploy(invalid, partial.Revision).ID, "failed")
	if rejected.ResolvedSpec != nil || !strings.Contains(rejected.Error, "not found") {
		t.Fatalf("invalid image omitted an actionable resolver cause: %s", rejected.Error)
	}
	for _, event := range rejected.Events {
		if event.Type == "policy" || event.Type == "applying" {
			t.Fatal("registry failure reached infrastructure reconciliation")
		}
	}
	for name, before := range snapshots {
		after, err := kube.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before.Spec.Template, after.Spec.Template) || before.Annotations["hakopod.io/operation"] != after.Annotations["hakopod.io/operation"] {
			t.Fatal("resolver failure changed a live workload")
		}
	}
	t.Log(fmt.Sprintf("invalid image rejected before Kubernetes mutation: %s", rejected.Error))
}
