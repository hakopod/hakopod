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

func TestManagedActionsWorkspaceLive(t *testing.T) {
	if os.Getenv("HAKOPOD_ACTIONS_WORKSPACE_TEST") != "1" {
		t.Skip("requires the named development cluster with the Actions sandbox")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("only the named development cluster is permitted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c, err := New(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.ActionsAvailable(ctx); err != nil {
		t.Fatal(err)
	}
	target := runnerTarget(t)
	target.ApplicationID = "actions-workspace-development"
	s := target.Spec.Services["runner"]
	s.Actions.WorkspaceSizeGiB = 8
	s.Actions.TimeoutMinutes = 10
	s.Resources = &spec.Resources{CPURequest: "500m", CPULimit: "1500m", MemoryRequest: "1Gi", MemoryLimit: "4Gi"}
	target.Spec.Services["runner"] = s
	if err = c.bootstrap(ctx, target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if err := c.kube.CoreV1().Namespaces().Delete(cleanup, Namespace(target.ApplicationID), metav1.DeleteOptions{}); err != nil {
			t.Error(err)
		}
	})
	if err = c.SaveActionsConfig(ctx, target, "runner", "workspace-fixture", "unused-development-fixture"); err != nil {
		t.Fatal(err)
	}
	pod := actionsPod(target, "runner", "workspace-fixture", s)
	// Exercise the product's pod volumes and reservations without registering a
	// GitHub runner. This fixture contains no provider or registration credential.
	pod.Spec.Containers[0].Command = []string{"sh", "-c", `set -eu
test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token
mkdir -p /home/runner/_work
dd if=/dev/zero of=/home/runner/_work/storage-probe bs=1048576 count=2304
test "$(stat -c %s /home/runner/_work/storage-probe)" = 2415919104
docker run --rm -v /home/runner/_work:/work docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0 sh -c 'test "$(stat -c %s /work/storage-probe)" = 2415919104'
test "$(docker info --format '{{.DockerRootDir}}')" = /home/runner/.docker-data
rm /home/runner/_work/storage-probe
# This multi-layer image exhausted the former 2 GiB memory-backed data volume.
docker run -d --name storage-postgres -e POSTGRES_PASSWORD=development-fixture postgres:17.11-bookworm@sha256:051f7b7b3abdd564d5d1bd1e8c4b9c1b6e77087d1dd22020ede611c096a272e0
trap 'docker rm -f storage-postgres' EXIT
ready=false
for attempt in $(seq 1 60); do
  if docker exec storage-postgres pg_isready -U postgres; then ready=true; break; fi
  sleep 2
done
$ready
test "$(docker exec storage-postgres psql -U postgres -Atc 'select 42')" = 42
printf 'FROM docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0\nRUN echo nested-build > /proof\n' | docker build -t storage-fixture -
test "$(docker run --rm storage-fixture cat /proof)" = nested-build
echo 'PASS: disk-backed workspace, PostgreSQL service and nested Docker build'
`}
	if _, err = c.kube.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for {
		current, err := c.kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if current.Status.Phase == corev1.PodSucceeded {
			return
		}
		if current.Status.Phase == corev1.PodFailed {
			t.Fatalf("workspace fixture failed: %s %s", current.Status.Reason, current.Status.Message)
		}
		if err = sleepContext(ctx, 2*time.Second); err != nil {
			t.Fatal("workspace fixture did not finish", err)
		}
	}
}

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
