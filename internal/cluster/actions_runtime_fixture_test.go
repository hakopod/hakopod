package cluster

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
)

// Export-only generation for an owned development VM or opt-in CI run:
// HAKOPOD_ACTIONS_EXPORT_BENCHMARK=1 HAKOPOD_ACTIONS_RUNTIME_FIXTURE_DIR=<directory>
// go test -p=1 ./internal/cluster -run '^TestActionsExportBenchmarkFixture$' -count=1
// This uses a fake Kubernetes client and never reads a provider credential.
func TestActionsExportBenchmarkFixture(t *testing.T) {
	directory := os.Getenv("HAKOPOD_ACTIONS_RUNTIME_FIXTURE_DIR")
	if directory == "" || os.Getenv("HAKOPOD_ACTIONS_EXPORT_BENCHMARK") != "1" {
		t.Skip("only used by the opt-in export benchmark harness")
	}
	workload, err := os.ReadFile("../../scripts/actions/export-benchmark.py")
	if err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientset()
	c := &Client{kube: kube}
	target := runnerTarget(t)
	target.ApplicationID = "actions-export-benchmark"
	target.Project = "actions-development-fixture"
	s := target.Spec.Services["runner"]
	s.NodeName = "k3d-hakopod-dev-server-0"
	s.Resources = &spec.Resources{CPURequest: "500m", CPULimit: "2", MemoryRequest: "1Gi", MemoryLimit: "4Gi"}
	s.Actions.TimeoutMinutes = 12
	s.Actions.WorkspaceSizeGiB = 4
	target.Spec.Services["runner"] = s
	if err := c.bootstrap(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	pod := actionsPod(target, "runner", "runtime-fixture", s)
	pod.APIVersion, pod.Kind = "v1", "Pod"
	// The benchmark never registers with GitHub. Remove the unused JIT projection
	// rather than mount even a synthetic registration secret into the workload.
	pod.Spec.Volumes = pod.Spec.Volumes[:1]
	pod.Spec.Containers[0].VolumeMounts = pod.Spec.Containers[0].VolumeMounts[:1]
	flags := ""
	if os.Getenv("HAKOPOD_ACTIONS_EXPORT_FORCE_OVERLAY_DIFF") == "1" {
		flags = ", '--force-overlay-diff'"
	}
	// Preserve the bounded JSON report in logs before the runner exits. kubectl
	// cannot exec into a completed container to recover its workspace file.
	wrapper := fmt.Sprintf(`import base64, json, sys, traceback
from pathlib import Path
sys.argv = ['export-benchmark.py'%s]
status = 0
try:
    exec(compile(base64.b64decode('%s'), 'export-benchmark.py', 'exec'), {'__name__': '__main__'})
except SystemExit as error:
    status = 0 if error.code is None else error.code if isinstance(error.code, int) else 1
except BaseException:
    traceback.print_exc()
    status = 1
reports = list(Path('/home/runner/_work').glob('hako-export-*/report.json'))
if len(reports) != 1:
    print('HAKOPOD_EXPORT_REPORT_ERROR missing or ambiguous report', flush=True)
    status = 1
else:
    with reports[0].open('rb') as source:
        data = source.read(1048577)
    if len(data) > 1048576:
        print('HAKOPOD_EXPORT_REPORT_ERROR report exceeded 1 MiB', flush=True)
        status = 1
    else:
        print('HAKOPOD_EXPORT_REPORT ' + json.dumps(json.loads(data), separators=(',', ':')), flush=True)
raise SystemExit(status)
`, flags, base64.StdEncoding.EncodeToString(workload))
	pod.Spec.Containers[0].Command = []string{"python3", "-u", "-c", wrapper}
	pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{Name: "HAKOPOD_EXPORT_BENCHMARK_DEV_CONTEXT", Value: "k3d-hakopod-dev"})
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken || pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != ActionsRuntime || pod.Spec.HostNetwork || pod.Spec.HostPID || pod.Spec.HostIPC {
		t.Fatal("export benchmark lost the product sandbox boundary")
	}
	if len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].EmptyDir == nil || pod.Spec.Volumes[0].EmptyDir.SizeLimit == nil || pod.Spec.Volumes[0].EmptyDir.SizeLimit.Cmp(resource.MustParse("4Gi")) != 0 {
		t.Fatal("export benchmark must use only the bounded 4 GiB product workspace")
	}
	for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		if len(container.EnvFrom) != 0 || container.SecurityContext == nil || (container.SecurityContext.Privileged != nil && *container.SecurityContext.Privileged) {
			t.Fatal("export benchmark container has unexpected credentials or privilege")
		}
		for _, value := range container.Env {
			if value.ValueFrom != nil || (value.Name != "DOCKER_HOST" && value.Name != "ACTIONS_RUNNER_PRINT_LOG_TO_STDOUT" && value.Name != "HAKOPOD_EXPORT_BENCHMARK_DEV_CONTEXT") {
				t.Fatal("export benchmark received an unexpected environment source")
			}
		}
	}
	items := []runtime.Object{}
	for _, action := range kube.Actions() {
		if created, ok := action.(clienttesting.CreateAction); ok {
			object := created.GetObject()
			if _, secret := object.(*corev1.Secret); secret {
				t.Fatal("export benchmark must not inject any secret")
			}
			kinds, _, err := scheme.Scheme.ObjectKinds(object)
			if err != nil || len(kinds) != 1 {
				t.Fatalf("fixture object kind unavailable: %T %v", object, err)
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
		labels["hakopod.io/development-fixture"] = "actions-export-benchmark"
		metadata.SetLabels(labels)
	}
	data, err := json.MarshalIndent(map[string]any{"apiVersion": "v1", "kind": "List", "items": items}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "export-benchmark.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// TestActionsRuntimeAcceptanceFixtures exports the product pod and namespace
// policy without contacting a cluster or accepting a GitHub credential. The CI
// harness applies these development fixtures only to a disposable named cluster.
func TestActionsRuntimeAcceptanceFixtures(t *testing.T) {
	directory := os.Getenv("HAKOPOD_ACTIONS_RUNTIME_FIXTURE_DIR")
	if directory == "" {
		t.Skip("only used by the disposable runtime acceptance harness")
	}
	workload, err := os.ReadFile("../../scripts/actions/runtime-workload.py")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workload", "disk", "replacement"} {
		kube := fake.NewClientset()
		c := &Client{kube: kube}
		target := runnerTarget(t)
		target.ApplicationID = "actions-acceptance-" + name
		if name == "replacement" {
			target.ApplicationID = "actions-acceptance-disk"
		}
		target.Project = "actions-development-fixture"
		s := target.Spec.Services["runner"]
		s.NodeName = "k3d-hakopod-dev-server-0"
		s.Resources = &spec.Resources{CPURequest: "500m", CPULimit: "2", MemoryRequest: "1Gi", MemoryLimit: "4Gi"}
		s.Actions.TimeoutMinutes = 24
		s.Actions.WorkspaceSizeGiB = 8
		if name != "workload" {
			s.Actions.WorkspaceSizeGiB = 2
			s.Actions.TimeoutMinutes = 5
		}
		target.Spec.Services["runner"] = s
		if err := c.bootstrap(context.Background(), target); err != nil {
			t.Fatal(err)
		}
		const slot = "runtime-fixture"
		if err := c.SaveActionsConfig(context.Background(), target, "runner", slot, "unused-development-fixture"); err != nil {
			t.Fatal(err)
		}
		items := []runtime.Object{}
		for _, action := range kube.Actions() {
			if created, ok := action.(clienttesting.CreateAction); ok {
				object := created.GetObject()
				kinds, _, err := scheme.Scheme.ObjectKinds(object)
				if err != nil || len(kinds) != 1 {
					t.Fatalf("fixture object kind unavailable: %T %v", object, err)
				}
				object.GetObjectKind().SetGroupVersionKind(kinds[0])
				items = append(items, object)
			}
		}
		pod := actionsPod(target, "runner", slot, s)
		pod.APIVersion, pod.Kind = "v1", "Pod"
		pod.Spec.Containers[0].Command = []string{"python3", "-u", "-c", string(workload)}
		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{Name: "ACCEPTANCE_SCENARIO", Value: name})
		items = append(items, pod)
		data, err := json.MarshalIndent(map[string]any{"apiVersion": "v1", "kind": "List", "items": items}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestActionsRuntimeImageObservationLive(t *testing.T) {
	expected := os.Getenv("HAKOPOD_ACTIONS_RUNTIME_IMAGE_STATE")
	if expected == "" {
		t.Skip("requires the disposable runtime acceptance fixture")
	}
	configPath := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(configPath)
	if os.Getenv("GITHUB_ACTIONS") != "true" || err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("only the named disposable CI cluster is permitted")
	}
	c, err := New(configPath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	target := runnerTarget(t)
	target.ApplicationID = "actions-acceptance-workload"
	target.Project = "actions-development-fixture"
	images, err := c.ActionsPodImages(ctx, target, "runner")
	if err != nil {
		t.Fatal(err)
	}
	switch expected {
	case "running":
		if len(images) != 1 || !strings.Contains(images[0], "ghcr.io/hakopod/actions-runner") || !strings.Contains(images[0], "@sha256:") {
			t.Fatalf("expected one observed running managed image, got %v", images)
		}
		nodes, err := c.PlacementNodesForRuntime(ctx, target, "actions")
		if err != nil || len(nodes) != 1 || nodes[0].Name != "k3d-hakopod-dev-server-0" || !nodes[0].Available {
			t.Fatalf("named development node is not an eligible runner target: %v %v", nodes, err)
		}
		service := target.Spec.Services["runner"]
		service.NodeName = "k3d-hakopod-dev-server-0"
		service.Architecture = nodes[0].Architecture
		if err := c.ActionsPoolAvailable(ctx, target, service); err != nil {
			t.Fatalf("selected development node failed pre-registration placement: %v", err)
		}
		service.NodeName = "hakopod-actions-deliberately-missing-fixture"
		if err := c.ActionsPoolAvailable(ctx, target, service); err == nil || !strings.Contains(err.Error(), "No ready Managed Actions node matches") {
			t.Fatalf("missing selected node passed pre-registration placement: %v", err)
		}
		if err := c.StartActionsPod(ctx, target, "runner", "missing-placement", service); err == nil || !strings.Contains(err.Error(), "No ready Managed Actions node matches") {
			t.Fatalf("missing selected node did not fail before registration/pod creation: %v", err)
		}
	case "absent":
		if len(images) != 0 {
			t.Fatalf("deleted pod still reported an image: %v", images)
		}
	default:
		t.Fatal("unexpected image observation scenario")
	}
}
