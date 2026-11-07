package cluster

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRecreateRolloutBudgetIncludesTerminationGrace(t *testing.T) {
	target := testTarget(t)
	svc := target.Spec.Services["api"]
	svc.TerminationGraceSeconds = 600
	target.Spec.Services["api"] = svc
	rolling := deployment(target, "api", svc, time.Minute)
	if *rolling.Spec.ProgressDeadlineSeconds != 60 || recreateGrace(target, svc) != 0 {
		t.Fatalf("rolling update counted the grace period: deadline %d", *rolling.Spec.ProgressDeadlineSeconds)
	}
	svc.UpdateStrategy = "recreate"
	recreate := deployment(target, "api", svc, time.Minute)
	if recreate.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || *recreate.Spec.ProgressDeadlineSeconds != 660 {
		t.Fatalf("recreate progress deadline %d, strategy %s", *recreate.Spec.ProgressDeadlineSeconds, recreate.Spec.Strategy.Type)
	}
	svc.UpdateStrategy = ""
	target.policy = &WorkloadPolicy{Recreate: true}
	if recreateGrace(target, svc) != 10*time.Minute {
		t.Fatal("installation-wide recreate did not count the grace period")
	}
}

func TestRecreateWaitReadyAddsGraceToItsBudget(t *testing.T) {
	target := testTarget(t)
	svc := target.Spec.Services["api"]
	svc.UpdateStrategy, svc.TerminationGraceSeconds = "recreate", 1
	target.Spec.Services["api"] = svc
	dep := deployment(target, "api", svc, time.Second)
	dep.Generation = 1
	ns := Namespace(target.ApplicationID)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: ns, Labels: dep.Spec.Template.Labels, CreationTimestamp: metav1.Now()}, Spec: dep.Spec.Template.Spec, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}}}
	c := &Client{kube: fake.NewClientset(dep, pod), options: Options{RolloutTimeout: time.Second}}
	// One second startup, half a second for scheduling, one second of grace.
	if err := c.waitReady(context.Background(), target, "api", 1); err == nil || !strings.Contains(err.Error(), "rollout stopped after 2.5s") {
		t.Fatalf("recreate budget: %v", err)
	}
}
