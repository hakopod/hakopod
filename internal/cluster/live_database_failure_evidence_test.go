package cluster

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// TestLiveDatabaseFailureEvidence observes a real cgroup OOM in the named
// development cluster. It never treats an exit code as proof of an OOM.
func TestLiveDatabaseFailureEvidence(t *testing.T) {
	if os.Getenv("HAKOPOD_TEST_NATIVE_OOM") != "1" {
		t.Skip("set HAKOPOD_TEST_NATIVE_OOM=1 to run the owned native OOM fixture")
	}
	kubeconfig := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	if kubeconfig == "" {
		t.Fatal("HAKOPOD_TEST_KUBECONFIG is required")
	}
	raw, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	if raw.CurrentContext != "k3d-hakopod-dev" {
		t.Fatalf("native OOM fixture requires k3d-hakopod-dev, got %q", raw.CurrentContext)
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	namespace := fmt.Sprintf("hakopod-fixture-oom-evidence-%d", time.Now().UnixNano())
	created := false
	t.Cleanup(func() {
		if !created {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 45*time.Second)
		defer stop()
		_ = client.CoreV1().Namespaces().Delete(cleanup, namespace, metav1.DeleteOptions{})
		for cleanup.Err() == nil {
			_, getErr := client.CoreV1().Namespaces().Get(cleanup, namespace, metav1.GetOptions{})
			if apierrors.IsNotFound(getErr) {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Errorf("owned namespace %s was not confirmed deleted: %v", namespace, cleanup.Err())
	})

	_, err = client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: namespace,
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": "hakopod-native-test",
			"hakopod.io/fixture":           "database-failure-evidence",
		},
	}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	created = true

	const memoryLimit = "32Mi"
	const image = "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a"
	pod, err := client.CoreV1().Pods(namespace).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "postgresql-oom-observation",
			Labels: map[string]string{
				"app.kubernetes.io/name": "hakopod-native-oom-fixture",
				"hakopod.io/fixture":     "database-failure-evidence",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "postgres",
				Image:   image,
				Command: []string{"python", "-B", "-c"},
				Args:    []string{"import time; blocks=[]\nwhile True:\n blocks.append(bytearray(8*1024*1024)); time.sleep(0.05)"},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi"), corev1.ResourceCPU: resource.MustParse("10m")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(memoryLimit), corev1.ResourceCPU: resource.MustParse("250m")},
				},
			}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var observed corev1.Pod
	for ctx.Err() == nil {
		current, getErr := client.CoreV1().Pods(namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if getErr != nil {
			t.Fatal(getErr)
		}
		if len(current.Status.ContainerStatuses) == 1 {
			terminated := current.Status.ContainerStatuses[0].State.Terminated
			if terminated != nil {
				if terminated.Reason != "OOMKilled" {
					t.Fatalf("container terminated without Kubernetes OOM evidence: reason=%q exit=%d", terminated.Reason, terminated.ExitCode)
				}
				observed = *current
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if observed.Name == "" {
		t.Fatalf("timed out waiting for exact Kubernetes OOMKilled evidence: %v", ctx.Err())
	}

	observedAt := time.Now().UTC()
	resource := database.Resource{Revision: 7, Spec: database.Spec{Engine: "postgresql"}}
	failures := databaseFailureEvidence(resource, []corev1.Pod{observed, observed}, database.Observation{ObservedAt: observedAt, Revision: resource.Revision})
	if len(failures) != 1 {
		t.Fatalf("real OOM evidence was not deduplicated: %#v", failures)
	}
	failure := failures[0]
	if failure.Reason != "OOMKilled" || failure.Container != "postgres" || failure.Member != observed.Name || failure.MemberUID != string(observed.UID) {
		t.Fatalf("collector changed Kubernetes identity evidence: %#v", failure)
	}
	if failure.OccurredAt.IsZero() || failure.OccurredAt.After(observedAt) || failure.ObservedAt != observedAt {
		t.Fatalf("collector did not preserve the observed termination timestamp: %#v", failure)
	}
	if failure.MemoryLimitBytes == nil || *failure.MemoryLimitBytes != 32<<20 {
		t.Fatalf("collector did not preserve the 32 MiB cgroup limit: %#v", failure)
	}
	if failure.Source != "kubernetes_container_status" || !strings.Contains(failure.Summary, "exceeded its 32 MiB memory limit") {
		t.Fatalf("collector did not explain the exact OOM evidence: %#v", failure)
	}
	if healthy := databaseFailureEvidence(resource, []corev1.Pod{{ObjectMeta: observed.ObjectMeta, Spec: observed.Spec}}, database.Observation{ObservedAt: observedAt.Add(time.Second), Revision: resource.Revision}); len(healthy) != 0 {
		t.Fatalf("healthy observation invented failure evidence: %#v", healthy)
	}

	t.Logf("observed exact OOMKilled evidence for pod %s container %s at %s with memory limit %s", failure.Member, failure.Container, failure.OccurredAt.Format(time.RFC3339Nano), memoryLimit)
}
