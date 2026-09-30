package cluster

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

const actionsExportStockBuildkit = "docker.io/moby/buildkit:v0.32.2@sha256:28a898719c18a33f4e8000685287fa36fd0dd9560c6440227d3a732d79bb41d8"

func actionsExportBenchmarkArguments(force bool, image, integrationSHA string) (string, error) {
	args := []string{"export-benchmark.py"}
	if force {
		args = append(args, "--force-overlay-diff")
	}
	if image != "" {
		candidate := regexp.MustCompile(`^ghcr\.io/hakopod/buildkit:v0\.32\.2-hakopod-[a-f0-9]{40}@sha256:[a-f0-9]{64}$`)
		if image != actionsExportStockBuildkit && !candidate.MatchString(image) {
			return "", fmt.Errorf("BuildKit image must be the stock pin or a digest-pinned Hakopod v0.32.2 candidate")
		}
		args = append(args, "--buildkit-image", image)
	}
	if integrationSHA != "" {
		if !strings.HasPrefix(image, "ghcr.io/hakopod/buildkit:") || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(integrationSHA) {
			return "", fmt.Errorf("native tests require a pinned candidate and exact helper checksum")
		}
		args = append(args, "--integration-tests", "--integration-helper-sha256", integrationSHA)
	}
	data, err := json.Marshal(args)
	return string(data), err
}

func TestActionsExportBenchmarkArguments(t *testing.T) {
	candidate := "ghcr.io/hakopod/buildkit:v0.32.2-hakopod-" + strings.Repeat("a", 40) + "@sha256:" + strings.Repeat("b", 64)
	for _, image := range []string{"", actionsExportStockBuildkit, candidate} {
		for _, force := range []bool{false, true} {
			encoded, err := actionsExportBenchmarkArguments(force, image, "")
			if err != nil {
				t.Fatal(err)
			}
			var args []string
			if err := json.Unmarshal([]byte(encoded), &args); err != nil {
				t.Fatal(err)
			}
			expected := []string{"export-benchmark.py"}
			if force {
				expected = append(expected, "--force-overlay-diff")
			}
			if image != "" {
				expected = append(expected, "--buildkit-image", image)
			}
			if fmt.Sprint(args) != fmt.Sprint(expected) {
				t.Fatalf("unexpected benchmark arguments: %v", args)
			}
		}
	}
	for _, image := range []string{
		strings.Split(candidate, "@")[0], " " + candidate, candidate + "\n", candidate + ",network=host",
		strings.Replace(candidate, "ghcr.io/", "ghcr.io.evil/", 1),
		strings.Replace(candidate, "/hakopod/", "/other/", 1),
		strings.Replace(candidate, "v0.32.2", "v0.32.3", 1),
		strings.Replace(candidate, strings.Repeat("a", 40), strings.Repeat("a", 39), 1),
		strings.Replace(candidate, strings.Repeat("b", 64), strings.Repeat("B", 64), 1),
		"docker.io/moby/buildkit:v0.32.2@sha256:" + strings.Repeat("b", 64),
	} {
		if _, err := actionsExportBenchmarkArguments(false, image, ""); err == nil {
			t.Fatalf("unapproved BuildKit image was accepted: %q", image)
		}
	}
	checksum := strings.Repeat("c", 64)
	encoded, err := actionsExportBenchmarkArguments(false, candidate, checksum)
	if err != nil || !strings.Contains(encoded, `"--integration-tests","--integration-helper-sha256","`+checksum+`"`) {
		t.Fatalf("native test selection was not preserved: %v", err)
	}
	for _, image := range []string{"", actionsExportStockBuildkit} {
		if _, err := actionsExportBenchmarkArguments(false, image, checksum); err == nil {
			t.Fatal("native tests accepted a stock image")
		}
	}
	if _, err := actionsExportBenchmarkArguments(false, candidate, "not-a-checksum"); err == nil {
		t.Fatal("native tests accepted an invalid helper checksum")
	}
}

// Export-only generation for an owned development VM or opt-in CI run:
// HAKOPOD_ACTIONS_EXPORT_BENCHMARK=1 HAKOPOD_ACTIONS_RUNTIME_FIXTURE_DIR=<directory>
// HAKOPOD_ACTIONS_EXPORT_BUILDKIT_IMAGE optionally selects an immutable candidate.
// go test -p=1 ./internal/cluster -run '^TestActionsExportBenchmarkFixture$' -count=1
// This uses a fake Kubernetes client and never reads a provider credential.
func TestActionsExportBenchmarkFixture(t *testing.T) {
	helperSHA := ""
	if os.Getenv("HAKOPOD_ACTIONS_EXPORT_INTEGRATION_TESTS") == "1" {
		helper, err := os.ReadFile("../../scripts/actions/buildkit-integration.py")
		if err != nil || len(helper) == 0 || len(helper) > 128*1024 {
			t.Fatalf("native test helper exceeds its source bound: %v", err)
		}
		helperSHA = fmt.Sprintf("%x", sha256.Sum256(helper))
	}
	arguments, err := actionsExportBenchmarkArguments(os.Getenv("HAKOPOD_ACTIONS_EXPORT_FORCE_OVERLAY_DIFF") == "1", os.Getenv("HAKOPOD_ACTIONS_EXPORT_BUILDKIT_IMAGE"), helperSHA)
	if err != nil {
		t.Fatal(err)
	}
	directory := os.Getenv("HAKOPOD_ACTIONS_RUNTIME_FIXTURE_DIR")
	if directory == "" || os.Getenv("HAKOPOD_ACTIONS_EXPORT_BENCHMARK") != "1" {
		t.Skip("only used by the opt-in export benchmark harness")
	}
	workload, err := os.ReadFile("../../scripts/actions/export-benchmark.py")
	if err != nil {
		t.Fatal(err)
	}
	if len(workload) == 0 || len(workload) > 128*1024 {
		t.Fatal("export benchmark source exceeds its 128 KiB bound")
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
	// Keep the exact source within the bounded Pod and command inputs, including
	// the optional VM observer. This changes transport only, not the workload.
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(workload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	// Preserve the bounded JSON report in logs before the runner exits. kubectl
	// cannot exec into a completed container to recover its workspace file.
	wrapper := fmt.Sprintf(`import base64, gzip, hashlib, io, json, sys, traceback
from pathlib import Path
sys.argv = %s
status = 0
try:
    with gzip.GzipFile(fileobj=io.BytesIO(base64.b64decode('%s', validate=True))) as source:
        workload = source.read(131073)
    if len(workload) != %d or hashlib.sha256(workload).hexdigest() != '%x':
        raise RuntimeError('Export benchmark source identity mismatch')
    exec(compile(workload, 'export-benchmark.py', 'exec'), {'__name__': '__main__'})
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
`, arguments, base64.StdEncoding.EncodeToString(compressed.Bytes()), len(workload), sha256.Sum256(workload))
	if len(wrapper) >= 120*1024 {
		t.Fatal("export benchmark command exceeds its 120 KiB bound")
	}
	pod.Spec.Containers[0].Command = []string{"python3", "-u", "-c", wrapper}
	pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{Name: "HAKOPOD_EXPORT_BENCHMARK_DEV_CONTEXT", Value: "k3d-hakopod-dev"})
	podJSON, err := json.Marshal(pod)
	if err != nil || len(podJSON) > 65536 {
		t.Fatalf("export benchmark Pod exceeds its 64 KiB bound: %v", err)
	}
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
