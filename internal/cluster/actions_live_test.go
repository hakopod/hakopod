package cluster

import (
	"context"
	"encoding/json"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"testing"
	"time"
)

// The only credentials accepted by this fixture are single-job JIT configs.
// It never receives the repository administration token used to issue them.
func TestManagedActionsGitHubJobsLive(t *testing.T) {
	raw := os.Getenv("HAKOPOD_ACTIONS_TEST_JIT")
	if raw == "" {
		t.Skip("requires explicitly issued single-job GitHub fixture registrations")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Fatal("live runner fixture is restricted to disposable CI")
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	config, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("only the named development cluster is permitted")
	}
	var configs []string
	if json.Unmarshal([]byte(raw), &configs) != nil || len(configs) != 2 {
		t.Fatal("expected two single-job registrations")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	c, err := New(kubeconfig, Options{})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: ActionsRuntime, Labels: map[string]string{managedBy: "hakopod"}}, Handler: ActionsRuntime, Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{"hakopod.io/actions-runtime": "ready"}}, Overhead: &nodev1.Overhead{PodFixed: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("512Mi")}}}
	if _, err = c.kube.NodeV1().RuntimeClasses().Create(ctx, runtime, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	node, err := c.kube.CoreV1().Nodes().Get(ctx, "k3d-hakopod-dev-server-0", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Labels["hakopod.io/actions-runtime"] = "ready"
	if _, err = c.kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	target := runnerTarget(t)
	svc := target.Spec.Services["runner"]
	svc.Resources = &spec.Resources{CPURequest: "500m", CPULimit: "1500m", MemoryRequest: "1Gi", MemoryLimit: "4Gi"}
	svc.Replicas = 2
	svc.Actions.TimeoutMinutes = 20
	target.Spec.Services["runner"] = svc
	if err = c.bootstrap(ctx, target); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if !t.Failed() {
			c.kube.CoreV1().Namespaces().Delete(context.Background(), Namespace(target.ApplicationID), metav1.DeleteOptions{})
		}
	}()
	ids := []string{"11111111111111111111111111111111", "22222222222222222222222222222222"}
	for i, id := range ids {
		if err = c.SaveActionsConfig(ctx, target, "runner", id, configs[i]); err != nil {
			t.Fatal("save single-job config failed")
		}
		if err = c.StartActionsPod(ctx, target, "runner", id, svc); err != nil {
			t.Fatal(err)
		}
	}
	for {
		done := 0
		for _, id := range ids {
			p, e := c.kube.CoreV1().Pods(Namespace(target.ApplicationID)).Get(ctx, "actions-"+id, metav1.GetOptions{})
			if e != nil {
				t.Fatal(e)
			}
			if p.Status.Phase == corev1.PodFailed {
				t.Fatalf("runner pod failed: %s %s", p.Status.Reason, p.Status.Message)
			}
			if p.Status.Phase == corev1.PodSucceeded {
				done++
			}
		}
		if done == len(ids) {
			break
		}
		if err = sleepContext(ctx, 5*time.Second); err != nil {
			t.Fatal("runner jobs did not finish before the fixture deadline")
		}
	}
	t.Log("Both real GitHub job runners completed using the product pod builder and lifecycle-bound Docker sidecar")
}
