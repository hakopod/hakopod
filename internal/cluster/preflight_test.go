package cluster

import (
	"context"
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
	if report.CPURequestMillis != 300 || report.MemoryRequestBytes != 384<<20 {
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
