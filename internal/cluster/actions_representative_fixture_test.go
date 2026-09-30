package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	clienttesting "k8s.io/client-go/testing"
)

// TestActionsRepresentativeBuildFixture generates a credential-free product pod
// for a private repository's opt-in development acceptance. The source archive
// travels separately, never in Kubernetes objects or public build artifacts.
// HAKOPOD_ACTIONS_REPRESENTATIVE_STORAGE=shared-overlay2-16 selects the explicit
// shared-workspace comparison; an absent value retains the original VFS pod.
func TestActionsRepresentativeBuildFixture(t *testing.T) {
	directory := os.Getenv("HAKOPOD_ACTIONS_REPRESENTATIVE_DIR")
	if directory == "" {
		t.Skip("private representative acceptance is not selected")
	}
	path := os.Getenv("HAKOPOD_ACTIONS_REPRESENTATIVE_SCRIPT")
	source, err := os.ReadFile(path)
	if err != nil || len(source) == 0 || len(source) > 128*1024 {
		t.Fatalf("representative helper must be readable and at most 128 KiB: %v", err)
	}
	wrapper := fmt.Sprintf(`import hashlib, os, pathlib, runpy, stat, sys, time
root = pathlib.Path('/home/runner/_work/representative')
assert root.parent.parent.resolve(strict=True) == root.parent.parent
root.parent.mkdir(mode=0o700, exist_ok=True)
assert root.parent.resolve(strict=True) == root.parent
root.mkdir(mode=0o700, exist_ok=False)
end = time.monotonic() + 180
while not (root / 'ready').exists():
    assert time.monotonic() < end, 'private source staging timed out'
    time.sleep(0.25)
assert root.resolve(strict=True) == root
fd = os.open(root / 'runner-build-acceptance.py', os.O_RDONLY | os.O_NOFOLLOW)
with os.fdopen(fd, 'rb') as src:
    assert stat.S_ISREG(os.fstat(src.fileno()).st_mode)
    value = src.read(128 * 1024 + 1)
assert len(value) <= 128 * 1024 and hashlib.sha256(value).hexdigest() == %q
sys.argv = [str(root / 'runner-build-acceptance.py'), 'inner']
runpy.run_path(sys.argv[0], run_name='__main__')
`, fmt.Sprintf("%x", sha256.Sum256(source)))
	kube := fake.NewClientset()
	c := &Client{kube: kube}
	target := runnerTarget(t)
	target.ApplicationID = "actions-representative-build"
	target.Project = "actions-development-fixture"
	s := target.Spec.Services["runner"]
	s.Architecture = "arm64"
	s.NodeName = "k3d-hakopod-dev-server-0"
	s.Resources = &spec.Resources{CPURequest: "2", CPULimit: "2", MemoryRequest: "6Gi", MemoryLimit: "6Gi"}
	s.Actions.TimeoutMinutes = 55
	s.Actions.WorkspaceSizeGiB = 16
	target.Spec.Services["runner"] = s
	if err := c.bootstrap(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	pod := actionsPod(target, "runner", "representative", s)
	pod.APIVersion, pod.Kind = "v1", "Pod"
	pod.Spec.Volumes = pod.Spec.Volumes[:1]
	pod.Spec.Containers[0].VolumeMounts = pod.Spec.Containers[0].VolumeMounts[:1]
	pod.Spec.Containers[0].Command = []string{"python3", "-u", "-c", wrapper}
	pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{Name: "HAKOPOD_EXPORT_BENCHMARK_DEV_CONTEXT", Value: "k3d-hakopod-dev"})
	pod.Labels["hakopod.io/development-fixture"] = "actions-representative-build"
	if err := actionsRepresentativeSharedWorkspaceFixture(pod, os.Getenv("HAKOPOD_ACTIONS_REPRESENTATIVE_STORAGE")); err != nil {
		t.Fatal(err)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken || pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != ActionsRuntime || pod.Spec.HostNetwork || pod.Spec.HostPID || pod.Spec.HostIPC {
		t.Fatal("representative build lost the product sandbox boundary")
	}
	if len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].EmptyDir == nil || pod.Spec.Volumes[0].EmptyDir.SizeLimit.Cmp(resource.MustParse("16Gi")) != 0 {
		t.Fatal("representative build must retain the supported 16 GiB workspace")
	}
	for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		if len(container.EnvFrom) != 0 || container.SecurityContext == nil || (container.SecurityContext.Privileged != nil && *container.SecurityContext.Privileged) {
			t.Fatal("representative build received unexpected credentials or privilege")
		}
		for _, value := range container.Env {
			if value.ValueFrom != nil || (value.Name != "DOCKER_HOST" && value.Name != "ACTIONS_RUNNER_PRINT_LOG_TO_STDOUT" && value.Name != "HAKOPOD_EXPORT_BENCHMARK_DEV_CONTEXT") {
				t.Fatal("representative build received an unexpected environment source")
			}
		}
	}
	items := []runtime.Object{}
	for _, action := range kube.Actions() {
		if created, ok := action.(clienttesting.CreateAction); ok {
			object := created.GetObject()
			if _, secret := object.(*corev1.Secret); secret {
				t.Fatal("representative build must not inject any secret")
			}
			kinds, _, err := scheme.Scheme.ObjectKinds(object)
			if err != nil || len(kinds) != 1 {
				t.Fatalf("fixture kind unavailable: %T %v", object, err)
			}
			object.GetObjectKind().SetGroupVersionKind(kinds[0])
			items = append(items, object)
		}
	}
	items = append(items, pod)
	for _, object := range items {
		metadata, err := meta.Accessor(object)
		if err != nil {
			t.Fatal(err)
		}
		labels := metadata.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels["hakopod.io/development-fixture"] = "actions-representative-build"
		metadata.SetLabels(labels)
	}
	data, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": items})
	if err != nil || len(data) > 64*1024 {
		t.Fatalf("representative fixture exceeds 64 KiB: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "representative.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestActionsRepresentativeStorageProfilesPreserveProductFixture(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "private-helper.py")
	if err := os.WriteFile(script, []byte("print('development fixture')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAKOPOD_ACTIONS_REPRESENTATIVE_DIR", directory)
	t.Setenv("HAKOPOD_ACTIONS_REPRESENTATIVE_SCRIPT", script)
	var baseline *corev1.Pod
	var baselineObjects []json.RawMessage
	for _, selection := range []string{"", "baseline-vfs", "shared-overlay2-16"} {
		t.Setenv("HAKOPOD_ACTIONS_REPRESENTATIVE_STORAGE", selection)
		TestActionsRepresentativeBuildFixture(t)
		data, err := os.ReadFile(filepath.Join(directory, "representative.json"))
		if err != nil {
			t.Fatal(err)
		}
		var fixture struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(data, &fixture); err != nil {
			t.Fatal(err)
		}
		var pod *corev1.Pod
		var other []json.RawMessage
		for _, raw := range fixture.Items {
			var item corev1.Pod
			if err := json.Unmarshal(raw, &item); err != nil {
				t.Fatal(err)
			}
			if item.Kind != "Pod" {
				other = append(other, raw)
				continue
			}
			if pod != nil {
				t.Fatal("representative acceptance generated more than one Pod")
			}
			pod = &item
		}
		if pod == nil || pod.Name != "actions-representative" || pod.Labels["hakopod.io/development-fixture"] != "actions-representative-build" || pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 3300 {
			t.Fatal("representative fixture lost its exact identity or deadline")
		}
		if selection == "" {
			baseline, baselineObjects = pod, other
			if len(pod.Annotations) != 0 || pod.Spec.InitContainers[1].Command[2] != actionsDaemon {
				t.Fatal("default representative fixture no longer uses unchanged VFS")
			}
			continue
		}
		if !reflect.DeepEqual(other, baselineObjects) {
			t.Fatal("representative storage selection changed the namespace, quota, or network policy")
		}
		if selection == "shared-overlay2-16" {
			expected := map[string]string{"dev.gvisor.spec.mount.runner.type": "bind", "dev.gvisor.spec.mount.runner.share": "pod", "dev.gvisor.spec.mount.runner.options": "rw,rprivate,mode=0770,uid=1001,gid=1001,size=17g"}
			if !reflect.DeepEqual(pod.Annotations, expected) || pod.Spec.InitContainers[1].Command[2] != strings.Replace(actionsDaemon, "--storage-driver=vfs", "--storage-driver=overlay2", 1) || !strings.HasSuffix(pod.Spec.Containers[0].Command[3], baseline.Spec.Containers[0].Command[3]) || !strings.Contains(pod.Spec.InitContainers[0].Command[2], ".hakopod-shared-prepare") {
				t.Fatal("representative storage did not retain its exact disk hints, driver, and staging checkpoint")
			}
			pod.Annotations = baseline.Annotations
			pod.Spec.InitContainers[0].Command = baseline.Spec.InitContainers[0].Command
			pod.Spec.InitContainers[1].Command = baseline.Spec.InitContainers[1].Command
			pod.Spec.Containers[0].Command = baseline.Spec.Containers[0].Command
		}
		if !reflect.DeepEqual(pod, baseline) {
			t.Fatal("representative storage changed product resources, images, credentials, placement, or security")
		}
	}
}
