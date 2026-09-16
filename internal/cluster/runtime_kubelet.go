package cluster

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type kubeletSummary struct {
	Node struct {
		Name string `json:"nodeName"`
	} `json:"node"`
	Pods []struct {
		Ref        struct{ Name, Namespace, UID string } `json:"podRef"`
		Containers []struct {
			Name string `json:"name"`
			CPU  struct {
				Time      time.Time `json:"time"`
				NanoCores *uint64   `json:"usageNanoCores"`
			} `json:"cpu"`
			Memory struct {
				Time       time.Time `json:"time"`
				WorkingSet *uint64   `json:"workingSetBytes"`
			} `json:"memory"`
		} `json:"containers"`
	} `json:"pods"`
}

// Read through the authenticated Kubernetes API. The kubelet and node credentials
// stay private; only exact owned pod UIDs and container samples leave this function.
func (c *Client) kubeletRuntimeMetrics(ctx context.Context, pods []corev1.Pod) []podMetrics {
	nodes := map[string]bool{}
	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil && pod.Spec.NodeName != "" {
			nodes[pod.Spec.NodeName] = true
		}
	}
	if len(nodes) > 4 {
		return nil
	}
	ordered := make([]string, 0, len(nodes))
	for node := range nodes {
		ordered = append(ordered, node)
	}
	sort.Strings(ordered)
	samples := []podMetrics{}
	for _, node := range ordered {
		stream, err := c.restClient().Get().AbsPath("/api/v1/nodes/" + node + "/proxy/stats/summary").Stream(ctx)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(stream, (2<<20)+1))
		stream.Close()
		if err != nil || len(data) > 2<<20 {
			continue
		}
		samples = append(samples, summaryPodMetrics(data, node, pods)...)
	}
	return samples
}

func summaryPodMetrics(data []byte, node string, pods []corev1.Pod) []podMetrics {
	var summary kubeletSummary
	if json.Unmarshal(data, &summary) != nil || summary.Node.Name != node || len(summary.Pods) > 512 {
		return nil
	}
	owned := map[string]corev1.Pod{}
	for _, pod := range pods {
		if pod.Spec.NodeName == node && pod.UID != "" {
			owned[string(pod.UID)] = pod
		}
	}
	samples := []podMetrics{}
	for _, sample := range summary.Pods {
		pod, ok := owned[sample.Ref.UID]
		if !ok || pod.Name != sample.Ref.Name || pod.Namespace != sample.Ref.Namespace || len(sample.Containers) > 64 {
			continue
		}
		metric := podMetrics{Metadata: pod.ObjectMeta}
		expected := map[string]bool{}
		for _, container := range pod.Spec.Containers {
			expected[container.Name] = true
		}
		seen := map[string]bool{}
		for _, container := range sample.Containers {
			if !expected[container.Name] || seen[container.Name] {
				continue
			}
			seen[container.Name] = true
			if container.CPU.NanoCores == nil || container.Memory.WorkingSet == nil || *container.CPU.NanoCores > math.MaxInt64 || *container.Memory.WorkingSet > math.MaxInt64 {
				continue
			}
			stamp := container.CPU.Time
			if container.Memory.Time.Before(stamp) {
				stamp = container.Memory.Time
			}
			if !freshSample(container.CPU.Time) || !freshSample(container.Memory.Time) {
				continue
			}
			if metric.Timestamp.IsZero() || stamp.Before(metric.Timestamp.Time) {
				metric.Timestamp = metav1.NewTime(stamp)
			}
			metric.Containers = append(metric.Containers, struct {
				Name  string              `json:"name"`
				Usage corev1.ResourceList `json:"usage"`
			}{container.Name, corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewScaledQuantity(int64(*container.CPU.NanoCores), resource.Nano),
				corev1.ResourceMemory: *resource.NewQuantity(int64(*container.Memory.WorkingSet), resource.BinarySI),
			}})
		}
		samples = append(samples, metric)
	}
	return samples
}
func freshSample(stamp time.Time) bool {
	return !stamp.IsZero() && time.Since(stamp) <= 2*time.Minute && time.Until(stamp) <= 10*time.Second
}
