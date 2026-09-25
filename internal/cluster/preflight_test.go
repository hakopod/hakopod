package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPreflightAccountsForOtherWorkloadsAndJobPeak(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", Labels: map[string]string{"kubernetes.io/arch": "arm64"}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}, Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")}}}
	c := &Client{kube: fake.NewClientset(node)}
	target := testTarget(t)
	svc := target.Spec.Services["worker"]
	svc.Job = &spec.Job{TimeoutSeconds: 30}
	target.Spec.Services["worker"] = svc
	report, err := c.Preflight(context.Background(), target)
	if err != nil || report.Validate() != nil {
		t.Fatal(report, err)
	}
	if report.CPURequestMillis != 360 || report.MemoryRequestBytes != 462<<20 {
		t.Fatal(report)
	}
	busy := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "busy", Namespace: "other"}, Spec: corev1.PodSpec{NodeName: "node", Containers: []corev1.Container{{Name: "busy", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("900m"), corev1.ResourceMemory: resource.MustParse("900Mi")}}}}}}
	c.kube.CoreV1().Pods("other").Create(context.Background(), busy, metav1.CreateOptions{})
	report, err = c.Preflight(context.Background(), target)
	if err != nil || report.Validate() == nil {
		t.Fatal("overcommitted application accepted", report, err)
	}
}

func TestSharedStorageRejectsLocalPath(t *testing.T) {
	c := &Client{kube: fake.NewClientset(&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "local"}, Provisioner: "rancher.io/local-path"})}
	target := testTarget(t)
	target.Spec.Volumes = map[string]spec.NamedVolume{"uploads": {StorageClass: "local", AccessMode: "ReadWriteMany", SizeGiB: 1}}
	s := target.Spec.Services["web"]
	s.Mounts = []spec.Mount{{Volume: "uploads", MountPath: "/uploads"}}
	target.Spec.Services["web"] = s
	if err := c.validateStorage(context.Background(), target); err == nil {
		t.Fatal("local path accepted for shared uploads")
	}
}

func TestExtraHTTPIngressUsesDeclaredTargetPort(t *testing.T) {
	target := testTarget(t)
	svc := target.Spec.Services["web"]
	svc.Ports = []spec.Port{{Name: "admin", Port: 9000, TargetPort: 9001, Protocol: "TCP"}}
	svc.HTTP = map[string]spec.HTTPEndpoint{"admin": {Port: 9000}}
	target.Spec.Services["web"] = svc
	c := &Client{kube: fake.NewClientset(), options: Options{AppDomain: "example.test"}}
	if err := c.applyIngress(context.Background(), target, "web", svc); err != nil {
		t.Fatal(err)
	}
	ingress, _ := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(context.Background(), "web", metav1.GetOptions{})
	if len(ingress.Spec.Rules) != 2 || ingress.Spec.Rules[1].HTTP.Paths[0].Backend.Service.Port.Number != 9000 {
		t.Fatal("endpoint ingress missing")
	}
	found := false
	for _, policy := range policies(target) {
		if policy.Labels[serviceKey] != "web" {
			continue
		}
		for _, rule := range policy.Spec.Ingress {
			for _, peer := range rule.From {
				if peer.NamespaceSelector != nil {
					for _, p := range rule.Ports {
						found = found || p.Port.IntVal == 9001
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("ingress policy did not allow mapped target port")
	}
}

func TestRunnerPoolCapacityReportsExactShortage(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}, Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("32Gi")}}}
	busy := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "busy", Namespace: "other"}, Spec: corev1.PodSpec{NodeName: "node", Containers: []corev1.Container{{Name: "busy", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3010m"), corev1.ResourceMemory: resource.MustParse("9Gi")}}}}}}
	c := &Client{kube: fake.NewClientset(node, busy)}
	target := testTarget(t)
	runner := spec.Service{Image: spec.ActionsRunnerImage, Size: "compute", Replicas: 5, Actions: &spec.Actions{Repository: "example/repo", Credential: "runner-token"}}
	target.Spec.Services = map[string]spec.Service{"runner": runner}
	report, err := c.Preflight(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if report.Validate() == nil || !strings.Contains(report.Validate().Error(), "CPU short by 5.01 cores") || !strings.Contains(report.Validate().Error(), "0.99 CPU cores") || strings.Contains(report.Validate().Error(), "memory short by") {
		t.Fatal(report)
	}
	runner.Replicas = 3
	runner.Resources = &spec.Resources{CPURequest: "250m", CPULimit: "2", MemoryRequest: "1Gi", MemoryLimit: "4Gi"}
	target.Spec.Services["runner"] = runner
	report, err = c.Preflight(context.Background(), target)
	if err != nil || report.Validate() != nil || report.CPURequestMillis != 750 || report.MemoryRequestBytes != 3<<30 {
		t.Fatal(report, err)
	}
}

func TestCapacityShortageReportsMemoryAndExhaustedCapacity(t *testing.T) {
	text := capacityShortage(500, 4<<30, 990, 3<<30)
	if strings.Contains(text, "CPU short by") || !strings.Contains(text, "memory short by 1Gi") {
		t.Fatal(text)
	}
	text = capacityShortage(500, 4<<30, -10, 0)
	if !strings.Contains(text, "CPU short by 0.5 cores; memory short by 4Gi") || !strings.Contains(text, "Available after other reservations: 0 CPU cores and 0 memory") {
		t.Fatal(text)
	}
}

func TestPodRequestsIncludesNativeSidecarAndInitPeak(t *testing.T) {
	container := func(name, cpu, memory string) corev1.Container {
		return corev1.Container{Name: name, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)}}}
	}
	sidecar := container("docker", "300m", "384Mi")
	sidecar.RestartPolicy = ptr(corev1.ContainerRestartPolicyAlways)
	pod := corev1.PodSpec{Containers: []corev1.Container{container("runner", "100m", "128Mi")}, InitContainers: []corev1.Container{sidecar}}
	cpu, memory := podRequests(pod)
	if cpu != 400 || memory != 512<<20 {
		t.Fatal(cpu, memory)
	}
	pod.InitContainers = append(pod.InitContainers, container("prepare", "500m", "1Gi"))
	cpu, memory = podRequests(pod)
	if cpu != 800 || memory != 1408<<20 {
		t.Fatal(cpu, memory)
	}
}
