package cluster

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
)

// Observe only members whose namespace and controller ownership were checked by
// ObserveDatabase. One bounded request serves every member; telemetry failures
// must never prevent reconciliation or become healthy-looking zero usage.
func (c *Client) observeDatabaseMetrics(ctx context.Context, d database.Resource, pods []corev1.Pod, observation *database.Observation) {
	applyDatabaseMetrics(observation, pods, nil, d.Spec.Members())
	if len(pods) == 0 || c.restClient() == nil {
		return
	}
	step, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	metricsCtx, stop := context.WithTimeout(step, 1500*time.Millisecond)
	stream, err := c.restClient().Get().AbsPath("/apis/metrics.k8s.io/v1beta1/namespaces/"+DatabaseNamespace(d.ID)+"/pods").Param("limit", "64").Stream(metricsCtx)
	var data []byte
	if err == nil {
		data, err = io.ReadAll(io.LimitReader(stream, (512<<10)+1))
		stream.Close()
	}
	stop()
	var response struct {
		Items    []podMetrics `json:"items"`
		Metadata struct {
			Continue string `json:"continue"`
		} `json:"metadata"`
	}
	if err == nil && len(data) <= 512<<10 && json.Unmarshal(data, &response) == nil && len(response.Items) <= 64 && response.Metadata.Continue == "" {
		applyDatabaseMetrics(observation, pods, response.Items, d.Spec.Members())
	}
	if !observation.Metrics.Available {
		applyDatabaseMetrics(observation, pods, c.kubeletRuntimeMetrics(step, pods), d.Spec.Members())
	}
}

func applyDatabaseMetrics(observation *database.Observation, pods []corev1.Pod, samples []podMetrics, expected int) {
	total := database.Metrics{PodsExpected: expected, Reason: "No complete current resource sample for every database member."}
	var cpu float64
	var memory int64
	for i := range observation.Members {
		member := &observation.Members[i]
		unavailable := database.Metrics{PodsExpected: 1, Reason: "No current resource sample for this member."}
		member.Metrics = &unavailable
		for _, pod := range pods {
			if pod.Name != member.Name || string(pod.UID) != member.UID {
				continue
			}
			result := ServiceRuntime{Metrics: RuntimeMetrics{PodsExpected: 1}, Pods: []RuntimePod{{}}}
			for _, container := range pod.Spec.Containers {
				result.Pods[0].Containers = append(result.Pods[0].Containers, RuntimeContainer{Name: container.Name})
			}
			matching := []podMetrics{}
			for _, sample := range samples {
				if sample.Metadata.Name == pod.Name && sample.Metadata.Namespace == pod.Namespace && (sample.Metadata.UID == "" || sample.Metadata.UID == pod.UID) {
					matching = append(matching, sample)
				}
			}
			// Duplicate identities are ambiguous; never pick an arbitrary sample.
			if len(matching) != 1 {
				continue
			}
			applyRuntimeMetrics(&result, []corev1.Pod{pod}, matching)
			if !result.Metrics.Available {
				continue
			}
			metrics := database.Metrics(result.Metrics)
			member.Metrics = &metrics
			cpu += *metrics.CPU
			memory += *metrics.Memory
			total.PodsSampled++
			if total.SampledAt == nil || metrics.SampledAt.Before(*total.SampledAt) {
				total.SampledAt = metrics.SampledAt
			}
			if metrics.WindowSeconds > total.WindowSeconds {
				total.WindowSeconds = metrics.WindowSeconds
			}
		}
	}
	if expected > 0 && total.PodsSampled == expected && len(observation.Members) == expected {
		total.Available = true
		total.Reason = ""
		total.CPU = &cpu
		total.Memory = &memory
	} else {
		total.SampledAt = nil
	}
	observation.Metrics = &total
}
