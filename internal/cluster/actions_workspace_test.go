package cluster

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	stdruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func workspaceRuntimeClass() *nodev1.RuntimeClass {
	return &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: ActionsRuntime, Labels: map[string]string{managedBy: "hakopod"}}, Handler: ActionsRuntime,
		Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{"hakopod.io/actions-runtime": "ready"}}}
}

func workspaceNode(name, capability string) *corev1.Node {
	node := placementNode(name)
	node.Labels["hakopod.io/actions-runtime"] = "ready"
	if capability != "" {
		node.Labels[ActionsWorkspaceCapabilityLabel] = capability
	}
	return node
}

func TestActionsWorkspaceSelectionRespectsOperatorAndCloudOwnership(t *testing.T) {
	shared := ActionsWorkspaceSharedOverlay2V1
	for _, test := range []struct {
		name, mode, nodeName, capability string
		policy                           *WorkloadPolicy
		provider                         actions.Provider
		want                             ActionsWorkspaceProfile
		failure                          bool
	}{
		{name: "legacy self-hosted", want: ActionsWorkspaceVFS},
		{name: "enrolled self-hosted", capability: string(shared), want: shared},
		{name: "stale self-hosted enrollment", capability: "shared-overlay2-v0", want: ActionsWorkspaceVFS},
		{name: "named legacy node cannot borrow enrollment", nodeName: "selected", want: ActionsWorkspaceVFS},
		{name: "named enrolled node", nodeName: "selected", capability: string(shared), want: shared},
		{name: "explicit VFS wins over enrollment", capability: string(shared), policy: &WorkloadPolicy{NodeName: "selected", ActionsWorkspaceProfile: ActionsWorkspaceVFS}, want: ActionsWorkspaceVFS},
		{name: "empty policy remains VFS", capability: string(shared), policy: &WorkloadPolicy{NodeName: "selected"}, want: ActionsWorkspaceVFS},
		{name: "cloud requires explicit profile", mode: DeploymentManagedCloud, capability: string(shared), policy: &WorkloadPolicy{NodeName: "selected"}, want: ActionsWorkspaceVFS},
		{name: "cloud operator cannot infer profile", mode: DeploymentManagedCloud, capability: string(shared), want: ActionsWorkspaceVFS},
		{name: "cloud shared allocation", mode: DeploymentManagedCloud, capability: string(shared), policy: &WorkloadPolicy{NodeName: "selected", ActionsWorkspaceProfile: shared}, want: shared},
		{name: "cloud missing capability", mode: DeploymentManagedCloud, policy: &WorkloadPolicy{NodeName: "selected", ActionsWorkspaceProfile: shared}, failure: true},
		{name: "cloud stale capability", mode: DeploymentManagedCloud, capability: "shared-overlay2-v0", policy: &WorkloadPolicy{NodeName: "selected", ActionsWorkspaceProfile: shared}, failure: true},
		{name: "invalid profile", policy: &WorkloadPolicy{NodeName: "selected", ActionsWorkspaceProfile: "overlay2"}, failure: true},
		{name: "GitLab ignores enrollment", capability: string(shared), provider: actions.ProviderGitLab, want: ActionsWorkspaceVFS},
		{name: "Bitbucket ignores enrollment", capability: string(shared), provider: actions.ProviderBitbucket, want: ActionsWorkspaceVFS},
		{name: "GitLab does not inherit GitHub profile", capability: string(shared), provider: actions.ProviderGitLab, policy: &WorkloadPolicy{NodeName: "selected", ActionsWorkspaceProfile: shared}, want: ActionsWorkspaceVFS},
		{name: "Bitbucket does not inherit GitHub profile", capability: string(shared), provider: actions.ProviderBitbucket, policy: &WorkloadPolicy{NodeName: "selected", ActionsWorkspaceProfile: shared}, want: ActionsWorkspaceVFS},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := runnerTarget(t)
			service := target.Spec.Services["runner"]
			service.NodeName = test.nodeName
			service.Actions.Provider = test.provider
			selected := workspaceNode("selected", test.capability)
			kube := fake.NewClientset(selected, workspaceRuntimeClass())
			if test.nodeName != "" || test.policy != nil {
				if err := kube.Tracker().Add(workspaceNode("another-enrolled-node", string(shared))); err != nil {
					t.Fatal(err)
				}
			}
			client := &Client{kube: kube, options: Options{DeploymentMode: test.mode, OperatorNodeLimit: 1}}
			if test.policy != nil {
				client.options.WorkloadPolicy = func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
					return *test.policy, nil
				}
			}
			placement, err := client.actionsPoolPlacement(context.Background(), target, service)
			if test.failure {
				if err == nil {
					t.Fatal("invalid workspace placement was accepted")
				}
				return
			}
			if err != nil || placement.workspaceProfile != test.want {
				t.Fatal("unexpected workspace selection", placement, err)
			}
			wantCapability := ""
			if test.want == shared {
				wantCapability = string(shared)
			}
			if placement.selector[ActionsWorkspaceCapabilityLabel] != wantCapability {
				t.Fatal("scheduler capability differs from the selected profile", placement.selector)
			}
			if test.nodeName != "" && placement.nodeName != test.nodeName {
				t.Fatal("named node placement changed")
			}
		})
	}
}

func TestActionsWorkspaceSelfHostedPreferenceRetainsArchitectureAndTaints(t *testing.T) {
	target := runnerTarget(t)
	service := target.Spec.Services["runner"]
	service.Architecture = "amd64"
	legacy := workspaceNode("a-legacy", "")
	qualified := workspaceNode("z-qualified", string(ActionsWorkspaceSharedOverlay2V1))
	wrongArchitecture := workspaceNode("b-wrong-architecture", string(ActionsWorkspaceSharedOverlay2V1))
	wrongArchitecture.Labels["kubernetes.io/arch"] = "arm64"
	kube := fake.NewClientset(legacy, qualified, wrongArchitecture, workspaceRuntimeClass())
	client := &Client{kube: kube}
	placement, err := client.actionsPoolPlacement(context.Background(), target, service)
	if err != nil || placement.workspaceProfile != ActionsWorkspaceSharedOverlay2V1 || placement.selector["kubernetes.io/arch"] != "amd64" {
		t.Fatal("qualified node was not preferred with the original architecture", placement, err)
	}
	qualified.Spec.Taints = []corev1.Taint{{Key: "maintenance", Effect: corev1.TaintEffectNoSchedule}}
	if _, err := kube.CoreV1().Nodes().Update(context.Background(), qualified, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	placement, err = client.actionsPoolPlacement(context.Background(), target, service)
	if err != nil || placement.workspaceProfile != ActionsWorkspaceVFS {
		t.Fatal("shared preference bypassed architecture or scheduling taints", placement, err)
	}
}

func TestActionsWorkspaceNamedNodeCannotAdoptAnotherNodeResponse(t *testing.T) {
	client := &Client{kube: fake.NewClientset(workspaceRuntimeClass())}
	client.kube.(*fake.Clientset).PrependReactor("get", "nodes", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, workspaceNode("wrong-node", string(ActionsWorkspaceSharedOverlay2V1)), nil
	})
	target := runnerTarget(t)
	service := target.Spec.Services["runner"]
	service.NodeName = "selected"
	if _, err := client.actionsPoolPlacement(context.Background(), target, service); err == nil {
		t.Fatal("placement accepted a different node object")
	}
}

func TestActionsWorkspacePreferenceQueriesQualifiedFleetDirectly(t *testing.T) {
	kube := fake.NewClientset(workspaceRuntimeClass())
	kube.PrependReactor("list", "nodes", func(action clienttesting.Action) (bool, runtime.Object, error) {
		query := action.(clienttesting.ListAction).GetListRestrictions().Labels
		if query.Matches(labels.Set(workspaceNode("legacy", "").Labels)) {
			t.Fatal("shared preference scanned an unrelated generic inventory page")
		}
		return true, &corev1.NodeList{Items: []corev1.Node{*workspaceNode("qualified", string(ActionsWorkspaceSharedOverlay2V1))}}, nil
	})
	target := runnerTarget(t)
	client := &Client{kube: kube}
	placement, err := client.actionsPoolPlacement(context.Background(), target, target.Spec.Services["runner"])
	if err != nil || placement.workspaceProfile != ActionsWorkspaceSharedOverlay2V1 {
		t.Fatal("qualified fleet query failed", placement, err)
	}
}

func TestActionsWorkspaceCapabilityIsRecheckedImmediatelyBeforeCreate(t *testing.T) {
	for _, change := range []string{"capability", "runtime", "node"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			target := runnerTarget(t)
			service := target.Spec.Services["runner"]
			service.NodeName = "selected"
			node := workspaceNode("selected", string(ActionsWorkspaceSharedOverlay2V1))
			kube := fake.NewClientset(node, workspaceRuntimeClass())
			client := &Client{kube: kube}
			if err := client.SaveActionsConfig(ctx, target, "runner", "fresh", "development-fixture"); err != nil {
				t.Fatal(err)
			}
			checks := 0
			target.BeforeStep = func(context.Context) error {
				checks++
				if checks != 3 {
					return nil
				}
				switch change {
				case "capability":
					node.Labels[ActionsWorkspaceCapabilityLabel] = "shared-overlay2-v0"
					_, err := kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
					return err
				case "runtime":
					return kube.NodeV1().RuntimeClasses().Delete(ctx, ActionsRuntime, metav1.DeleteOptions{})
				default:
					return kube.CoreV1().Nodes().Delete(ctx, node.Name, metav1.DeleteOptions{})
				}
			}
			if err := client.StartActionsPod(ctx, target, "runner", "fresh", service); err == nil {
				t.Fatal("stale workspace placement created a runner")
			}
			for _, action := range kube.Actions() {
				if action.GetVerb() == "create" && action.GetResource().Resource == "pods" {
					t.Fatal("pod creation happened after capability was revoked")
				}
			}
		})
	}
}

func TestActionsWorkspaceProfilePreservesProductEnvelope(t *testing.T) {
	for _, size := range []int64{2, 3, 8, 16} {
		target := runnerTarget(t)
		service := target.Spec.Services["runner"]
		service.Actions.WorkspaceSizeGiB = size
		baseline := actionsPod(target, "runner", "fresh", service)
		for _, profile := range []ActionsWorkspaceProfile{"", ActionsWorkspaceVFS, ActionsWorkspaceSharedOverlay2V1} {
			pod := baseline.DeepCopy()
			if err := applyActionsWorkspaceProfile(pod, size, profile); err != nil {
				t.Fatal(err)
			}
			if profile == ActionsWorkspaceSharedOverlay2V1 {
				if len(pod.Annotations) != 3 || pod.Annotations["dev.gvisor.spec.mount.runner.type"] != "bind" || pod.Annotations["dev.gvisor.spec.mount.runner.share"] != "pod" || pod.Annotations["dev.gvisor.spec.mount.runner.options"] != fmt.Sprintf("rw,rprivate,mode=0770,uid=1001,gid=1001,size=%dg", size+1) || strings.Count(pod.Spec.InitContainers[1].Command[2], "--storage-driver=overlay2") != 1 || strings.Contains(pod.Spec.InitContainers[1].Command[2], "--storage-driver=vfs") {
					t.Fatal("shared profile did not apply the exact mount and driver contract")
				}
				if !strings.Contains(pod.Spec.InitContainers[0].Command[2], ".hakopod-shared-prepare") || !strings.HasPrefix(pod.Spec.Containers[0].Command[2], actionsWorkspaceStartupGuard) || !strings.HasSuffix(pod.Spec.Containers[0].Command[2], "os.execv('/usr/bin/python3', ['/usr/bin/python3', '/usr/local/lib/hakopod/observe.py'])\n") {
					t.Fatal("workspace guard does not precede GitHub registration")
				}
				pod.Annotations = nil
				pod.Spec.InitContainers[0].Command = baseline.Spec.InitContainers[0].Command
				pod.Spec.InitContainers[1].Command = baseline.Spec.InitContainers[1].Command
				pod.Spec.Containers[0].Command = baseline.Spec.Containers[0].Command
			}
			if !reflect.DeepEqual(pod, baseline) {
				t.Fatal("workspace profile changed resources, security, volumes, image pins, or unrelated pod fields")
			}
		}
	}
}

func TestActionsWorkspaceRejectsModifiedOrExistingPodBeforeMutation(t *testing.T) {
	for _, change := range []string{"profile", "size", "existing", "annotation", "prepare", "daemon", "observer", "memory"} {
		t.Run(change, func(t *testing.T) {
			target := runnerTarget(t)
			service := target.Spec.Services["runner"]
			pod := actionsPod(target, "runner", "fresh", service)
			profile, size := ActionsWorkspaceSharedOverlay2V1, spec.ActionsWorkspaceGiB(service.Actions)
			switch change {
			case "profile":
				profile = "unqualified-driver"
			case "size":
				size = 17
			case "existing":
				pod.ResourceVersion = "2"
			case "annotation":
				pod.Annotations = map[string]string{"dev.gvisor.flag.overlay2": "all:memory"}
			case "prepare":
				pod.Spec.InitContainers[0].Command[2] = "changed"
			case "daemon":
				pod.Spec.InitContainers[1].Command[2] += " --storage-driver=vfs"
			case "observer":
				pod.Spec.Containers[0].Command[2] = "changed"
			case "memory":
				pod.Spec.Volumes[0].EmptyDir.Medium = corev1.StorageMediumMemory
			}
			before := pod.DeepCopy()
			if err := applyActionsWorkspaceProfile(pod, size, profile); err == nil || !reflect.DeepEqual(pod, before) {
				t.Fatal("invalid profile mutated a pod", err)
			}
		})
	}
}

func TestActionsExistingPodRetainsStorageAndRequiresOriginalOwnership(t *testing.T) {
	for _, change := range []string{"none", "pod-owner", "pod-service", "provider", "secret-owner", "secret-service", "mutable-secret", "missing-secret", "config-volume"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			target := runnerTarget(t)
			service := target.Spec.Services["runner"]
			pod := actionsPod(target, "runner", "existing", service)
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace, Labels: labelsFor(target, "runner")}, Immutable: ptr(true), Data: map[string][]byte{"config": []byte("development-fixture")}}
			switch change {
			case "pod-owner":
				pod.Labels[ownerKey] = "another-application"
			case "pod-service":
				pod.Labels[serviceKey] = "another-service"
			case "provider":
				pod.Labels["hakopod.io/actions-provider"] = "gitlab"
			case "secret-owner":
				secret.Labels[ownerKey] = "another-application"
			case "secret-service":
				secret.Labels[serviceKey] = "another-service"
			case "mutable-secret":
				secret.Immutable = ptr(false)
			case "config-volume":
				pod.Spec.Volumes[1].Secret.SecretName = "different-secret"
			}
			kube := fake.NewClientset(pod)
			if change != "missing-secret" {
				if err := kube.Tracker().Add(secret); err != nil {
					t.Fatal(err)
				}
			}
			client := &Client{kube: kube, options: Options{WorkloadPolicy: func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
				t.Fatal("existing pod reselected its profile or placement")
				return WorkloadPolicy{}, errors.New("not reached")
			}}}
			err := client.StartActionsPod(ctx, target, "runner", "existing", service)
			if (change == "none") != (err == nil) {
				t.Fatal("existing runner ownership check was incorrect", err)
			}
			stored, readErr := kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if readErr != nil || !reflect.DeepEqual(stored, pod) {
				t.Fatal("existing pod was changed", readErr)
			}
			for _, action := range kube.Actions() {
				if action.GetVerb() != "get" {
					t.Fatal("existing pod caused an unexpected effect", action)
				}
			}
		})
	}
}

func TestActionsConfigReuseAndFreshPodRequireOriginalImmutableConfig(t *testing.T) {
	for _, change := range []string{"none", "mutable", "nil-immutable", "empty", "oversized", "wrong-service", "unowned"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			target := runnerTarget(t)
			service := target.Spec.Services["runner"]
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "actions-config-check", Namespace: Namespace(target.ApplicationID), Labels: labelsFor(target, "runner")}, Immutable: ptr(true), Data: map[string][]byte{"config": []byte("original-development-fixture")}}
			switch change {
			case "mutable":
				secret.Immutable = ptr(false)
			case "nil-immutable":
				secret.Immutable = nil
			case "empty":
				secret.Data["config"] = nil
			case "oversized":
				secret.Data["config"] = make([]byte, (128<<10)+1)
			case "wrong-service":
				secret.Labels[serviceKey] = "another-service"
			case "unowned":
				secret.Labels[ownerKey] = "another-application"
			}
			kube := fake.NewClientset(secret, workspaceNode("qualified", string(ActionsWorkspaceSharedOverlay2V1)), workspaceRuntimeClass())
			client := &Client{kube: kube}
			err := client.SaveActionsConfig(ctx, target, "runner", "config-check", "replacement-must-not-be-used")
			if (change == "none") != (err == nil) {
				t.Fatal("invalid original config was reused", err)
			}
			stored, readErr := kube.CoreV1().Secrets(secret.Namespace).Get(ctx, secret.Name, metav1.GetOptions{})
			if readErr != nil || !reflect.DeepEqual(stored, secret) {
				t.Fatal("original config contents changed", readErr)
			}
			err = client.StartActionsPod(ctx, target, "runner", "config-check", service)
			if (change == "none") != (err == nil) {
				t.Fatal("fresh runner config check was incorrect", err)
			}
			for _, action := range kube.Actions() {
				if change != "none" && action.GetResource().Resource == "pods" && action.GetVerb() == "create" {
					t.Fatal("fresh pod was created with invalid configuration")
				}
			}
		})
	}
}

func TestActionsWorkspaceCreateRacePreservesExistingVFSJob(t *testing.T) {
	ctx := context.Background()
	target := runnerTarget(t)
	service := target.Spec.Services["runner"]
	existing := actionsPod(target, "runner", "race", service)
	kube := fake.NewClientset(workspaceNode("qualified", string(ActionsWorkspaceSharedOverlay2V1)), workspaceRuntimeClass())
	client := &Client{kube: kube}
	if err := client.SaveActionsConfig(ctx, target, "runner", "race", "development-fixture"); err != nil {
		t.Fatal(err)
	}
	kube.PrependReactor("create", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		if err := kube.Tracker().Add(existing); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "pods"}, existing.Name)
	})
	if err := client.StartActionsPod(ctx, target, "runner", "race", service); err != nil {
		t.Fatal(err)
	}
	stored, err := kube.CoreV1().Pods(existing.Namespace).Get(ctx, existing.Name, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(stored, existing) {
		t.Fatal("create race changed the existing job", err)
	}
}

func TestActionsWorkspaceStartupGuard(t *testing.T) {
	if stdruntime.GOOS != "linux" {
		t.Skip("runner startup guard runs on Linux")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "python3", "-B", "test_actions_workspace_start.py").CombinedOutput()
	if err != nil {
		t.Fatalf("Actions workspace startup guard: %v\n%s", err, output)
	}
}
