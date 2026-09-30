package cluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func actionsServicePullWrapper(source []byte) (string, error) {
	encoded, err := actionsFixtureSources([]string{"service-pull-benchmark.py"}, map[string][]byte{"service-pull-benchmark.py": source})
	if err != nil {
		return "", err
	}
	wrapper := fmt.Sprintf(`import base64, gzip, hashlib, io, sys
sys.dont_write_bytecode = True
files = %s
if len(files) != 1 or files[0]['name'] != 'service-pull-benchmark.py':
    raise RuntimeError('Service pull source inventory changed')
item = files[0]
with gzip.GzipFile(fileobj=io.BytesIO(base64.b64decode(item['gzip_base64'], validate=True))) as source:
    data = source.read(131073)
if not 0 < len(data) <= 131072 or len(data) != item['size'] or hashlib.sha256(data).hexdigest() != item['sha256']:
    raise RuntimeError('Service pull source identity mismatch')
exec(compile(data, item['name'], 'exec'), {'__name__': '__main__'})
`, encoded)
	if len(wrapper) >= 120*1024 {
		return "", fmt.Errorf("service pull command exceeds its 120 KiB bound")
	}
	return wrapper, nil
}

func TestActionsServicePullWrapperBounds(t *testing.T) {
	wrapper, err := actionsServicePullWrapper([]byte("print('fixture')\n"))
	if err != nil || !strings.Contains(wrapper, "Service pull source identity mismatch") {
		t.Fatalf("service pull source wrapper is unavailable: %v", err)
	}
	for _, source := range [][]byte{nil, bytes.Repeat([]byte("x"), 128*1024+1)} {
		if _, err := actionsServicePullWrapper(source); err == nil {
			t.Fatal("service pull accepted missing or oversized source")
		}
	}
}

func TestActionsServicePullBenchmarkFixture(t *testing.T) {
	if os.Getenv("HAKOPOD_ACTIONS_SERVICE_PULL_BENCHMARK") != "1" {
		t.Skip("only used by the opt-in cold service image benchmark")
	}
	for _, name := range []string{"HAKOPOD_ACTIONS_BUILDKIT_CANDIDATE", "HAKOPOD_ACTIONS_PUBLISH_CANDIDATE", "HAKOPOD_ACTIONS_EXPORT_BENCHMARK", "HAKOPOD_ACTIONS_BUILDKIT_QUALIFICATION", "HAKOPOD_ACTIONS_EXPORT_FORCE_OVERLAY_DIFF", "HAKOPOD_ACTIONS_EXPORT_INTEGRATION_TESTS", "HAKOPOD_ACTIONS_RUNTIME_FORCE_OVERLAY_DIFF"} {
		if value := os.Getenv(name); value != "" && value != "0" {
			t.Fatal("service pull must run separately from other diagnostics")
		}
	}
	for _, name := range []string{"HAKOPOD_ACTIONS_EXPORT_BUILDKIT_IMAGE", "HAKOPOD_ACTIONS_EXPORT_TEST_RUN", "HAKOPOD_ACTIONS_RUNTIME_BUILDKIT_IMAGE"} {
		if os.Getenv(name) != "" {
			t.Fatal("service pull cannot select a BuildKit image or native test artifacts")
		}
	}
	driver := os.Getenv("HAKOPOD_ACTIONS_DOCKER_STORAGE_DRIVER")
	if driver == "" {
		driver = "vfs"
	}
	if driver != "vfs" && driver != "overlay2" {
		t.Fatal("service pull requires vfs or overlay2")
	}
	if driver == "overlay2" && os.Getenv("HAKOPOD_ACTIONS_SHARED_WORKSPACE") != "1" {
		t.Fatal("service pull overlay2 requires the explicit shared workspace")
	}
	directory := os.Getenv("HAKOPOD_ACTIONS_RUNTIME_FIXTURE_DIR")
	if directory == "" {
		t.Skip("fixture output directory was not requested")
	}
	source, err := os.ReadFile(filepath.Join("../../scripts/actions", "service-pull-benchmark.py"))
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := actionsServicePullWrapper(source)
	if err != nil {
		t.Fatal(err)
	}
	writeActionsDiagnosticFixtureOptions(t, directory, "service-pull-benchmark.json", wrapper, actionsDiagnosticFixtureOptions{
		label: "actions-service-pull-benchmark", contextEnv: "HAKOPOD_SERVICE_PULL_BENCHMARK_DEV_CONTEXT", timeoutMinutes: 30,
		runnerEnv: []corev1.EnvVar{{Name: "HAKOPOD_ACTIONS_DOCKER_STORAGE_DRIVER", Value: driver}},
	})
}

func TestActionsServicePullFixtureIsolation(t *testing.T) {
	for _, selection := range [][2]string{{"0", "vfs"}, {"1", "vfs"}, {"1", "overlay2"}} {
		t.Run(selection[0]+"-"+selection[1], func(t *testing.T) {
			t.Setenv("HAKOPOD_ACTIONS_SHARED_WORKSPACE", selection[0])
			t.Setenv("HAKOPOD_ACTIONS_DOCKER_STORAGE_DRIVER", selection[1])
			directory := t.TempDir()
			writeActionsDiagnosticFixtureOptions(t, directory, "service-pull-benchmark.json", "print('fixture')", actionsDiagnosticFixtureOptions{
				label: "actions-service-pull-benchmark", contextEnv: "HAKOPOD_SERVICE_PULL_BENCHMARK_DEV_CONTEXT", timeoutMinutes: 30,
				runnerEnv: []corev1.EnvVar{{Name: "HAKOPOD_ACTIONS_DOCKER_STORAGE_DRIVER", Value: selection[1]}},
			})
			data, err := os.ReadFile(filepath.Join(directory, "service-pull-benchmark.json"))
			if err != nil {
				t.Fatal(err)
			}
			var fixture struct {
				Items []json.RawMessage `json:"items"`
			}
			if err := json.Unmarshal(data, &fixture); err != nil {
				t.Fatal(err)
			}
			pods := 0
			for _, raw := range fixture.Items {
				var pod corev1.Pod
				if err := json.Unmarshal(raw, &pod); err != nil {
					t.Fatal(err)
				}
				if pod.Labels["hakopod.io/development-fixture"] != "actions-service-pull-benchmark" || pod.Kind == "Secret" {
					t.Fatal("service pull fixture lost its explicit marker or includes credentials")
				}
				if pod.Kind != "Pod" {
					continue
				}
				pods++
				if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 1800 || pod.Spec.Containers[0].Resources.Limits.Memory().Value() != 896*1024*1024 || pod.Spec.Containers[0].Resources.Limits.Cpu().MilliValue() != 475 {
					t.Fatal("service pull resource or deadline bounds changed")
				}
				if (len(pod.Annotations) != 0) != (selection[0] == "1") {
					t.Fatal("service pull shared workspace changed without selection")
				}
				expectedDaemon := actionsDaemon
				if selection[1] == "overlay2" {
					expectedDaemon = strings.Replace(actionsDaemon, "--storage-driver=vfs", "--storage-driver=overlay2", 1)
				}
				if pod.Spec.InitContainers[1].Image != ActionsDaemonImage || pod.Spec.InitContainers[1].Command[2] != expectedDaemon {
					t.Fatal("service pull changed its pinned product daemon or selected driver")
				}
			}
			if pods != 1 {
				t.Fatal("service pull must create exactly one runner Pod")
			}
		})
	}
}
