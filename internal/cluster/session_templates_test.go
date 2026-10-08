package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func sessionTemplateFixture(t *testing.T) (*Client, Target) {
	t.Helper()
	app, err := spec.Normalize(spec.Application{Name: "session-test", Services: map[string]spec.Service{"worker": {Image: "example/worker@sha256:" + strings.Repeat("a", 64), Command: []string{"worker"}, RunAsUser: 1000, RunAsGroup: 1000, FSGroup: 1000, ReadOnlyRootFilesystem: true, RuntimeProfile: "sandbox", Session: &spec.SandboxSession{AllowedIdentities: []string{strings.Repeat("b", 32)}, ReadyCommand: []string{"ready"}, HelperCommand: []string{"helper"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "session-test", Project: "demo", Environment: "test", Revision: 1, Spec: app}
	return &Client{kube: fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}})}, target
}

func TestSessionTemplateLifecycleDoesNotCreateWorker(t *testing.T) {
	c, target := sessionTemplateFixture(t)
	ctx := context.Background()
	svc := target.Spec.Services["worker"]
	if err := c.applySessionTemplate(ctx, target, "worker", svc); err != nil {
		t.Fatal(err)
	}
	for _, action := range c.kube.(*fake.Clientset).Actions() {
		if action.GetVerb() == "create" && action.GetResource().Resource != "configmaps" {
			t.Fatal("template created runtime resource", action.GetResource())
		}
	}
	observed, err := c.Observe(ctx, target)
	if err != nil || observed.Status != "healthy" || len(observed.Services) != 1 || observed.Services[0].Status != "configured" {
		t.Fatal("template was not observed independently of a Deployment", observed, err)
	}
	target.Revision++
	state, err := c.observeSessionTemplate(ctx, target, "worker", svc)
	if err != nil || state.Status != "pending" {
		t.Fatal("stale template accepted", state, err)
	}
	svc.Suspended = true
	target.Spec.Services["worker"] = svc
	if err = c.applySessionTemplate(ctx, target, "worker", svc); err != nil {
		t.Fatal(err)
	}
	state, err = c.observeSessionTemplate(ctx, target, "worker", svc)
	if err != nil || state.Status != "stopped" {
		t.Fatal("suspension ignored", state, err)
	}
	target.Spec.Services = map[string]spec.Service{}
	if err = c.cleanupSessionTemplates(ctx, target); err != nil {
		t.Fatal(err)
	}
	markers, err := c.kube.CoreV1().ConfigMaps(Namespace(target.ApplicationID)).List(ctx, metav1.ListOptions{})
	if err != nil || len(markers.Items) != 0 {
		t.Fatal("template cleanup failed", err)
	}
}

func TestSessionTemplateRejectsOwnershipChangeAndCancelledWrite(t *testing.T) {
	c, target := sessionTemplateFixture(t)
	ctx := context.Background()
	svc := target.Spec.Services["worker"]
	target.BeforeStep = func(context.Context) error { return context.Canceled }
	if err := c.applySessionTemplate(ctx, target, "worker", svc); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled deployment wrote template", err)
	}
	target.BeforeStep = nil
	if err := c.applySessionTemplate(ctx, target, "worker", svc); err != nil {
		t.Fatal(err)
	}
	cm, err := c.kube.CoreV1().ConfigMaps(Namespace(target.ApplicationID)).Get(ctx, sessionTemplateName("worker"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cm.Labels[ownerKey] = "unrelated"
	if _, err = c.kube.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.applySessionTemplate(ctx, target, "worker", svc); err == nil {
		t.Fatal("overwrote foreign marker")
	}
	if _, err = c.observeSessionTemplate(ctx, target, "worker", svc); err == nil {
		t.Fatal("observed foreign marker")
	}
	target.Spec.Services = map[string]spec.Service{}
	// The foreign marker is outside our selector. Cleanup must not remove it.
	if err = c.cleanupSessionTemplates(ctx, target); err != nil {
		t.Fatal(err)
	}
	if _, err = c.kube.CoreV1().ConfigMaps(cm.Namespace).Get(ctx, cm.Name, metav1.GetOptions{}); err != nil {
		t.Fatal("removed foreign marker", err)
	}
}

func TestSessionTemplateRejectsWorkloadKindTransitions(t *testing.T) {
	c, target := sessionTemplateFixture(t)
	ctx := context.Background()
	svc := target.Spec.Services["worker"]
	if err := c.applySessionTemplate(ctx, target, "worker", svc); err != nil {
		t.Fatal(err)
	}
	svc.Session = nil
	target.Spec.Services["worker"] = svc
	if err := c.validateWorkloadKinds(ctx, target); err == nil {
		t.Fatal("converted session template into a Deployment")
	}
	c, target = sessionTemplateFixture(t)
	svc = target.Spec.Services["worker"]
	if _, err := c.kube.AppsV1().Deployments(Namespace(target.ApplicationID)).Create(ctx, deployment(target, "worker", svc, 0), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.validateWorkloadKinds(ctx, target); err == nil {
		t.Fatal("converted Deployment into session template")
	}
}

func TestSessionRuntimeRequiresRunscAndPinnedGuard(t *testing.T) {
	c, target, binding := runtimeProfileFixture(t)
	svc := target.Spec.Services["api"]
	svc.Session = &spec.SandboxSession{}
	target.Spec.Services["api"] = svc
	ctx := context.Background()
	c.options.SessionGuardImage = "example/guard@sha256:" + strings.Repeat("a", 64)
	if err := c.ValidateRuntimeProfiles(ctx, target.Project, target.Environment, target.Spec); err == nil {
		t.Fatal("shared-kernel runtime accepted")
	}
	binding.Handler = "runsc"
	c.options.RuntimeProfileBindings = []RuntimeProfileBinding{binding}
	class, err := c.kube.NodeV1().RuntimeClasses().Get(ctx, binding.RuntimeClass, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	class.Handler = "runsc"
	class.Overhead = &nodev1.Overhead{PodFixed: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("20m"), corev1.ResourceMemory: resource.MustParse("50Mi")}}
	if _, err = c.kube.NodeV1().RuntimeClasses().Update(ctx, class, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.ValidateRuntimeProfiles(ctx, target.Project, target.Environment, target.Spec); err != nil {
		t.Fatal(err)
	}
	for _, image := range []string{"", "example/guard:latest", "example/guard@sha256:invalid"} {
		c.options.SessionGuardImage = image
		if err = c.ValidateRuntimeProfiles(ctx, target.Project, target.Environment, target.Spec); err == nil {
			t.Fatal("missing or mutable guard accepted", image)
		}
	}
}

func TestSessionCapacityIncludesConcurrentWorkers(t *testing.T) {
	_, target := sessionTemplateFixture(t)
	c := &Client{}
	got, err := c.Preflight(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	profile := spec.EffectiveResources(target.Spec.Services["worker"])
	cpu := resource.MustParse(profile.CPURequest)
	memory := resource.MustParse(profile.MemoryRequest)
	if got.CPURequestMillis != (max(cpu.MilliValue(), sessionGuardCPURequestMillis)+sessionRuntimeCPUOverheadMillis)*spec.MaxSandboxSessions || got.MemoryRequestBytes != (max(memory.Value(), sessionGuardMemoryRequestBytes)+sessionRuntimeMemoryOverheadBytes)*spec.MaxSandboxSessions {
		t.Fatal("session concurrency was omitted from capacity", got)
	}
}

func TestSessionCapacityIncludesGuardAndRuntimeOnSelectedNode(t *testing.T) {
	_, target := sessionTemplateFixture(t)
	service := target.Spec.Services["worker"]
	service.Resources = &spec.Resources{CPURequest: "1m", MemoryRequest: "1Mi"}
	cpu, memory := scheduledPodRequests(service, nil)
	if cpu.MilliValue() != 70 || memory.Value() != 82<<20 {
		t.Fatal("guard initialization or runtime overhead omitted", cpu.String(), memory.String())
	}
	node := func(name, cpu, memory string) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}, Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)}}}
	}
	target.Spec.Services["worker"] = service
	c := &Client{kube: fake.NewClientset(node("tiny", "60m", "80Mi"))}
	report, err := c.Preflight(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	has := func(code, status string) bool {
		for _, check := range report.Checks {
			if check.Code == code && check.Status == status {
				return true
			}
		}
		return false
	}
	if !has("service_capacity", "blocked") {
		t.Fatal("single worker overhead did not block undersized node", report)
	}
	service.NodeName = "one"
	service.Resources = &spec.Resources{CPURequest: "200m", MemoryRequest: "128Mi"}
	target.Spec.Services["worker"] = service
	c.kube = fake.NewClientset(node("one", "1", "1Gi"), node("two", "1", "1Gi"))
	report, err = c.Preflight(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if !has("resources", "passed") || !has("node_capacity", "blocked") {
		t.Fatal("pinned session workers incorrectly used aggregate cluster capacity", report)
	}
}
