package cluster

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func runnerTarget(t *testing.T) Target {
	t.Helper()
	a, e := spec.Normalize(spec.Application{Name: "runners", Services: map[string]spec.Service{"runner": {Actions: &spec.Actions{Repository: "team/repo", Credential: "github-token"}}}})
	if e != nil {
		t.Fatal(e)
	}
	return Target{ApplicationID: "0123456789abcdef0123456789abcdef", Project: "demo", Environment: "development", Spec: a, Revision: 1}
}
func TestActionsSandboxAndResourceBudget(t *testing.T) {
	target := runnerTarget(t)
	s := target.Spec.Services["runner"]
	p := actionsPod(target, "runner", "slot", s)
	if *p.Spec.RuntimeClassName != ActionsRuntime || *p.Spec.AutomountServiceAccountToken || p.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatal("sandbox boundary missing")
	}
	if p.Spec.InitContainers[1].RestartPolicy == nil || *p.Spec.InitContainers[1].RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Fatal("Docker must be a lifecycle-bound sidecar")
	}
	for _, v := range p.Spec.Volumes {
		if v.HostPath != nil {
			t.Fatal("host storage exposed")
		}
	}
	for _, c := range append(p.Spec.InitContainers, p.Spec.Containers...) {
		if c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged {
			t.Fatal("host privileged mode")
		}
		for _, e := range c.Env {
			if e.Name == "GITHUB_TOKEN" || e.Value == "github-token" {
				t.Fatal("provider credential exposed")
			}
		}
	}
	profile := spec.EffectiveResources(s)
	for _, kind := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		for _, limit := range []bool{false, true} {
			total := resource.MustParse("100m")
			value := profile.CPURequest
			if limit {
				value = profile.CPULimit
			}
			if kind == corev1.ResourceMemory {
				total = resource.MustParse("512Mi")
				value = profile.MemoryRequest
				if limit {
					value = profile.MemoryLimit
				}
			}
			for _, c := range []corev1.Container{p.Spec.InitContainers[1], p.Spec.Containers[0]} {
				r := c.Resources.Requests
				if limit {
					r = c.Resources.Limits
				}
				total.Add(r[kind])
			}
			if total.Cmp(resource.MustParse(value)) > 0 {
				t.Fatal("runtime overhead exceeds advertised budget", kind, total.String(), value)
			}
		}
	}
}
func TestActionsRuntimeFailsClosedAndMutationsAreFenced(t *testing.T) {
	target := runnerTarget(t)
	k := fake.NewClientset()
	c := &Client{kube: k}
	ctx := context.Background()
	if c.ActionsAvailable(ctx) == nil {
		t.Fatal("missing runtime accepted")
	}
	r := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: ActionsRuntime, Labels: map[string]string{managedBy: "hakopod"}}, Handler: ActionsRuntime, Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{"hakopod.io/actions-runtime": "ready"}}}
	k.NodeV1().RuntimeClasses().Create(ctx, r, metav1.CreateOptions{})
	if c.ActionsAvailable(ctx) == nil {
		t.Fatal("missing ready node accepted")
	}
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", Labels: map[string]string{"hakopod.io/actions-runtime": "ready"}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	k.CoreV1().Nodes().Create(ctx, n, metav1.CreateOptions{})
	if err := c.ActionsAvailable(ctx); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("claim lost")
	target.BeforeStep = func(context.Context) error { return denied }
	if err := c.SaveActionsConfig(ctx, target, "runner", "slot", "single-job"); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if _, err := c.DeleteActionsPod(ctx, target, "slot"); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if err := c.DeleteActionsConfig(ctx, target, "slot"); !errors.Is(err, denied) {
		t.Fatal(err)
	}
}
func TestActionsApplicationCannotChangeNamespaceKind(t *testing.T) {
	target := runnerTarget(t)
	ctx := context.Background()
	k := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: map[string]string{"hakopod.io/workload-kind": "ordinary"}}})
	c := &Client{kube: k}
	if c.validateWorkloadKinds(ctx, target) == nil {
		t.Fatal("ordinary namespace elevated")
	}
	ns, _ := k.CoreV1().Namespaces().Get(ctx, Namespace(target.ApplicationID), metav1.GetOptions{})
	ns.Labels["hakopod.io/workload-kind"] = "actions"
	k.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{})
	target.Spec.Services = map[string]spec.Service{"web": {Image: "nginx:alpine"}}
	if c.validateWorkloadKinds(ctx, target) == nil {
		t.Fatal("ordinary workload allowed in Actions namespace")
	}
}
