package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RuntimeMetrics is a current Kubernetes observation, not a retained time
// series. Missing, stale, or partial samples never become a fabricated zero.
type RuntimeMetrics struct {
	Available     bool       `json:"available"`
	Reason        string     `json:"reason,omitempty"`
	CPU           *float64   `json:"cpu_millicores,omitempty"`
	Memory        *int64     `json:"memory_bytes,omitempty"`
	SampledAt     *time.Time `json:"sampled_at,omitempty"`
	WindowSeconds float64    `json:"window_seconds,omitempty"`
	PodsSampled   int        `json:"pods_sampled"`
	PodsExpected  int        `json:"pods_expected"`
}
type RuntimeCondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason,omitempty"`
	Message            string    `json:"message,omitempty"`
	LastTransitionTime time.Time `json:"last_transition_time"`
}
type RuntimeAllocation struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}
type RuntimeResources struct {
	Requests RuntimeAllocation `json:"requests"`
	Limits   RuntimeAllocation `json:"limits"`
}
type RuntimeContainer struct {
	Name      string           `json:"name"`
	Ready     bool             `json:"ready"`
	Restarts  int32            `json:"restarts"`
	State     string           `json:"state"`
	Reason    string           `json:"reason,omitempty"`
	Message   string           `json:"message,omitempty"`
	Image     string           `json:"image"`
	ImageID   string           `json:"image_id,omitempty"`
	Resources RuntimeResources `json:"resources"`
	CPU       *float64         `json:"cpu_millicores,omitempty"`
	Memory    *int64           `json:"memory_bytes,omitempty"`
}
type RuntimeEvent struct {
	Type     string    `json:"type"`
	Reason   string    `json:"reason"`
	Message  string    `json:"message"`
	Count    int32     `json:"count"`
	LastSeen time.Time `json:"last_seen"`
}
type RuntimePod struct {
	Name       string             `json:"name"`
	Phase      string             `json:"phase"`
	Ready      bool               `json:"ready"`
	NodeName   string             `json:"node_name"`
	PodIP      string             `json:"pod_ip"`
	CreatedAt  time.Time          `json:"created_at"`
	Conditions []RuntimeCondition `json:"conditions"`
	Containers []RuntimeContainer `json:"containers"`
	Events     []RuntimeEvent     `json:"events"`
}
type ServiceRuntime struct {
	ApplicationID string         `json:"application_id"`
	Service       string         `json:"service"`
	ObservedAt    time.Time      `json:"observed_at"`
	Metrics       RuntimeMetrics `json:"metrics"`
	Pods          []RuntimePod   `json:"pods"`
	Truncated     bool           `json:"truncated"`
	podsTruncated bool
}
type podMetrics struct {
	Metadata   metav1.ObjectMeta `json:"metadata"`
	Timestamp  metav1.Time       `json:"timestamp"`
	Window     metav1.Duration   `json:"window"`
	Containers []struct {
		Name  string              `json:"name"`
		Usage corev1.ResourceList `json:"usage"`
	} `json:"containers"`
}

func (c *Client) ServiceRuntime(ctx context.Context, t Target, service string) (ServiceRuntime, error) {
	result := ServiceRuntime{ApplicationID: t.ApplicationID, Service: service, ObservedAt: time.Now().UTC(), Pods: []RuntimePod{}, Metrics: RuntimeMetrics{Reason: "Kubernetes has no current complete resource sample"}}
	if _, ok := t.Spec.Services[service]; !ok {
		return result, fmt.Errorf("service does not exist")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	namespace := Namespace(t.ApplicationID)
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return result, err
	}
	if err = owned(ns, t); err != nil {
		return result, err
	}
	selector := managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + serviceKey + "=" + service
	pods, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 64})
	if err != nil {
		return result, err
	}
	result.Truncated = pods.Continue != "" || len(pods.Items) > 64
	result.podsTruncated = result.Truncated
	if len(pods.Items) > 64 {
		pods.Items = pods.Items[:64]
	}
	sort.Slice(pods.Items, func(i, j int) bool { return pods.Items[i].Name < pods.Items[j].Name })
	index := map[string]int{}
	for _, pod := range pods.Items {
		if err = owned(&pod, t); err != nil {
			return result, err
		}
		item := RuntimePod{Name: pod.Name, Phase: string(pod.Status.Phase), Ready: podReady(pod), NodeName: pod.Spec.NodeName, PodIP: pod.Status.PodIP, CreatedAt: pod.CreationTimestamp.Time, Conditions: []RuntimeCondition{}, Containers: []RuntimeContainer{}, Events: []RuntimeEvent{}}
		for _, condition := range pod.Status.Conditions {
			if len(item.Conditions) == 16 {
				break
			}
			item.Conditions = append(item.Conditions, RuntimeCondition{Type: string(condition.Type), Status: string(condition.Status), Reason: condition.Reason, Message: boundedMessage(condition.Message), LastTransitionTime: condition.LastTransitionTime.Time})
		}
		for _, container := range pod.Spec.Containers {
			if len(item.Containers) == 16 {
				result.Truncated = true
				break
			}
			x := RuntimeContainer{Name: container.Name, Image: container.Image, State: "unknown", Resources: RuntimeResources{Requests: runtimeAllocation(container.Resources.Requests), Limits: runtimeAllocation(container.Resources.Limits)}}
			for _, status := range pod.Status.ContainerStatuses {
				if status.Name != container.Name {
					continue
				}
				x.Ready = status.Ready
				x.Restarts = status.RestartCount
				x.ImageID = status.ImageID
				switch {
				case status.State.Running != nil:
					x.State = "running"
				case status.State.Waiting != nil:
					x.State = "waiting"
					x.Reason = status.State.Waiting.Reason
					x.Message = boundedMessage(status.State.Waiting.Message)
				case status.State.Terminated != nil:
					x.State = "terminated"
					x.Reason = status.State.Terminated.Reason
					x.Message = boundedMessage(status.State.Terminated.Message)
				}
			}
			item.Containers = append(item.Containers, x)
		}
		index[string(pod.UID)] = len(result.Pods)
		result.Pods = append(result.Pods, item)
		if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil {
			result.Metrics.PodsExpected++
		}
	}
	// Events are bounded to the one owned namespace, filtered by exact pod UID.
	events, eventErr := c.kube.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.kind=Pod", Limit: 200})
	if eventErr == nil {
		result.Truncated = result.Truncated || events.Continue != ""
		sort.Slice(events.Items, func(i, j int) bool { return eventTime(events.Items[i]).After(eventTime(events.Items[j])) })
		for _, event := range events.Items {
			if i, ok := index[string(event.InvolvedObject.UID)]; ok && len(result.Pods[i].Events) < 8 {
				result.Pods[i].Events = append(result.Pods[i].Events, RuntimeEvent{Type: event.Type, Reason: event.Reason, Message: boundedMessage(event.Message), Count: event.Count, LastSeen: eventTime(event)})
			}
		}
	}
	if c.restClient() == nil {
		return result, nil
	}
	metricsCtx, metricsCancel := context.WithTimeout(ctx, 2*time.Second)
	stream, metricErr := c.restClient().Get().AbsPath("/apis/metrics.k8s.io/v1beta1/namespaces/"+namespace+"/pods").Param("labelSelector", selector).Param("limit", "64").Stream(metricsCtx)
	var data []byte
	if metricErr == nil {
		data, metricErr = io.ReadAll(io.LimitReader(stream, (512<<10)+1))
		stream.Close()
	}
	metricsCancel()
	var metrics struct {
		Items []podMetrics `json:"items"`
	}
	if metricErr == nil && len(data) <= 512<<10 && json.Unmarshal(data, &metrics) == nil && len(metrics.Items) <= 128 {
		applyRuntimeMetrics(&result, pods.Items, metrics.Items)
	}
	if !result.Metrics.Available && result.Metrics.PodsExpected > 0 {
		result.Metrics = RuntimeMetrics{PodsExpected: result.Metrics.PodsExpected, Reason: "Kubernetes has no current complete resource sample"}
		for i := range result.Pods {
			for j := range result.Pods[i].Containers {
				result.Pods[i].Containers[j].CPU = nil
				result.Pods[i].Containers[j].Memory = nil
			}
		}
		applyRuntimeMetrics(&result, pods.Items, c.kubeletRuntimeMetrics(ctx, pods.Items))
	}

	return result, nil
}

func eventTime(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

func applyRuntimeMetrics(result *ServiceRuntime, pods []corev1.Pod, metrics []podMetrics) {
	byName := map[string]podMetrics{}
	for _, m := range metrics {
		byName[m.Metadata.Name] = m
	}
	var totalCPU float64
	var totalMemory int64
	var oldest time.Time
	window := float64(0)
	for i, pod := range pods {
		if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
			continue
		}
		m, ok := byName[pod.Name]
		if !ok || m.Timestamp.IsZero() || time.Since(m.Timestamp.Time) > 2*time.Minute || time.Until(m.Timestamp.Time) > 10*time.Second {
			continue
		}
		if !pod.CreationTimestamp.IsZero() && m.Timestamp.Time.Before(pod.CreationTimestamp.Time) {
			continue
		}
		usages := map[string]corev1.ResourceList{}
		for _, x := range m.Containers {
			usages[x.Name] = x.Usage
		}
		complete := len(result.Pods[i].Containers) > 0
		for j := range result.Pods[i].Containers {
			x := &result.Pods[i].Containers[j]
			usage, found := usages[x.Name]
			cpu, cpuOK := usage[corev1.ResourceCPU]
			memory, memoryOK := usage[corev1.ResourceMemory]
			if !found || !cpuOK || !memoryOK {
				complete = false
				continue
			}
			cpuValue := cpu.AsApproximateFloat64() * 1000
			memoryValue := memory.Value()
			if cpuValue < 0 || memoryValue < 0 {
				complete = false
				continue
			}
			x.CPU = &cpuValue
			x.Memory = &memoryValue
		}
		if !complete {
			continue
		}
		result.Metrics.PodsSampled++
		for _, x := range result.Pods[i].Containers {
			totalCPU += *x.CPU
			totalMemory += *x.Memory
		}
		if oldest.IsZero() || m.Timestamp.Time.Before(oldest) {
			oldest = m.Timestamp.Time
		}
		if m.Window.Duration.Seconds() > window {
			window = m.Window.Duration.Seconds()
		}
	}
	if result.Metrics.PodsExpected > 0 && result.Metrics.PodsSampled == result.Metrics.PodsExpected && !result.podsTruncated {
		result.Metrics.Available = true
		result.Metrics.Reason = ""
		result.Metrics.CPU = &totalCPU
		result.Metrics.Memory = &totalMemory
		result.Metrics.SampledAt = &oldest
		result.Metrics.WindowSeconds = window
	}
}

func runtimeAllocation(values corev1.ResourceList) RuntimeAllocation {
	var result RuntimeAllocation
	if value, ok := values[corev1.ResourceCPU]; ok {
		result.CPU = value.String()
	}
	if value, ok := values[corev1.ResourceMemory]; ok {
		result.Memory = value.String()
	}
	return result
}
