package cluster

import (
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestKubeletMetricsRequireExactFreshOwnedSamples(t *testing.T) {
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "owned", UID: "pod-uid"}, Spec: corev1.PodSpec{NodeName: "worker", Containers: []corev1.Container{{Name: "app"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	fixture := func() map[string]any {
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		return map[string]any{"node": map[string]any{"nodeName": "worker"}, "pods": []any{map[string]any{"podRef": map[string]any{"name": "api-1", "namespace": "owned", "uid": "pod-uid"}, "containers": []any{map[string]any{"name": "app", "cpu": map[string]any{"time": stamp, "usageNanoCores": 25000000}, "memory": map[string]any{"time": stamp, "workingSetBytes": 33554432}}}}}}
	}
	tests := []struct {
		name      string
		change    func(map[string]any)
		available bool
	}{
		{"complete", func(m map[string]any) {}, true},
		{"wrong node", func(m map[string]any) { m["node"].(map[string]any)["nodeName"] = "other" }, false},
		{"wrong UID", func(m map[string]any) { kubeletRef(m)["uid"] = "old-pod" }, false},
		{"wrong namespace", func(m map[string]any) { kubeletRef(m)["namespace"] = "other-tenant" }, false},
		{"wrong name", func(m map[string]any) { kubeletRef(m)["name"] = "other" }, false},
		{"missing CPU", func(m map[string]any) { delete(kubeletContainer(m)["cpu"].(map[string]any), "usageNanoCores") }, false},
		{"missing memory", func(m map[string]any) { delete(kubeletContainer(m)["memory"].(map[string]any), "workingSetBytes") }, false},
		{"unknown container", func(m map[string]any) { kubeletContainer(m)["name"] = "unrelated" }, false},
		{"stale", func(m map[string]any) {
			kubeletContainer(m)["cpu"].(map[string]any)["time"] = time.Now().Add(-3 * time.Minute)
		}, false},
		{"future", func(m map[string]any) {
			kubeletContainer(m)["memory"].(map[string]any)["time"] = time.Now().Add(time.Minute)
		}, false},
		{"missing timestamp", func(m map[string]any) { delete(kubeletContainer(m)["cpu"].(map[string]any), "time") }, false},
		{"overflow", func(m map[string]any) {
			kubeletContainer(m)["memory"].(map[string]any)["workingSetBytes"] = uint64(1) << 63
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := fixture()
			tt.change(data)
			raw, _ := json.Marshal(data)
			result := ServiceRuntime{Metrics: RuntimeMetrics{PodsExpected: 1}, Pods: []RuntimePod{{Containers: []RuntimeContainer{{Name: "app"}}}}}
			applyRuntimeMetrics(&result, []corev1.Pod{pod}, summaryPodMetrics(raw, "worker", []corev1.Pod{pod}))
			if result.Metrics.Available != tt.available {
				t.Fatalf("availability: %+v", result.Metrics)
			}
			if tt.available {
				if *result.Metrics.CPU != 25 || *result.Metrics.Memory != 32<<20 {
					t.Fatal("incorrect units")
				}
			} else if result.Metrics.CPU != nil || result.Metrics.Memory != nil {
				t.Fatal("invented usage")
			}
		})
	}
	// A second expected container without a sample must leave the aggregate unavailable.
	pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "sidecar"})
	raw, _ := json.Marshal(fixture())
	result := ServiceRuntime{Metrics: RuntimeMetrics{PodsExpected: 1}, Pods: []RuntimePod{{Containers: []RuntimeContainer{{Name: "app"}, {Name: "sidecar"}}}}}
	applyRuntimeMetrics(&result, []corev1.Pod{pod}, summaryPodMetrics(raw, "worker", []corev1.Pod{pod}))
	if result.Metrics.Available || result.Metrics.Memory != nil {
		t.Fatal("partial sample reported complete")
	}
}
func kubeletRef(m map[string]any) map[string]any {
	return m["pods"].([]any)[0].(map[string]any)["podRef"].(map[string]any)
}
func kubeletContainer(m map[string]any) map[string]any {
	return m["pods"].([]any)[0].(map[string]any)["containers"].([]any)[0].(map[string]any)
}
