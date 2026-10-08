package cluster

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
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

func TestRuntimeProfileRejectsUnsupportedClassState(t *testing.T) {
	for name, change := range map[string]func(*nodev1.RuntimeClass){
		"terminating": func(class *nodev1.RuntimeClass) {
			now := metav1.Now()
			class.DeletionTimestamp = &now
		},
		"overhead": func(class *nodev1.RuntimeClass) {
			class.Overhead = &nodev1.Overhead{PodFixed: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Mi")}}
		},
		"node selector": func(class *nodev1.RuntimeClass) {
			class.Scheduling = &nodev1.Scheduling{NodeSelector: map[string]string{"runtime": "bounded"}}
		},
		"tolerations": func(class *nodev1.RuntimeClass) {
			class.Scheduling = &nodev1.Scheduling{Tolerations: []corev1.Toleration{{Key: "runtime", Operator: corev1.TolerationOpExists}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			c, target, binding := runtimeProfileFixture(t)
			class, err := c.kube.NodeV1().RuntimeClasses().Get(ctx, binding.RuntimeClass, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			change(class)
			if _, err := c.kube.NodeV1().RuntimeClasses().Update(ctx, class, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := c.ValidateRuntimeProfiles(ctx, target.Project, target.Environment, target.Spec); err == nil {
				t.Fatal("an unsupported RuntimeClass passed acceptance")
			}
			svc := target.Spec.Services["api"]
			if _, err := c.applyDeployment(ctx, target, "api", svc); err == nil {
				t.Fatal("an unsupported RuntimeClass reached deployment")
			}
		})
	}
}

func TestRuntimeProfileDeploymentSwitchAndRemoval(t *testing.T) {
	ctx := context.Background()
	c, target, binding := runtimeProfileFixture(t)
	svc := target.Spec.Services["api"]
	if _, err := c.applyDeployment(ctx, target, "api", svc); err != nil {
		t.Fatal(err)
	}
	api := c.kube.AppsV1().Deployments(Namespace(target.ApplicationID))
	current, err := api.Get(ctx, "api", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	current.Spec.Template.Annotations["reloader.example/restartedAt"] = "keep"
	if _, err := api.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	binding.Name, binding.RuntimeClass = "other-worker", "other-runtime"
	c.options.RuntimeProfileBindings = append(c.options.RuntimeProfileBindings, binding)
	if _, err := c.kube.NodeV1().RuntimeClasses().Create(ctx, &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: binding.RuntimeClass}, Handler: binding.Handler}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{binding.Name, ""} {
		svc.RuntimeProfile = profile
		target.Spec.Services["api"] = svc
		if _, err := c.applyDeployment(ctx, target, "api", svc); err != nil {
			t.Fatal(err)
		}
		current, err := api.Get(ctx, "api", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		pod := current.Spec.Template
		if pod.Annotations["reloader.example/restartedAt"] != "keep" {
			t.Fatal("runtime selection removed another controller's annotation")
		}
		if profile == "" {
			if _, present := pod.Annotations[runtimeProfileAnnotation]; present || pod.Spec.RuntimeClassName != nil {
				t.Fatal("removing the profile retained its runtime or annotation")
			}
		} else if pod.Annotations[runtimeProfileAnnotation] != profile || pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != binding.RuntimeClass {
			t.Fatal("switching the profile retained the previous runtime or annotation")
		}
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

func TestRuntimeProfileFixtureCleanupOwnership(t *testing.T) {
	for _, tc := range []struct {
		name      string
		knownUID  bool
		replaced  bool
		unrelated bool
	}{
		{name: "acknowledged creation", knownUID: true},
		{name: "lost creation response"},
		{name: "replaced class", knownUID: true, replaced: true},
		{name: "unrelated class", unrelated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "fixture", Labels: map[string]string{"hakopod.io/runtime-fixture": "unique-marker"}}, Handler: "runc"}
			current := expected.DeepCopy()
			current.UID = "original"
			if tc.knownUID {
				expected.UID = current.UID
			}
			if tc.replaced {
				current.UID = "replacement"
			}
			if tc.unrelated {
				current.Labels = nil
			}
			c := &Client{kube: fake.NewClientset(current)}
			ctx := context.Background()
			err := cleanupRuntimeProfileFixtureClass(ctx, c, expected)
			_, getErr := c.kube.NodeV1().RuntimeClasses().Get(ctx, expected.Name, metav1.GetOptions{})
			if tc.replaced || tc.unrelated {
				if err == nil || getErr != nil {
					t.Fatal("cleanup did not preserve an unowned class", err, getErr)
				}
			} else if err != nil || !apierrors.IsNotFound(getErr) {
				t.Fatal("cleanup left the owned fixture", err, getErr)
			}
		})
	}
}

func TestRuntimeProfileFixtureAmbiguousCreation(t *testing.T) {
	expected := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "fixture", Labels: map[string]string{"hakopod.io/runtime-fixture": "unique-marker"}}, Handler: "runc"}
	t.Run("unconfirmed absence", func(t *testing.T) {
		c := &Client{kube: fake.NewClientset()}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if err := cleanupRuntimeProfileFixtureClass(ctx, c, expected); err == nil || !strings.Contains(err.Error(), "unconfirmed") {
			t.Fatal("an unresolved creation was reported as cleaned", err)
		}
	})
	t.Run("delayed creation", func(t *testing.T) {
		current := expected.DeepCopy()
		current.UID = "delayed"
		kube := fake.NewClientset(current)
		reads := 0
		kube.PrependReactor("get", "runtimeclasses", func(kubetesting.Action) (bool, runtime.Object, error) {
			reads++
			if reads == 1 {
				return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "node.k8s.io", Resource: "runtimeclasses"}, expected.Name)
			}
			return false, nil, nil
		})
		c := &Client{kube: kube}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := cleanupRuntimeProfileFixtureClass(ctx, c, expected); err != nil {
			t.Fatal("delayed fixture creation was not reconciled", err)
		}
		if _, err := kube.NodeV1().RuntimeClasses().Get(ctx, expected.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatal("delayed fixture remains", err)
		}
	})
}
