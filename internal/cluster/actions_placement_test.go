package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestActionsPlacementDiscoveryRespectsRuntimeAndAllocation(t *testing.T) {
	ctx := context.Background()
	ready := placementNode("actions-node")
	ready.Labels["hakopod.io/actions-runtime"] = "ready"
	ready.Labels["hakopod.com/pool"] = "private"
	missing := placementNode("ordinary-node")
	paused := placementNode("paused-node")
	paused.Spec.Unschedulable = true
	paused.Labels["hakopod.io/actions-runtime"] = "ready"
	c := &Client{kube: fake.NewClientset(ready, missing, paused)}
	nodes, err := c.PlacementNodesForRuntime(ctx, Target{}, "actions")
	if err != nil || len(nodes) != 3 {
		t.Fatal(nodes, err)
	}
	byName := map[string]PlacementNode{}
	for _, node := range nodes {
		byName[node.Name] = node
	}
	if !byName["actions-node"].Available || byName["ordinary-node"].Available || !strings.Contains(byName["ordinary-node"].Reason, "sandbox") || byName["paused-node"].Reason != "Scheduling paused" {
		t.Fatal("runtime placement eligibility is incorrect", byName)
	}
	ordinary, err := c.PlacementNodes(ctx, Target{})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range ordinary {
		if node.Name == "ordinary-node" && !node.Available {
			t.Fatal("Actions filtering changed ordinary placement")
		}
	}
	c.options.DeploymentMode = DeploymentManagedCloud
	nodes, err = c.PlacementNodesForRuntime(ctx, Target{}, "actions")
	if err != nil || len(nodes) != 0 {
		t.Fatal("Cloud node inventory leaked without allocation", nodes, err)
	}
	c.options.WorkloadPolicy = func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
		return WorkloadPolicy{NodeName: "actions-node", Pool: "private"}, nil
	}
	nodes, err = c.PlacementNodesForRuntime(ctx, Target{}, "actions")
	if err != nil || len(nodes) != 1 || nodes[0].Name != "actions-node" || !nodes[0].Available {
		t.Fatal("private allocation was not preserved", nodes, err)
	}
	if _, err := c.PlacementNodesForRuntime(ctx, Target{}, "arbitrary-runtime"); err == nil {
		t.Fatal("unknown runtime accepted")
	}
}

func TestActionsSelectedNodeRequiresSandboxAndExactName(t *testing.T) {
	ctx := context.Background()
	target := runnerTarget(t)
	svc := target.Spec.Services["runner"]
	svc.NodeName = "chosen"
	svc.Architecture = "amd64"
	target.Spec.Services["runner"] = svc
	chosen := placementNode("chosen")
	// Node names and hostname labels are not guaranteed to be identical.
	chosen.Labels["kubernetes.io/hostname"] = "different-hostname"
	other := placementNode("other")
	other.Labels["kubernetes.io/hostname"] = "chosen"
	other.Labels["hakopod.io/actions-runtime"] = "ready"
	runtime := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: ActionsRuntime, Labels: map[string]string{managedBy: "hakopod"}}, Handler: ActionsRuntime,
		Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{"hakopod.io/actions-runtime": "ready"}}}
	c := &Client{kube: fake.NewClientset(chosen, other, runtime)}
	if err := c.validatePlacement(ctx, target); err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatal("node without the sandbox was accepted", err)
	}
	chosen.Labels["hakopod.io/actions-runtime"] = "ready"
	if _, err := c.kube.CoreV1().Nodes().Update(ctx, chosen, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.validatePlacement(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveActionsConfig(ctx, target, "runner", "selected", "unused-development-fixture"); err != nil {
		t.Fatal(err)
	}
	if err := c.StartActionsPod(ctx, target, "runner", "selected", svc); err != nil {
		t.Fatal(err)
	}
	pod, err := c.kube.CoreV1().Pods(Namespace(target.ApplicationID)).Get(ctx, "actions-selected", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fields := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields
	if pod.Spec.NodeName != "" || pod.Spec.NodeSelector["kubernetes.io/hostname"] != "" || pod.Spec.NodeSelector["kubernetes.io/arch"] != "amd64" || len(fields) != 1 || fields[0].Key != "metadata.name" || fields[0].Values[0] != "chosen" {
		t.Fatal("selected node did not retain exact scheduler constraints", pod.Spec)
	}
	if err := c.kube.CoreV1().Nodes().Delete(ctx, "chosen", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.StartActionsPod(ctx, target, "runner", "missing", svc); err == nil {
		t.Fatal("missing selected node fell back to another node's hostname label")
	}
	if _, err := c.kube.CoreV1().Pods(Namespace(target.ApplicationID)).Get(ctx, "actions-missing", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("fallback created a pod", err)
	}
	c.options.WorkloadPolicy = func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
		return WorkloadPolicy{NodeName: "other"}, nil
	}
	if err := c.StartActionsPod(ctx, target, "runner", "outside-allocation", svc); err == nil || !strings.Contains(err.Error(), "allocated") {
		t.Fatal("selected node escaped the trusted allocation", err)
	}
}

func TestActionsPlacementPreservesSchedulingTaints(t *testing.T) {
	node := placementNode("allocated")
	node.Labels["hakopod.io/actions-runtime"] = "ready"
	node.Spec.Taints = []corev1.Taint{{Key: "hakopod.com/pool", Value: "private", Effect: corev1.TaintEffectNoSchedule}}
	if actionsNodeUnavailable(*node, nil) == "" {
		t.Fatal("unallocated node taint was ignored")
	}
	if reason := actionsNodeUnavailable(*node, &WorkloadPolicy{NodeName: "allocated", Pool: "private"}); reason != "" {
		t.Fatal("trusted pool toleration was discarded", reason)
	}
}

func TestActionsAllocatedPlacementDoesNotScanOtherNodes(t *testing.T) {
	ctx := context.Background()
	node := placementNode("allocated")
	node.Labels["kubernetes.io/hostname"] = "independent-hostname"
	node.Labels["hakopod.io/actions-runtime"] = "ready"
	node.Labels["hakopod.com/pool"] = "private"
	node.Spec.Taints = []corev1.Taint{{Key: "hakopod.com/pool", Value: "private", Effect: corev1.TaintEffectNoSchedule}}
	runtimeClass := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: ActionsRuntime, Labels: map[string]string{managedBy: "hakopod"}}, Handler: ActionsRuntime,
		Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{"hakopod.io/actions-runtime": "ready"}}}
	kube := fake.NewClientset(node, runtimeClass)
	kube.PrependReactor("list", "nodes", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("unrelated cluster inventory must not be scanned")
	})
	unallocated := &Client{kube: kube, options: Options{DeploymentMode: DeploymentManagedCloud}}
	if err := unallocated.ActionsPoolAvailable(ctx, Target{}, spec.Service{}); err == nil || !strings.Contains(err.Error(), "not allocated") {
		t.Fatal("unallocated Cloud scope inherited another environment's sandbox", err)
	}
	c := &Client{kube: kube, options: Options{WorkloadPolicy: func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
		return WorkloadPolicy{NodeName: "allocated", Pool: "private"}, nil
	}}}
	target := runnerTarget(t)
	svc := target.Spec.Services["runner"]
	svc.Architecture = "amd64"
	target.Spec.Services["runner"] = svc
	nodes, err := c.PlacementNodesForRuntime(ctx, target, "actions")
	if err != nil || len(nodes) != 1 || nodes[0].Name != "allocated" || !nodes[0].Available {
		t.Fatal("allocated discovery depends on unrelated nodes", nodes, err)
	}
	if err := c.validateDeliveryPolicy(ctx, target); err != nil {
		t.Fatal("allocated admission depends on unrelated nodes", err)
	}
	if err := c.SaveActionsConfig(ctx, target, "runner", "allocated", "unused-development-fixture"); err != nil {
		t.Fatal(err)
	}
	if err := c.StartActionsPod(ctx, target, "runner", "allocated", svc); err != nil {
		t.Fatal("trusted node with a different hostname was rejected", err)
	}
	pod, err := kube.CoreV1().Pods(Namespace(target.ApplicationID)).Get(ctx, "actions-allocated", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fields := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields
	if pod.Spec.NodeName != "" || pod.Spec.NodeSelector["kubernetes.io/hostname"] != "" || pod.Spec.NodeSelector["hakopod.com/pool"] != "private" || pod.Spec.NodeSelector["kubernetes.io/arch"] != "amd64" || fields[0].Values[0] != "allocated" || len(pod.Spec.Tolerations) != 1 {
		t.Fatal("trusted exact-node scheduling constraints were lost", pod.Spec)
	}
	mismatch := svc
	mismatch.Architecture = "arm64"
	if err := c.ActionsPoolAvailable(ctx, target, mismatch); err == nil {
		t.Fatal("architecture mismatch passed the pre-registration check")
	}
	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{Key: "maintenance", Effect: corev1.TaintEffectNoSchedule})
	if _, err := kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.ActionsPoolAvailable(ctx, target, svc); err == nil {
		t.Fatal("new scheduling taint passed the pre-registration check")
	}
	if err := c.StartActionsPod(ctx, target, "runner", "newly-tainted", svc); err == nil || !strings.Contains(err.Error(), "No ready Managed Actions node matches") {
		t.Fatal("runner start ignored a new scheduling taint", err)
	}
	if _, err := kube.CoreV1().Pods(Namespace(target.ApplicationID)).Get(ctx, "actions-newly-tainted", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("tainted placement created a pod", err)
	}
}
