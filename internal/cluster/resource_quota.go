package cluster

import (
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Explicit budgets must fit the namespace even when they exceed a stock profile.
// Keep enough room for the old revision during a rollout or service removal.
// Trusted hosted-pool quotas are applied afterwards and remain authoritative.
func explicitResourceQuota(q *corev1.ResourceQuota, t Target) {
	explicit := false
	budgets := map[string]corev1.ResourceList{}
	apps := []spec.Application{t.Spec}
	if t.Previous != nil {
		apps = append(apps, *t.Previous)
	}
	for _, app := range apps {
		for name, s := range app.Services {
			explicit = explicit || s.Resources != nil
			p := spec.EffectiveResources(s)
			count := max(s.Replicas, 1)
			if s.Autoscaling != nil {
				count = max(count, s.Autoscaling.MaxReplicas)
			}
			if s.Job != nil {
				count = 1
			} else {
				count++
			}
			values := map[corev1.ResourceName]string{
				corev1.ResourceRequestsCPU: p.CPURequest, corev1.ResourceLimitsCPU: p.CPULimit,
				corev1.ResourceRequestsMemory: p.MemoryRequest, corev1.ResourceLimitsMemory: p.MemoryLimit,
			}
			if budgets[name] == nil {
				budgets[name] = corev1.ResourceList{}
			}
			for key, value := range values {
				amount := resource.MustParse(value)
				amount.Mul(int64(count))
				if old, ok := budgets[name][key]; !ok || amount.Cmp(old) > 0 {
					budgets[name][key] = amount
				}
			}
		}
	}
	if !explicit {
		return
	}
	total := corev1.ResourceList{}
	for _, budget := range budgets {
		for key, value := range budget {
			sum := total[key]
			sum.Add(value)
			total[key] = sum
		}
	}
	for key, value := range total {
		if value.Cmp(q.Spec.Hard[key]) > 0 {
			q.Spec.Hard[key] = value
		}
	}
}
