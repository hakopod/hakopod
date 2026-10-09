package cluster

import (
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestDatabaseFailureEvidenceRequiresExactOOMReason(t *testing.T) {
	now := time.Now().UTC()
	d := database.Resource{Revision: 3, Spec: database.Spec{Engine: "postgresql"}}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "database-1", UID: types.UID("member-uid")}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "postgres", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}}}}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "postgres", RestartCount: 2, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 137, FinishedAt: metav1.NewTime(now.Add(-time.Minute))}}}}}}
	observation := database.Observation{ObservedAt: now, Revision: d.Revision}
	if failures := databaseFailureEvidence(d, []corev1.Pod{pod}, observation); len(failures) != 0 {
		t.Fatal("exit code 137 was incorrectly reported as an out-of-memory failure", failures)
	}
	pod.Status.ContainerStatuses[0].LastTerminationState.Terminated.Reason = "OOMKilled"
	failures := databaseFailureEvidence(d, []corev1.Pod{pod}, observation)
	if len(failures) != 1 {
		t.Fatal("exact Kubernetes OOM evidence was not recorded", failures)
	}
	failure := failures[0]
	if failure.Code != "memory_limit_exceeded" || failure.MemoryLimitBytes == nil || *failure.MemoryLimitBytes != 256<<20 || failure.ExitCode == nil || *failure.ExitCode != 137 || failure.Summary != "PostgreSQL member database-1 exceeded its 256 MiB memory limit." {
		t.Fatalf("incomplete OOM evidence: %#v", failure)
	}
}

func TestDatabaseFailureEvidencePreservesUnknownMemoryLimit(t *testing.T) {
	now := time.Now().UTC()
	d := database.Resource{Revision: 1, Spec: database.Spec{Engine: "redis"}}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "redis-0", UID: types.UID("redis-uid")}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "redis", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(now)}}}}}}
	failure := databaseFailureEvidence(d, []corev1.Pod{pod}, database.Observation{ObservedAt: now, Revision: 1})[0]
	if failure.MemoryLimitBytes != nil || failure.Summary != "Redis member redis-0 was terminated because Kubernetes reported OOMKilled." {
		t.Fatalf("unknown memory limit was invented: %#v", failure)
	}
}
