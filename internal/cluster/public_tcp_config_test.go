package cluster

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestPublicTCPPortOperatorConfiguration(t *testing.T) {
	for _, invalid := range []string{"80", "6443", "587,587", "587,", "0", "65536", "587;echo no", "2587,22"} {
		if _, err := ParsePublicTCPPorts(invalid); err == nil {
			t.Fatalf("accepted invalid port list %q", invalid)
		}
	}
	ports, err := ParsePublicTCPPorts("587, 465")
	if err != nil || len(ports) != 2 || ports[0] != 465 || ports[1] != 587 {
		t.Fatalf("ports=%v err=%v", ports, err)
	}
}

func TestPublicTCPPortInventoryBound(t *testing.T) {
	values := make([]string, 257)
	for i := range values {
		values[i] = strconv.Itoa(20000 + i)
	}
	ports, err := ParsePublicTCPPorts(strings.Join(values[:256], ","))
	if err != nil || len(ports) != 256 {
		t.Fatal("bounded administrator inventory rejected", err)
	}
	if _, err := ParsePublicTCPPorts(strings.Join(values, ",")); err == nil {
		t.Fatal("unbounded port inventory accepted")
	}
}

func TestPublicTCPDeploymentModes(t *testing.T) {
	for _, raw := range []string{"", DeploymentSelfHosted, DeploymentManagedCloud} {
		mode, err := ParseDeploymentMode(raw)
		if err != nil || (raw == "" && mode != DeploymentSelfHosted) {
			t.Fatal("valid deployment mode rejected", err)
		}
		policy := (&Client{options: Options{DeploymentMode: raw}}).PublicTCPPolicy()
		if policy.Mode != mode || policy.Allowed != (mode == DeploymentSelfHosted) || policy.Message == "" {
			t.Fatal("wrong installation policy", policy)
		}
	}
	for _, raw := range []string{"managed_cloud", "cloud", "SELF-HOSTED", "self-hosted ", "managed-cloud\n"} {
		if _, err := ParseDeploymentMode(raw); err == nil {
			t.Fatal("invalid deployment mode accepted")
		}
		if policy := (&Client{options: Options{DeploymentMode: raw}}).PublicTCPPolicy(); policy.Allowed {
			t.Fatal("malformed deployment mode failed open")
		}
		if _, err := New("nonexistent-kubeconfig", Options{DeploymentMode: raw}); err == nil || !strings.Contains(err.Error(), "HAKOPOD_DEPLOYMENT_MODE") {
			t.Fatal("constructor touched Kubernetes before validating deployment mode", err)
		}
	}
	if _, err := New("nonexistent-kubeconfig", Options{DeploymentMode: DeploymentManagedCloud, PublicTCPPorts: []int32{587}}); err == nil || !strings.Contains(err.Error(), "cannot configure public TCP") {
		t.Fatal("managed-cloud constructor accepted public port provisioning", err)
	}
}

func TestManagedCloudRejectsPreprovisionedPublicTCPWithoutKubernetesCalls(t *testing.T) {
	c, target := publicTCPTestClient(t)
	ctx := context.Background()
	obj := publicTCPObject(target, "haproxy")
	if _, err := c.dynamic.Resource(publicTCPResource).Namespace(obj.GetNamespace()).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.options.DeploymentMode = DeploymentManagedCloud
	kube := c.kube.(*kubefake.Clientset)
	dynamic := c.dynamic.(*dynamicfake.FakeDynamicClient)
	kube.ClearActions()
	dynamic.ClearActions()
	for _, call := range []func(context.Context, Target) error{c.ValidatePublicTCP, c.PreparePublicTCP, c.ReconcilePublicTCP} {
		if err := call(ctx, target); !errors.Is(err, ErrPublicTCPDisabled) {
			t.Fatal("managed-cloud allowed preprovisioned tenant TCP", err)
		}
	}
	statuses, err := c.ObservePublicTCP(ctx, target, "mail")
	if err != nil || len(statuses) != 1 || statuses[0].Status != "disabled" || !strings.Contains(statuses[0].Message, "managed cloud") {
		t.Fatal("legacy public listener mislabeled as configured", statuses, err)
	}
	if len(kube.Actions()) != 0 || len(dynamic.Actions()) != 0 {
		t.Fatal("mode denial read or mutated Kubernetes")
	}
	c.kube, c.dynamic = nil, nil
	svc := target.Spec.Services["mail"]
	svc.PublicTCP = nil
	svc.Ports = []spec.Port{{Name: "smtp", Port: 587, TargetPort: 1587, Protocol: "TCP"}}
	target.Spec.Services["mail"] = svc
	if err := c.ValidatePublicTCP(ctx, target); err != nil {
		t.Fatal("managed-cloud blocked private TCP", err)
	}
	statuses, err = c.ObservePublicTCP(ctx, target, "mail")
	if err != nil || len(statuses) != 0 {
		t.Fatal("private port treated as a disabled public listener", statuses, err)
	}
}

func TestManagedCloudStartupRequiresCleanOrAcknowledgedInventory(t *testing.T) {
	legacy := schema.GroupVersionResource{Group: "ingress.v1.haproxy.org", Version: "v1", Resource: "tcps"}
	for _, scenario := range []string{"clean", "active-v3", "active-v1", "empty-acknowledged", "empty-pending", "empty-wrong-hash", "malformed", "wrong-owner", "unowned"} {
		t.Run(scenario, func(t *testing.T) {
			c, target := publicTCPTestClient(t)
			c.options.DeploymentMode = DeploymentManagedCloud
			c.options.PublicTCPPorts = nil
			if scenario != "clean" {
				obj := publicTCPObject(target, "haproxy")
				resource := publicTCPResource
				if scenario == "active-v1" {
					obj.SetAPIVersion("ingress.v1.haproxy.org/v1")
					resource = legacy
				}
				if strings.HasPrefix(scenario, "empty-") {
					obj.Object["spec"] = []any{}
					if scenario == "empty-acknowledged" {
						obj.SetAnnotations(map[string]string{"hakopod.io/tcp-acknowledged": publicTCPHash([]any{})})
					} else if scenario == "empty-wrong-hash" {
						obj.SetAnnotations(map[string]string{"hakopod.io/tcp-acknowledged": "incorrect"})
					}
				}
				if scenario == "malformed" {
					obj.Object["spec"] = map[string]any{"unexpected": "shape"}
				}
				if scenario == "wrong-owner" {
					obj.SetNamespace("operator-namespace")
				}
				if scenario == "unowned" {
					obj.SetLabels(map[string]string{managedBy: "other"})
				}
				if _, err := c.dynamic.Resource(resource).Namespace(obj.GetNamespace()).Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			dynamic := c.dynamic.(*dynamicfake.FakeDynamicClient)
			dynamic.ClearActions()
			err := c.ValidatePublicTCPInstallation(context.Background())
			allowed := scenario == "clean" || scenario == "empty-acknowledged" || scenario == "unowned"
			if (err == nil) != allowed {
				t.Fatal("startup inventory policy mismatch", err)
			}
			for _, action := range dynamic.Actions() {
				if action.GetVerb() != "list" {
					t.Fatal("startup gate changed cluster resources")
				}
			}
		})
	}
}

func TestManagedCloudStartupInventoryFailures(t *testing.T) {
	for _, scenario := range []string{"api-missing", "read-failure", "overflow", "continuation"} {
		t.Run(scenario, func(t *testing.T) {
			c, _ := publicTCPTestClient(t)
			c.options.DeploymentMode, c.options.PublicTCPPorts = DeploymentManagedCloud, nil
			dynamic := c.dynamic.(*dynamicfake.FakeDynamicClient)
			dynamic.PrependReactor("list", "tcps", func(action ktesting.Action) (bool, runtime.Object, error) {
				if scenario == "api-missing" {
					return true, nil, apierrors.NewNotFound(action.GetResource().GroupResource(), "tcps")
				}
				if scenario == "read-failure" {
					return true, nil, errors.New("test connection failure")
				}
				list := &unstructured.UnstructuredList{}
				if scenario == "overflow" {
					list.Items = make([]unstructured.Unstructured, 257)
					for i := range list.Items {
						list.Items[i].SetLabels(map[string]string{managedBy: "hakopod", ownerKey: "fixture"})
					}
				} else {
					list.SetContinue("more-routes")
				}
				return true, list, nil
			})
			if err := c.ValidatePublicTCPInstallation(context.Background()); (err == nil) != (scenario == "api-missing") {
				t.Fatal("startup did not fail closed for unavailable/partial inventory", err)
			}
		})
	}
	if err := (&Client{options: Options{DeploymentMode: DeploymentManagedCloud}}).ValidatePublicTCPInstallation(context.Background()); err == nil {
		t.Fatal("managed-cloud startup accepted missing runtime client")
	}
	if err := (&Client{}).ValidatePublicTCPInstallation(context.Background()); err != nil {
		t.Fatal("self-hosted mode performed managed-cloud inventory check", err)
	}
}
