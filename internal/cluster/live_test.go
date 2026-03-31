package cluster

import (
	"context"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// This is an opt-in, real Kubernetes test, never a fixture. Its explicit
// context gate prevents accidentally mutating an operator's current cluster.
func TestLiveRuntime(t *testing.T) {
	if os.Getenv("HAKOPOD_CLUSTER_TEST") != "1" {
		t.Skip("set HAKOPOD_CLUSTER_TEST=1 and HAKOPOD_TEST_KUBECONFIG to the isolated k3d-hakopod-dev cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	if path == "" {
		t.Fatal("explicit HAKOPOD_TEST_KUBECONFIG is required")
	}
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatalf("refusing real-cluster test on context %q", configuration.CurrentContext)
	}
	c, err := New(path, Options{AppDomain: "127.0.0.1.sslip.io", IngressClass: "haproxy", PublicPort: 18080, RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../examples/shop/hakopod.toml")
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resolved, err := c.Resolve(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "runtime-integration-dedicated-v1", Project: "runtime-test", Environment: "test", OperationID: "test-initial", Revision: 1, Spec: resolved}
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		ns, err := c.kube.CoreV1().Namespaces().Get(cleanupCtx, Namespace(target.ApplicationID), metav1.GetOptions{})
		if err != nil {
			return
		}
		if err := owned(ns, target); err != nil {
			t.Error(err)
			return
		}
		if err := c.kube.CoreV1().Namespaces().Delete(cleanupCtx, ns.Name, deleteOptions(ns)); err != nil {
			t.Error(err)
		}
	})
	observe, err := c.Deploy(ctx, target, func(e Event) { t.Logf("%s %s: %s", e.Type, e.Service, e.Message) })
	if err != nil {
		t.Fatal(err)
	}
	if observe.Status != "healthy" {
		t.Fatalf("unexpected observed state: %+v", observe)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	var body []byte
	for attempt := 0; attempt < 20; attempt++ {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:18080/api", nil)
		request.Host = c.hostname(target, "web")
		response, err := client.Do(request)
		if err == nil {
			body, _ = io.ReadAll(io.LimitReader(response.Body, 64<<10))
			response.Body.Close()
			if response.StatusCode == 200 && strings.Contains(string(body), "private-api") {
				break
			}
		}
		if attempt == 19 {
			t.Fatalf("HAProxy /api proxy did not reach private API: %s, %v", body, err)
		}
		if err := sleepContext(ctx, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("real HAProxy public web -> private API response: %s", body)
	before, err := c.kube.AppsV1().Deployments(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	previous := target.Spec
	updated, err := spec.Normalize(target.Spec)
	if err != nil {
		t.Fatal(err)
	}
	api := updated.Services["api"]
	api.Env = map[string]string{"RELEASE": "2"}
	updated.Services["api"] = api
	target.Spec = updated
	target.Previous = &previous
	target.Revision = 2
	target.OperationID = "test-service-update"
	if _, err := c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	after, err := c.kube.AppsV1().Deployments(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.Spec.Template, before.Spec.Template) {
		t.Fatalf("unchanged web service pod template changed")
	}
	// An old healthy replica must not let a new unready configuration succeed.
	healthy := target.Spec
	broken, err := spec.Normalize(healthy)
	if err != nil {
		t.Fatal(err)
	}
	brokenAPI := broken.Services["api"]
	brokenAPI.Healthcheck = "/not-ready"
	broken.Services["api"] = brokenAPI
	target.Spec = broken
	target.Revision = 3
	target.OperationID = "test-failed-readiness"
	target.Previous = &healthy
	c.options.RolloutTimeout = 12 * time.Second
	failed, err := c.Deploy(ctx, target, nil)
	if err == nil || failed.Status == "healthy" {
		t.Fatalf("old healthy pods masked a failed new rollout: %+v, %v", failed, err)
	}
	t.Logf("intentional readiness failure reported accurately: %v", err)
	// Let the Deployment controller record its own stalled condition before
	// recovery, covering a real condition that can outlive a generation change.
	for attempt := 0; attempt < 35; attempt++ {
		stalled, err := c.kube.AppsV1().Deployments(Namespace(target.ApplicationID)).Get(ctx, "api", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(deploymentFailure(stalled), "deadline exceeded") {
			t.Log("Kubernetes recorded ProgressDeadlineExceeded before recovery")
			break
		}
		if attempt == 34 {
			t.Fatal("Kubernetes did not record the expected stalled rollout")
		}
		if err := sleepContext(ctx, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	target.Spec = healthy
	target.Previous = &broken
	target.Revision = 4
	target.OperationID = "test-recovery"
	c.options.RolloutTimeout = 90 * time.Second
	if _, err := c.Deploy(ctx, target, nil); err != nil {
		t.Fatalf("previous healthy configuration did not recover: %v", err)
	}
	stream, err := c.Logs(ctx, Namespace(target.ApplicationID), "api", 20, false)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := io.ReadAll(io.LimitReader(stream, 64<<10))
	stream.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 {
		t.Fatal("real application logs missing")
	}
	nodes, err := c.Nodes(ctx)
	if err != nil || len(nodes) == 0 {
		t.Fatalf("real node state missing: %v", err)
	}
	t.Logf("verified %d actual node(s), private application logs, stable DNS/ingress, and unchanged service pod template", len(nodes))
}
