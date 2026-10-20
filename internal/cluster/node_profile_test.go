package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func nodeProfileBinding() NodeProfileBinding {
	return NodeProfileBinding{Name: "kelvo-node", Project: "p", Environment: "e", Application: "kelvo", Service: "node", Node: "kelvo-1", Seccomp: "unconfined", Privileged: true}
}

func TestNodeProfileGrantIsScopedAndPinned(t *testing.T) {
	c := &Client{options: Options{NodeProfileBindings: []NodeProfileBinding{nodeProfileBinding()}}}
	svc := spec.Service{NodeProfile: "kelvo-node", NodeName: "kelvo-1", Replicas: 1}
	if _, err := c.resolveNodeProfile("p", "e", "kelvo", "node", svc); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*spec.Service, *[4]string){
		"other node":  func(s *spec.Service, _ *[4]string) { s.NodeName = "other" },
		"no pin":      func(s *spec.Service, _ *[4]string) { s.NodeName = "" },
		"two copies":  func(s *spec.Service, _ *[4]string) { s.Replicas = 2 },
		"other scope": func(_ *spec.Service, scope *[4]string) { scope[3] = "worker" },
	} {
		s, scope := svc, [4]string{"p", "e", "kelvo", "node"}
		mutate(&s, &scope)
		if _, err := c.resolveNodeProfile(scope[0], scope[1], scope[2], scope[3], s); err == nil {
			t.Fatalf("%s was granted", name)
		}
	}
	managed := &Client{options: Options{DeploymentMode: DeploymentManagedCloud, NodeProfileBindings: []NodeProfileBinding{nodeProfileBinding()}}}
	if _, err := managed.resolveNodeProfile("p", "e", "kelvo", "node", svc); err == nil {
		t.Fatal("managed cloud honoured a node profile")
	}
}

func TestApplyNodeProfileRecreatesAndOnlyRelaxesTheServiceContainer(t *testing.T) {
	d := appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		SecurityContext: &corev1.PodSecurityContext{},
		Containers:      []corev1.Container{{Name: "app", SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false)}}, {Name: "helper", SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false)}}},
	}}}
	applyNodeProfile(nodeProfileBinding(), &d)
	pod := d.Template.Spec
	if d.Strategy.Type != appsv1.RecreateDeploymentStrategyType || pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeUnconfined {
		t.Fatal("strategy or seccomp not applied")
	}
	if !*pod.Containers[0].SecurityContext.Privileged || *pod.Containers[1].SecurityContext.AllowPrivilegeEscalation || pod.Containers[1].SecurityContext.Privileged != nil {
		t.Fatal("helper container was relaxed or service container was not")
	}
	b := nodeProfileBinding()
	b.Seccomp, b.Privileged = "runtime-default", false
	applyNodeProfile(b, &d)
	if d.Template.Spec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault || *d.Template.Spec.Containers[0].SecurityContext.Privileged {
		t.Fatal("a narrower grant kept the wider profile")
	}
}

func TestNodeProfileBindingsFileIsStrict(t *testing.T) {
	dir := t.TempDir()
	write := func(body string, mode os.FileMode) string {
		p := filepath.Join(dir, strings.ReplaceAll(t.Name()+body[:8], "/", "_"))
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil { // WriteFile honours the umask
			t.Fatal(err)
		}
		return p
	}
	good := "schema_version=1\n[[bindings]]\nname='kelvo-node'\nproject='p'\nenvironment='e'\napplication='kelvo'\nservice='node'\nnode='kelvo-1'\nseccomp='unconfined'\nprivileged=true\n"
	if got, err := ReadNodeProfileBindingsFile(write(good, 0600)); err != nil || len(got) != 1 {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"unknown field": good + "extra=1\n",
		"bad seccomp":   strings.Replace(good, "unconfined", "localhost", 1),
		"bad node":      strings.Replace(good, "kelvo-1", "Bad Node", 1),
		"no version":    strings.Replace(good, "schema_version=1", "schema_version=2", 1),
	} {
		if _, err := ReadNodeProfileBindingsFile(write(body, 0600)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := ReadNodeProfileBindingsFile(write(good+"\n", 0666)); err == nil {
		t.Fatal("group/other writable file accepted")
	}
}
