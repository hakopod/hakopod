package cluster

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRuntimeMetricsNeverFabricatePartialOrStaleUsage(t *testing.T) {
	now := time.Now().UTC()
	pods := []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "one", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}, {ObjectMeta: metav1.ObjectMeta{Name: "two", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}}
	makeResult := func() ServiceRuntime {
		return ServiceRuntime{Metrics: RuntimeMetrics{PodsExpected: 2}, Pods: []RuntimePod{{Containers: []RuntimeContainer{{Name: "app"}}}, {Containers: []RuntimeContainer{{Name: "app"}}}}}
	}
	metric := podMetrics{Metadata: metav1.ObjectMeta{Name: "one"}, Timestamp: metav1.NewTime(now), Window: metav1.Duration{Duration: 15 * time.Second}}
	metric.Containers = append(metric.Containers, struct {
		Name  string              `json:"name"`
		Usage corev1.ResourceList `json:"usage"`
	}{"app", corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("25m"), corev1.ResourceMemory: resource.MustParse("32Mi")}})
	result := makeResult()
	applyRuntimeMetrics(&result, pods, []podMetrics{metric})
	if result.Metrics.Available || result.Metrics.CPU != nil || result.Metrics.Memory != nil || result.Metrics.PodsSampled != 1 {
		t.Fatalf("partial metrics incorrectly represented: %+v", result.Metrics)
	}
	second := metric
	second.Metadata.Name = "two"
	result = makeResult()
	applyRuntimeMetrics(&result, pods, []podMetrics{metric, second})
	if !result.Metrics.Available || *result.Metrics.CPU != 50 || *result.Metrics.Memory != 64<<20 {
		t.Fatalf("complete metrics wrong: %+v", result.Metrics)
	}
	metric.Timestamp = metav1.NewTime(now.Add(-3 * time.Minute))
	result = makeResult()
	applyRuntimeMetrics(&result, pods, []podMetrics{metric})
	if result.Metrics.PodsSampled != 0 || result.Pods[0].Containers[0].CPU != nil {
		t.Fatal("stale metrics exposed as current")
	}
}

func TestRuntimeLiveExistingApplication(t *testing.T) {
	kubeconfig, application := os.Getenv("HAKOPOD_TEST_KUBECONFIG"), os.Getenv("HAKOPOD_TEST_RUNTIME_APP")
	if kubeconfig == "" || application == "" {
		t.Skip("requires explicit isolated Hakopod kubeconfig and existing application ID")
	}
	client, err := New(kubeconfig, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := client.ServiceRuntime(ctx, Target{ApplicationID: application, Spec: spec.Application{Services: map[string]spec.Service{"api": {}}}}, "api")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pods) == 0 || !result.Metrics.Available || result.Metrics.Memory == nil || *result.Metrics.Memory <= 0 {
		t.Fatalf("actual metrics-server sample unavailable: %+v", result.Metrics)
	}
	for _, pod := range result.Pods {
		if pod.NodeName == "" || pod.PodIP == "" || len(pod.Conditions) == 0 || len(pod.Containers) == 0 {
			t.Fatalf("missing actual pod details: %+v", pod)
		}
		if pod.Containers[0].Resources.Requests.Memory == "" {
			t.Fatal("missing actual resource request")
		}
	}
	t.Logf("real service sample: pods=%d cpu_millicores=%.3f memory_bytes=%d", len(result.Pods), *result.Metrics.CPU, *result.Metrics.Memory)
}
