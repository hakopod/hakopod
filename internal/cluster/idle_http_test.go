package cluster

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
	"time"
)

func idleFixture(t *testing.T) (*Client, Target, *appsv1.Deployment) {
	target := testTarget(t)
	delete(target.Spec.Services, "api")
	delete(target.Spec.Services, "worker")
	d := deployment(target, "web", target.Spec.Services["web"], time.Minute)
	d.Status = appsv1.DeploymentStatus{Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, ObservedGeneration: d.Generation}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-pod", Namespace: d.Namespace, Labels: d.Spec.Template.Labels}, Spec: d.Spec.Template.Spec, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	c := &Client{kube: fake.NewClientset(d, pod), options: Options{AppDomain: "example.test", WorkloadPolicy: func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
		return WorkloadPolicy{NodeName: "test", IdleHTTP: true}, nil
	}}}
	return c, target, d
}
func TestIdleSleepWakeAndManualZero(t *testing.T) {
	c, target, d := idleFixture(t)
	ctx := context.Background()
	if err := c.SetHTTPIdle(ctx, target, "web", true); err != nil {
		t.Fatal(err)
	}
	current, _ := c.kube.AppsV1().Deployments(d.Namespace).Get(ctx, "web", metav1.GetOptions{})
	if !idleDeployment(current) || target.Spec.Services["web"].Replicas != 1 {
		t.Fatal("saved replicas changed or marker missing")
	}
	if err := c.SetHTTPIdle(ctx, target, "web", false); err != nil {
		t.Fatal(err)
	}
	current, _ = c.kube.AppsV1().Deployments(d.Namespace).Get(ctx, "web", metav1.GetOptions{})
	if *current.Spec.Replicas != 1 {
		t.Fatal("did not wake")
	}
	current.Spec.Replicas = ptr(int32(0))
	delete(current.Annotations, idleHTTPAnnotation)
	c.kube.AppsV1().Deployments(d.Namespace).Update(ctx, current, metav1.UpdateOptions{})
	if err := c.SetHTTPIdle(ctx, target, "web", false); !errors.Is(err, ErrIdleIneligible) {
		t.Fatal("manual zero resurrected", err)
	}
}
func TestIdleScopeRevisionPolicyAndSuspension(t *testing.T) {
	for _, kind := range []string{"owner", "revision", "policy", "suspended", "private", "replicas", "volume", "multiple"} {
		t.Run(kind, func(t *testing.T) {
			c, target, d := idleFixture(t)
			svc := target.Spec.Services["web"]
			switch kind {
			case "owner":
				d.Labels[ownerKey] = "someone-else"
			case "revision":
				target.Revision++
			case "policy":
				c.options.WorkloadPolicy = nil
			case "suspended":
				svc.Suspended = true
			case "private":
				svc.Public = false
			case "replicas":
				svc.Replicas = 2
			case "volume":
				target.Spec.Volumes = map[string]spec.NamedVolume{"data": {}}
			case "multiple":
				target.Spec.Services["other"] = svc
			}
			target.Spec.Services["web"] = svc
			c.kube.AppsV1().Deployments(d.Namespace).Update(context.Background(), d, metav1.UpdateOptions{})
			if err := c.SetHTTPIdle(context.Background(), target, "web", true); err == nil {
				t.Fatal("ineligible target slept")
			}
		})
	}
}
func TestIdleObservationAndNewRevision(t *testing.T) {
	c, target, d := idleFixture(t)
	ctx := context.Background()
	d.Spec.Replicas = ptr(int32(0))
	d.Annotations[idleHTTPAnnotation] = "sleeping"
	d.Status = appsv1.DeploymentStatus{}
	c.kube.AppsV1().Deployments(d.Namespace).Update(ctx, d, metav1.UpdateOptions{})
	c.kube.CoreV1().Pods(d.Namespace).Delete(ctx, "web-pod", metav1.DeleteOptions{})
	o, err := c.Observe(ctx, target)
	if err != nil || o.Status != "healthy" || o.Services[0].Status != "sleeping" {
		t.Fatal(o, err)
	}
	target.Revision++
	svc := target.Spec.Services["web"]
	svc.Suspended = true
	target.Spec.Services["web"] = svc
	if _, err := c.applyDeployment(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	current, _ := c.kube.AppsV1().Deployments(d.Namespace).Get(ctx, "web", metav1.GetOptions{})
	if current.Annotations[idleHTTPAnnotation] != "" || *current.Spec.Replicas != 0 {
		t.Fatal("manual stop retained automatic marker")
	}
	if err = c.SetHTTPIdle(ctx, target, "web", false); !errors.Is(err, ErrIdleIneligible) {
		t.Fatal("manual stop woke", err)
	}
}
