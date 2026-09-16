package cluster

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPodMessageReportsRecentOutOfMemoryRestarts(t *testing.T) {
	for _, state := range []string{"terminated", "restarted", "backoff", "ready", "old", "other-exit"} {
		t.Run(state, func(t *testing.T) {
			target := testTarget(t)
			now := time.Now()
			stopped := &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(now.Add(-time.Minute))}
			status := corev1.ContainerStatus{Name: "api", RestartCount: 1, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now)}}, LastTerminationState: corev1.ContainerState{Terminated: stopped}}
			switch state {
			case "terminated":
				status.State = corev1.ContainerState{Terminated: stopped}
				status.LastTerminationState = corev1.ContainerState{}
			case "backoff":
				status.State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
			case "ready":
				status.Ready = true
			case "old":
				stopped.FinishedAt = metav1.NewTime(now.Add(-time.Hour))
			case "other-exit":
				stopped.Reason = "Error"
				stopped.ExitCode = 1
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-new", Namespace: Namespace(target.ApplicationID), CreationTimestamp: metav1.NewTime(now), Labels: map[string]string{managedBy: "hakopod", ownerKey: ownerID(target.ApplicationID), serviceKey: "api"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{status}, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}}}
			if status.Ready {
				pod.Status.Conditions[0].Status = corev1.ConditionTrue
			}
			old := pod.DeepCopy()
			old.Name = "api-old"
			old.CreationTimestamp = metav1.NewTime(now.Add(-time.Hour))
			old.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "api", Ready: true}}
			old.Status.Conditions[0].Status = corev1.ConditionTrue
			c := &Client{kube: fake.NewClientset(old, pod)}
			message := c.podMessage(context.Background(), target, "api")
			wantOOM := state == "terminated" || state == "restarted" || state == "backoff"
			if strings.Contains(message, "OOMKilled") != wantOOM {
				t.Fatalf("unexpected diagnostic: %q", message)
			}
			if wantOOM && !strings.Contains(message, "larger service size") {
				t.Fatalf("missing actionable advice: %q", message)
			}
			if state == "ready" && message != "" {
				t.Fatalf("healthy container blamed for historical failure: %q", message)
			}
		})
	}
}
