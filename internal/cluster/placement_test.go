package cluster

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func placementNode(name string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"kubernetes.io/arch": "amd64", "kubernetes.io/hostname": name}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}, Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")}}}
}
func TestNodePinUsesSchedulerAndRespectsPolicy(t *testing.T) {
	target := testTarget(t)
	svc := target.Spec.Services["web"]
	svc.NodeName = "worker"
	svc.Architecture = "amd64"
	d := deployment(target, "web", svc, time.Minute)
	pod := d.Spec.Template.Spec
	if pod.NodeName != "" || pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields[0].Values[0] != "worker" {
		t.Fatal("node pin bypasses scheduler", pod)
	}
	applyWorkloadPolicy(&WorkloadPolicy{NodeName: "worker", Pool: "free"}, &pod)
	if pod.NodeSelector["kubernetes.io/arch"] != "amd64" || pod.NodeSelector["hakopod.com/pool"] != "free" {
		t.Fatal("lost scheduling constraints", pod.NodeSelector)
	}
	target.Spec.Services["web"] = svc
	c := &Client{kube: fake.NewClientset(), options: Options{WorkloadPolicy: func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
		return WorkloadPolicy{NodeName: "allocated"}, nil
	}}}
	if err := c.validateWorkloadPolicy(context.Background(), target); err == nil {
		t.Fatal("tenant escaped allocated node")
	}
}
func TestNodePlacementCapacityAndReadiness(t *testing.T) {
	ctx := context.Background()
	target := testTarget(t)
	target.Spec.Services = map[string]spec.Service{}
	for _, name := range []string{"first", "second"} {
		target.Spec.Services[name] = spec.Service{Image: "nginx:alpine", Size: "large", Replicas: 1, NodeName: "worker"}
	}
	c := &Client{kube: fake.NewClientset(placementNode("worker"), placementNode("other"))}
	report, err := c.Preflight(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if report.Validate() == nil {
		t.Fatal("cluster capacity concealed pinned node overload", report)
	}
	svc := target.Spec.Services["first"]
	svc.Size = "small"
	target.Spec.Services = map[string]spec.Service{"first": svc}
	if err = c.validatePlacement(ctx, target); err != nil {
		t.Fatal(err)
	}
	node, _ := c.kube.CoreV1().Nodes().Get(ctx, "worker", metav1.GetOptions{})
	node.Spec.Unschedulable = true
	c.kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if err = c.validatePlacement(ctx, target); err == nil {
		t.Fatal("cordoned node accepted")
	}
	svc.NodeName = "missing"
	target.Spec.Services["first"] = svc
	if err = c.validatePlacement(ctx, target); err == nil {
		t.Fatal("unknown node accepted")
	}
}
func TestNodePlacementProtectsLocalVolume(t *testing.T) {
	target := testTarget(t)
	svc := target.Spec.Services["web"]
	svc.NodeName = "other"
	svc.Volume = &spec.Volume{SizeGiB: 1, MountPath: "/data"}
	target.Spec.Services = map[string]spec.Service{"web": svc}
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "web-data", Namespace: Namespace(target.ApplicationID), Labels: labelsFor(target, "web")}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "local"}}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "local"}, Spec: corev1.PersistentVolumeSpec{NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "kubernetes.io/hostname", Operator: corev1.NodeSelectorOpIn, Values: []string{"worker"}}}}}}}}}
	c := &Client{kube: fake.NewClientset(placementNode("worker"), placementNode("other"), claim, pv)}
	if err := c.validatePlacement(context.Background(), target); err == nil || !strings.Contains(err.Error(), "migrate or restore") {
		t.Fatal("stranded local volume", err)
	}
	svc.NodeName = "worker"
	target.Spec.Services["web"] = svc
	if err := c.validatePlacement(context.Background(), target); err != nil {
		t.Fatal(err)
	}
}
func TestPlacementDiscoveryDoesNotExposeOtherPoolNodes(t *testing.T) {
	a := placementNode("allocated")
	a.Labels["hakopod.com/pool"] = "free"
	c := &Client{kube: fake.NewClientset(a, placementNode("private")), options: Options{WorkloadPolicy: func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
		return WorkloadPolicy{NodeName: "allocated", Pool: "free"}, nil
	}}}
	nodes, err := c.PlacementNodes(context.Background(), Target{})
	if err != nil || len(nodes) != 1 || nodes[0].Name != "allocated" {
		t.Fatal(nodes, err)
	}
}
