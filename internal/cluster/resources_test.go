package cluster

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestExplicitPodResourcesAndCapacity(t *testing.T) {
	target := testTarget(t)
	web := target.Spec.Services["web"]
	web.Resources = &spec.Resources{CPURequest: "375m", CPULimit: "1500m", MemoryRequest: "384Mi", MemoryLimit: "768Mi"}
	web.Replicas = 2
	target.Spec.Services = map[string]spec.Service{"web": web}
	d := deployment(target, "web", web, time.Minute)
	want := d.Spec.Template.Spec.Containers[0].Resources
	if want.Requests.Cpu().MilliValue() != 375 || want.Limits.Cpu().MilliValue() != 1500 || want.Requests.Memory().Value() != 384<<20 || want.Limits.Memory().Value() != 768<<20 {
		t.Fatal(want)
	}
	report, err := (&Client{}).Preflight(context.Background(), target)
	if err != nil || report.CPURequestMillis != 750 || report.MemoryRequestBytes != 768<<20 {
		t.Fatal(report, err)
	}
	web.Autoscaling = &spec.Autoscaling{MinReplicas: 2, MaxReplicas: 5, TargetCPU: 70}
	target.Spec.Services["web"] = web
	report, err = (&Client{}).Preflight(context.Background(), target)
	if err != nil || report.CPURequestMillis != 1875 || report.MemoryRequestBytes != 1920<<20 {
		t.Fatal(report, err)
	}
	web.Autoscaling = nil
	web.Job = &spec.Job{TimeoutSeconds: 30}
	web.Replicas = 1
	target.Spec.Services["job"] = web
	report, err = (&Client{}).Preflight(context.Background(), target)
	if err != nil || report.CPURequestMillis != 2250 || report.MemoryRequestBytes != 2304<<20 {
		t.Fatal(report, err)
	}
}

func TestExplicitQuotaCoversRolloutAndRetainedRevision(t *testing.T) {
	target := testTarget(t)
	s := target.Spec.Services["web"]
	s.Resources = &spec.Resources{CPULimit: "32", MemoryLimit: "64Gi"}
	s.Replicas = 3
	target.Spec.Services = map[string]spec.Service{"web": s}
	quota := &corev1.ResourceQuota{Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{}}}
	serviceResourceQuota(quota, target)
	if q := quota.Spec.Hard[corev1.ResourceLimitsCPU]; q.Cmp(resource.MustParse("128")) != 0 {
		t.Fatal(q)
	}
	old := target.Spec
	target.Previous = &old
	target.Spec = spec.Application{Services: map[string]spec.Service{"replacement": {Size: "medium", Replicas: 1}}}
	serviceResourceQuota(quota, target)
	if q := quota.Spec.Hard[corev1.ResourceLimitsCPU]; q.Cmp(resource.MustParse("130400m")) != 0 {
		t.Fatal("old service budget lost", q)
	}
	if q := quota.Spec.Hard[corev1.ResourceLimitsMemory]; q.Cmp(resource.MustParse("263374Mi")) != 0 {
		t.Fatal(q)
	}
}

func TestCloudExplicitResourceCeilings(t *testing.T) {
	c := &Client{options: Options{DeploymentMode: DeploymentManagedCloud}}
	for _, field := range []string{"cpu_request", "cpu_limit", "memory_request", "memory_limit"} {
		target := testTarget(t)
		r := &spec.Resources{}
		switch field {
		case "cpu_request":
			r.CPURequest = "601m"
			r.CPULimit = "1"
		case "cpu_limit":
			r.CPULimit = "2401m"
		case "memory_request":
			r.MemoryRequest = "616Mi"
			r.MemoryLimit = "1Gi"
		case "memory_limit":
			r.MemoryLimit = "1230Mi"
		}
		s := target.Spec.Services["web"]
		s.Resources = r
		target.Spec.Services["web"] = s
		if err := c.ValidateCloudSpec(target.Spec); err == nil || !strings.Contains(err.Error(), field) {
			t.Fatalf("%s bypass: %v", field, err)
		}
		if err := (&Client{}).ValidateCloudSpec(target.Spec); err != nil {
			t.Fatal("selfhosted restricted", err)
		}
	}
}

func TestProfileQuotaCoversIncreasedBudgetsWithoutOverrides(t *testing.T) {
	target := testTarget(t)
	target.Spec.Services = map[string]spec.Service{"api": {Size: "compute", Replicas: 4}}
	quota := &corev1.ResourceQuota{Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
		corev1.ResourceLimitsCPU: resource.MustParse("16"), corev1.ResourceLimitsMemory: resource.MustParse("16Gi"),
	}}}
	serviceResourceQuota(quota, target)
	// Four desired pods and the rolling replacement must fit the effective profile.
	if q := quota.Spec.Hard[corev1.ResourceLimitsCPU]; q.Cmp(resource.MustParse("24")) != 0 {
		t.Fatal(q)
	}
	if q := quota.Spec.Hard[corev1.ResourceLimitsMemory]; q.Cmp(resource.MustParse("24580Mi")) != 0 {
		t.Fatal(q)
	}
}
