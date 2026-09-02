package cluster

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestManagedBackupOwnershipFence(t *testing.T) {
	ctx := context.Background()
	id := strings.Repeat("a", 32)
	yes := true
	one := int32(1)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "hakopod-system", UID: "namespace-a", Labels: map[string]string{managedBy: "hakopod", installationLabel: id}}}
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: ns.Name, UID: "deployment-a", Labels: map[string]string{managedBy: "hakopod", installationLabel: id}}, Spec: appsv1.DeploymentSpec{Replicas: &one}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "postgres-a", Namespace: ns.Name, UID: "replicaset-a", Labels: map[string]string{installationLabel: id}, OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: d.Name, UID: d.UID, Controller: &yes}}}}
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "postgres-a-pod", Namespace: ns.Name, UID: "pod-a", Labels: map[string]string{"app": "hakopod-postgres", installationLabel: id}, OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: &yes}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "postgres", Image: managedBackupImage}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "postgres", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	kube := fake.NewSimpleClientset(ns, d, rs, p)
	client := &Client{kube: kube}
	name, uid, fence, err := client.ManagedBackupPod(ctx)
	if err != nil || name != p.Name || uid != p.UID || len(fence) != 64 {
		t.Fatal("valid installer database rejected", err)
	}
	for _, mutate := range []func(*corev1.Pod){func(p *corev1.Pod) { p.Spec.Containers[0].Image = "postgres:latest" }, func(p *corev1.Pod) { p.OwnerReferences[0].UID = "unrelated" }, func(p *corev1.Pod) { p.Status.ContainerStatuses[0].Ready = false }} {
		changed := p.DeepCopy()
		mutate(changed)
		if _, err = kube.CoreV1().Pods(ns.Name).Update(ctx, changed, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err = client.ManagedBackupPod(ctx); err == nil {
			t.Fatal("untrusted management pod accepted")
		}
	}
	replaced := p.DeepCopy()
	replaced.UID = "pod-b"
	if _, err = kube.CoreV1().Pods(ns.Name).Update(ctx, replaced, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, _, newFence, err := client.ManagedBackupPod(ctx)
	if err != nil || newFence == fence {
		t.Fatal("replacement did not change ownership fence", err)
	}
}
