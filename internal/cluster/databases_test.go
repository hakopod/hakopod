package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func redisControllerFixture() *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "redis-operator", Namespace: "redis-operator", Generation: 1}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"hakopod.io/redis-controller-source": redisControllerSource}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "redis-operator", Image: "example.invalid/controller:fixture@sha256:" + strings.Repeat("a", 64), Env: []corev1.EnvVar{{Name: "EXEC_COMMAND_TIMEOUT", Value: "20m"}}}}}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, AvailableReplicas: 1, UpdatedReplicas: 1, Replicas: 1}}
}

func TestDatabaseRequiresSafeCurrentRedisController(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*appsv1.Deployment)
	}{
		{"upstream release without credential fix", func(d *appsv1.Deployment) { d.Spec.Template.Annotations = nil }},
		{"mutable image", func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.Containers[0].Image = "example.invalid/controller:latest"
		}},
		{"short command timeout", func(d *appsv1.Deployment) { d.Spec.Template.Spec.Containers[0].Env[0].Value = "5m" }},
		{"new controller still unavailable", func(d *appsv1.Deployment) { d.Status.UnavailableReplicas = 1 }},
		{"only old replicas available", func(d *appsv1.Deployment) { d.Status.UpdatedReplicas = 0 }},
		{"old replica still serving during rollout", func(d *appsv1.Deployment) { d.Status.Replicas = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment := redisControllerFixture()
			tc.change(deployment)
			c := &Client{kube: fake.NewClientset(deployment), dynamic: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{redisDatabaseResource: "RedisList"})}
			if err := c.DatabaseControllerAvailable(context.Background(), database.Spec{Engine: "redis", Mode: "standalone"}); err == nil {
				t.Fatal("unsafe or incomplete controller rollout accepted")
			}
		})
	}
}

func TestUnqualifiedDatabaseEnginesRemainUnavailable(t *testing.T) {
	c := &Client{kube: fake.NewClientset(), dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}
	for _, engine := range []string{"mysql", "clickhouse", "oracle"} {
		t.Run(engine, func(t *testing.T) {
			err := c.DatabaseControllerAvailable(context.Background(), database.Spec{Engine: engine, Mode: "standalone"})
			if err == nil || !strings.Contains(err.Error(), "unavailable in this release pending native qualification") {
				t.Fatal("unqualified database engine was available", err)
			}
			d := database.Resource{ID: strings.Repeat("a", 32), Project: "demo", Environment: "development", Revision: 1, Spec: database.Spec{Engine: engine}}
			err = c.ApplyDatabase(context.Background(), d, []byte("development-fixture-password-with-32-bytes"), func() error { return nil })
			if err == nil || !strings.Contains(err.Error(), "unavailable in this release pending native qualification") {
				t.Fatal("unqualified database apply escaped the release gate", err)
			}
		})
	}
	namespaces, err := c.kube.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
	if err != nil || len(namespaces.Items) != 0 {
		t.Fatal("release-held database apply changed namespaces", err)
	}
}

func TestDatabaseUpdatePreservesControllerMetadata(t *testing.T) {
	ctx := context.Background()
	c := &Client{kube: fake.NewClientset(redisControllerFixture()), dynamic: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{redisDatabaseResource: "RedisList"})}
	d := database.Resource{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Project: "demo", Environment: "development", Revision: 1, Spec: database.Spec{SchemaVersion: 1, Name: "fixture", Engine: "redis", Version: "8", Mode: "standalone", Shards: 1, CPU: "100m", Memory: "128Mi", StorageGiB: 1}}
	password := []byte("development-fixture-password-with-32-bytes")
	if err := c.ApplyDatabase(ctx, d, password, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	api := c.dynamic.Resource(redisDatabaseResource).Namespace(DatabaseNamespace(d.ID))
	object, err := api.Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	object.SetFinalizers([]string{"controller.example/retain"})
	annotations := object.GetAnnotations()
	annotations["controller.example/state"] = "owned"
	object.SetAnnotations(annotations)
	object.Object["spec"].(map[string]any)["controllerDefault"] = "keep"
	if _, err = api.Update(ctx, object, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	d.Revision = 2
	d.Spec.CPU = "200m"
	if err = c.ApplyDatabase(ctx, d, password, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	updated, err := api.Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.GetFinalizers()) != 1 || updated.GetAnnotations()["controller.example/state"] != "owned" || updated.Object["spec"].(map[string]any)["controllerDefault"] != "keep" {
		t.Fatal("controller-owned metadata was lost")
	}
	applied, err := c.DatabaseRevisionApplied(ctx, d)
	if err != nil || !applied {
		t.Fatal("accepted revision not recognized", err)
	}
	d.Spec.CPU = "300m"
	applied, err = c.DatabaseRevisionApplied(ctx, d)
	if err != nil || applied {
		t.Fatal("changed desired configuration was treated as applied", err)
	}
}
