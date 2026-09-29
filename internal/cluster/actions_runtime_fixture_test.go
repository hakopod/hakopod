package cluster

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
)

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
		service.NodeName = "hakopod-actions-deliberately-missing-fixture"
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
