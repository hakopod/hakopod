package cluster

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPlatformTLSExecCommandBindsAllowedContainerAndPort(t *testing.T) {
	for _, target := range []struct {
		component, container string
		port                 int32
		busybox              bool
	}{
		{"database", "database", 5432, false},
		{"api-gateway", "api-gateway", 8443, false},
		{"broker", "broker", 50051, false},
		{"controller-database", "controller-database", 5432, false},
		{"storage-controller", "storage-controller", 6699, false},
		{"proxy", "proxy", 5432, false},
		{"pageserver-0", "pageserver", 9898, false},
		{"safekeeper-2", "safekeeper", 7676, false},
		{"compute-0", "compute-tls", 3081, true},
		{"compute-1", "compute", 55433, false},
	} {
		t.Run(target.component+"/"+target.container, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "accepted", Labels: map[string]string{"app.kubernetes.io/component": target.component}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: target.container, Image: "example.test/runtime@sha256:" + strings.Repeat("a", 64), Ports: []corev1.ContainerPort{{ContainerPort: target.port}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
			container, command, err := platformTLSExecCommand(pod, int(target.port))
			if err != nil || container != target.container || len(command) != 3 || target.busybox && (command[0] != "/bin/sh" || !strings.HasPrefix(command[2], "exec nc -w 12 127.0.0.1 ")) || !target.busybox && (command[0] != "/bin/bash" || !strings.Contains(command[2], "/dev/tcp/127.0.0.1/")) {
				t.Fatalf("unexpected fixed byte-stream command: %v", err)
			}
			for name, mutate := range map[string]func(*corev1.Pod){
				"foreign container": func(p *corev1.Pod) { p.Spec.Containers[0].Name = "foreign" },
				"mutable image":     func(p *corev1.Pod) { p.Spec.Containers[0].Image = "example.test/runtime:latest" },
				"UDP":               func(p *corev1.Pod) { p.Spec.Containers[0].Ports[0].Protocol = corev1.ProtocolUDP },
				"duplicate port":    func(p *corev1.Pod) { p.Spec.Containers = append(p.Spec.Containers, p.Spec.Containers[0]) },
				"other port":        func(p *corev1.Pod) { p.Spec.Containers[0].Ports[0].ContainerPort++ },
				"deleting":          func(p *corev1.Pod) { value := metav1.Now(); p.DeletionTimestamp = &value },
				"not running":       func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending },
			} {
				changed := pod.DeepCopy()
				mutate(changed)
				if _, _, err := platformTLSExecCommand(changed, int(target.port)); err == nil {
					t.Fatalf("accepted %s", name)
				}
			}
			if _, _, err := platformTLSExecCommand(pod, 1); err == nil {
				t.Fatal("accepted an unqualified port")
			}
		})
	}
}
