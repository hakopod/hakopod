package cluster

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAppLogPodsMapsStateRevisionAndTermination(t *testing.T) {
	ns := "hp-0123456789abcdef0123456789abcdef"
	labels := map[string]string{managedBy: "hakopod", ownerKey: "0123456789abcdef0123456789abcdef", serviceKey: "web"}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: ns, Annotations: map[string]string{"hakopod.io/revision": "7"}}}
	now := metav1.Now()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1-a", Namespace: ns, UID: "u1", Labels: labels, DeletionTimestamp: &now,
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-1"}}},
		Spec: corev1.PodSpec{NodeName: "n1"},
		Status: corev1.PodStatus{Reason: "Evicted", ContainerStatuses: []corev1.ContainerStatus{{
			Name: "app", RestartCount: 2,
			State:                corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
		}}},
	}
	other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "kube-system", Labels: labels}}
	c := &Client{kube: fake.NewClientset(rs, pod, other)}
	got, err := c.AppLogPods(context.Background(), 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("pods %v err %v", got, err)
	}
	p := got[0]
	if p.Revision != 7 || !p.Terminating || p.Service != "web" || p.Node != "n1" || p.Reason != "Evicted" || p.UID != "u1" {
		t.Fatalf("pod %+v", p)
	}
	k := p.Containers[0]
	if !k.Terminated || k.Reason != "OOMKilled" || k.ExitCode != 137 || k.Restart != 2 || k.PreviousRestart != 1 || k.PreviousReason != "Error" || k.PreviousExitCode != 1 {
		t.Fatalf("container %+v", k)
	}
}

func TestAppLogMaskerMasksSecretEnvAndFailsWithoutSecret(t *testing.T) {
	ns := "hp-0123456789abcdef0123456789abcdef"
	labels := map[string]string{managedBy: "hakopod", ownerKey: "0123456789abcdef0123456789abcdef"}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: ns, Labels: labels},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "s"}}}}}}}}
	c := &Client{kube: fake.NewClientset(pod)}
	if _, err := c.AppLogMasker(context.Background(), ns, "p"); err == nil {
		t.Fatal("masker must fail when the referenced secret is unreadable")
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: ns, Labels: labels}, Data: map[string][]byte{"K": []byte("hunter2-secret-value")}}
	c = &Client{kube: fake.NewClientset(pod, secret)}
	mask, err := c.AppLogMasker(context.Background(), ns, "p")
	if err != nil {
		t.Fatal(err)
	}
	if out := mask.Line("token hunter2-secret-value end"); out == "token hunter2-secret-value end" {
		t.Fatalf("secret not masked: %q", out)
	}
}

func TestPodLogSinceReturnsOutput(t *testing.T) {
	c := &Client{kube: fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "hp-x"}})}
	out, err := c.PodLogSince(context.Background(), "hp-x", "p", "app", false, time.Now(), 1024)
	if err != nil || len(out) == 0 {
		t.Fatalf("out %q err %v", out, err)
	}
}
