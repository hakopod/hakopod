package cluster

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func runtimeProfileFixture(t *testing.T) (*Client, Target, RuntimeProfileBinding) {
	t.Helper()
	target := testTarget(t)
	svc := target.Spec.Services["api"]
	svc.RuntimeProfile = "bounded-worker"
	target.Spec.Services["api"] = svc
	binding := RuntimeProfileBinding{Name: svc.RuntimeProfile, Project: target.Project, Environment: target.Environment, Application: target.Spec.Name, Service: "api", RuntimeClass: "bounded-worker", Handler: "bounded-runc"}
	class := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: binding.RuntimeClass}, Handler: binding.Handler}
	return &Client{kube: fake.NewClientset(class), options: Options{RuntimeProfileBindings: []RuntimeProfileBinding{binding}}}, target, binding
}

func TestRuntimeProfileScopeAndRevocation(t *testing.T) {
	ctx := context.Background()
	c, target, binding := runtimeProfileFixture(t)
	if err := c.ValidateRuntimeProfiles(ctx, target.Project, target.Environment, target.Spec); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"Name", "Project", "Environment", "Application", "Service"} {
		t.Run(field, func(t *testing.T) {
			changed := binding
			reflect.ValueOf(&changed).Elem().FieldByName(field).SetString("ungranted")
			c.options.RuntimeProfileBindings = []RuntimeProfileBinding{changed}
			if err := c.ValidateRuntimeProfiles(ctx, target.Project, target.Environment, target.Spec); err == nil {
				t.Fatal("a different scope granted the runtime")
			}
		})
	}
	c.options.RuntimeProfileBindings = nil
	svc := target.Spec.Services["api"]
	if err := c.prepareRuntimeProfile(ctx, target, "api", svc, deployment(target, "api", svc, 0)); err == nil {
		t.Fatal("a grant revoked after review was accepted at workload preparation")
	}
	c.options.RuntimeProfileBindings = []RuntimeProfileBinding{binding}
	if err := c.kube.NodeV1().RuntimeClasses().Delete(ctx, binding.RuntimeClass, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateRuntimeProfiles(ctx, target.Project, target.Environment, target.Spec); err == nil {
		t.Fatal("a missing runtime class was accepted")
	}
	_, err := c.kube.NodeV1().RuntimeClasses().Create(ctx, &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: binding.RuntimeClass}, Handler: "other"}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.prepareRuntimeProfile(ctx, target, "api", svc, deployment(target, "api", svc, 0)); err == nil {
		t.Fatal("a replacement runtime handler was accepted")
	}
}

func TestRuntimeProfilePreservesPodSecurityAndPlacement(t *testing.T) {
	c, target, binding := runtimeProfileFixture(t)
	svc := target.Spec.Services["api"]
	wanted := deployment(target, "api", svc, 0)
	before := wanted.DeepCopy()
	if err := c.prepareRuntimeProfile(context.Background(), target, "api", svc, wanted); err != nil {
		t.Fatal(err)
	}
	if wanted.Spec.Template.Spec.RuntimeClassName == nil || *wanted.Spec.Template.Spec.RuntimeClassName != binding.RuntimeClass || wanted.Spec.Template.Annotations["hakopod.io/runtime-profile"] != binding.Name {
		t.Fatal("the pod does not select the approved runtime")
	}
	wanted.Spec.Template.Spec.RuntimeClassName = nil
	wanted.Spec.Template.Annotations = before.Spec.Template.Annotations
	if !reflect.DeepEqual(wanted, before) {
		t.Fatal("runtime selection changed unrelated pod security, resources or placement")
	}
	wanted.Spec.Template.Spec.RuntimeClassName = ptr("hosted-runtime")
	if err := c.prepareRuntimeProfile(context.Background(), target, "api", svc, wanted); err == nil {
		t.Fatal("runtime selection replaced an existing workload policy")
	}
}

func TestRuntimeProfileInstallationBoundaries(t *testing.T) {
	_, _, binding := runtimeProfileFixture(t)
	for _, options := range []Options{
		{DeploymentMode: DeploymentManagedCloud},
		{WorkloadPolicy: func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
			return WorkloadPolicy{}, nil
		}},
		{PlacementPolicy: func(context.Context, PlacementRequest) (WorkloadPolicy, error) { return WorkloadPolicy{}, nil }},
	} {
		if err := validateRuntimeProfileInstallation(options); err != nil {
			t.Fatal("an installation without bindings changed behavior", err)
		}
		options.RuntimeProfileBindings = []RuntimeProfileBinding{binding}
		if err := validateRuntimeProfileInstallation(options); err == nil {
			t.Fatal("an application override was enabled over a hosted runtime policy")
		}
	}
	if err := validateRuntimeProfileInstallation(Options{RuntimeProfileBindings: []RuntimeProfileBinding{binding}}); err != nil {
		t.Fatal("a self-hosted binding was rejected", err)
	}
}

func TestRuntimeProfileBindingFiles(t *testing.T) {
	valid := "schema_version=1\n[[bindings]]\nname='bounded'\nproject='test'\nenvironment='test'\napplication='test'\nservice='api'\nruntime_class='bounded'\nhandler='bounded-runc'\n"
	for _, tc := range []struct {
		name, text string
		mode       os.FileMode
		ok         bool
	}{
		{"valid", valid, 0600, true},
		{"version", strings.Replace(valid, "version=1", "version=2", 1), 0600, false},
		{"unknown", valid + "unexpected=true\n", 0600, false},
		{"scope", strings.Replace(valid, "project='test'", "project='*'", 1), 0600, false},
		{"handler", strings.Replace(valid, "handler='bounded-runc'", "handler='host/runtime'", 1), 0600, false},
		{"duplicate", valid + strings.SplitN(valid, "\n", 2)[1], 0600, false},
		{"writable", valid, 0666, false},
		{"oversize", valid + strings.Repeat("#", 64<<10), 0600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profiles.toml")
			if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			_, err := ReadRuntimeProfileBindingsFile(path)
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected binding file outcome: %v", err)
			}
		})
	}
	if _, err := ReadRuntimeProfileBindingsFile(t.TempDir()); err == nil {
		t.Fatal("a directory was accepted")
	}
	if _, err := ReadRuntimeProfileBindingsFile(""); err != nil {
		t.Fatal("an optional file was required", err)
	}
}
