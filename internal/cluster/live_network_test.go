package cluster

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

func TestLiveNamedNetworksAndHPA(t *testing.T) {
	if os.Getenv("HAKOPOD_CLUSTER_TEST") != "1" {
		t.Skip("requires explicit isolated development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	if path == "" {
		t.Fatal("explicit kubeconfig is required")
	}
	config, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatalf("refusing context %q", config.CurrentContext)
	}
	c, err := New(path, Options{RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	image := "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a"
	service := func(networks ...string) spec.Service {
		return spec.Service{Image: image, Port: 8080, Command: []string{"python", "-u", "-m", "http.server", "8080"}, Networks: networks}
	}
	app, err := spec.Normalize(spec.Application{Name: "network-test", Networks: map[string]spec.Network{"frontend": {}, "backend": {Internal: true}}, Services: map[string]spec.Service{"web": service("frontend", "backend"), "api": service("backend"), "edge": service("frontend")}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	app, err = c.Resolve(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "network-hpa-integration-dedicated-v1", Project: "runtime-test", Environment: "test", OperationID: "test-networks", Revision: 1, Spec: app}
	ns := Namespace(target.ApplicationID)
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		namespace, err := c.kube.CoreV1().Namespaces().Get(cleanupCtx, ns, metav1.GetOptions{})
		if err != nil {
			return
		}
		if err := owned(namespace, target); err != nil {
			t.Error(err)
			return
		}
		if err := c.kube.CoreV1().Namespaces().Delete(cleanupCtx, ns, deleteOptions(namespace)); err != nil {
			t.Error(err)
		}
	})
	if _, err := c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	pods := make(map[string]string)
	ips := make(map[string]string)
	for _, service := range spec.Names(app) {
		items, err := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: serviceKey + "=" + service, Limit: 32})
		if err != nil {
			t.Fatal(err)
		}
		for _, pod := range items.Items {
			if podReady(pod) && pod.DeletionTimestamp == nil {
				pods[service] = pod.Name
				ips[service] = pod.Status.PodIP
			}
		}
		if pods[service] == "" {
			t.Fatalf("no ready %s pod", service)
		}
	}
	probe := func(source, host string, port int, want bool) {
		t.Helper()
		code := "import socket,sys\ntry:\n socket.create_connection((sys.argv[1],int(sys.argv[2])),2).close();print('connected')\nexcept OSError:\n print('blocked')\n"
		cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "-n", ns, "exec", pods[source], "--", "python", "-c", code, host, strconv.Itoa(port))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("probe execution: %v %s", err, out)
		}
		connected := strings.TrimSpace(string(out)) == "connected"
		if connected != want {
			t.Fatalf("%s -> %s:%d expected allowed=%v, got %s", source, host, port, want, out)
		}
		t.Logf("real network policy %s -> %s:%d allowed=%v", source, host, port, want)
	}
	probe("web", "api", 8080, true)
	probe("web", ips["api"], 8080, true)
	probe("edge", "api", 8080, false)
	probe("edge", ips["api"], 8080, false)
	probe("api", "edge", 8080, false)
	probe("api", ips["edge"], 8080, false)
	probe("api", "web", 8080, true)
	probe("web", "1.1.1.1", 443, true)
	probe("api", "1.1.1.1", 443, false)
	dns := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "-n", ns, "exec", pods["api"], "--", "python", "-c", "import socket; assert socket.getaddrinfo('example.com',443); print('DNS allowed')")
	if output, err := dns.CombinedOutput(); err != nil {
		t.Fatalf("internal-only DNS exception failed: %v %s", err, output)
	}
	t.Log("internal-only service retains DNS, mixed membership retains internet egress")
	// Real API preservation check: HPA owns the existing replica value. Make
	// that value differ from the canonical initial/minimum replica count, then
	// reconcile a workload update and confirm Hakopod did not reset it.
	autoscaled := app.Services["web"]
	autoscaled.Autoscaling = &spec.Autoscaling{MinReplicas: 1, MaxReplicas: 3, TargetCPU: 70}
	if err := c.applyHPA(ctx, target, "web", autoscaled); err != nil {
		t.Fatal(err)
	}
	deploymentAPI := c.kube.AppsV1().Deployments(ns)
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		dep, err := deploymentAPI.Get(ctx, "web", metav1.GetOptions{})
		if err != nil {
			return err
		}
		dep.Spec.Replicas = ptr(int32(2))
		dep.Spec.Template.Annotations = map[string]string{"test.example/secret-reload": "preserved"}
		_, err = deploymentAPI.Update(ctx, dep, metav1.UpdateOptions{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.applyDeployment(ctx, target, "web", autoscaled); err != nil {
		t.Fatal(err)
	}
	after, err := deploymentAPI.Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if *after.Spec.Replicas != 2 || after.Spec.Template.Annotations["test.example/secret-reload"] != "preserved" {
		t.Fatalf("reconciler overwrote controller-owned fields: replicas=%d annotation=%q", *after.Spec.Replicas, after.Spec.Template.Annotations["test.example/secret-reload"])
	}
	hpa, err := c.kube.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if hpa.Spec.Behavior == nil || hpa.Spec.Behavior.ScaleDown == nil || *hpa.Spec.Behavior.ScaleDown.StabilizationWindowSeconds != 300 {
		t.Fatal("missing HPA downscale stabilization")
	}
	// Stop retains the service specification but removes HPA ownership and all active pods.
	autoscaled.Suspended = true
	target.Spec.Services["web"] = autoscaled
	target.Revision++
	if _, err := c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	stopped, err := deploymentAPI.Get(ctx, "web", metav1.GetOptions{})
	if err != nil || *stopped.Spec.Replicas != 0 || stopped.Status.Replicas != 0 {
		t.Fatal("stop did not scale to zero", err)
	}
	if _, err := c.kube.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(ctx, "web", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("stopped HPA still exists", err)
	}
	observed, err := c.Observe(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range observed.Services {
		if service.Name == "web" && service.Status != "stopped" {
			t.Fatal("stopped service reported incorrectly", service)
		}
	}
	autoscaled.Suspended = false
	target.Spec.Services["web"] = autoscaled
	target.Revision++
	if _, err := c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	resumed, err := deploymentAPI.Get(ctx, "web", metav1.GetOptions{})
	if err != nil || *resumed.Spec.Replicas < 1 {
		t.Fatal("resume did not restore replicas", err)
	}
	if _, err := c.kube.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(ctx, "web", metav1.GetOptions{}); err != nil {
		t.Fatal("resume did not restore HPA", err)
	}

	t.Log(fmt.Sprintf("real HPA field ownership verified at %d replicas, 300-second downscale stabilization", *after.Spec.Replicas))
}
