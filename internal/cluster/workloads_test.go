package cluster

import (
	"context"
	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
	"time"
)

func TestPersistentSecretScopeAndOwnership(t *testing.T) {
	ctx := context.Background()
	client := &Client{kube: fake.NewClientset()}
	target := testTarget(t)
	service := target.Spec.Services["api"]
	service.Volume = &spec.Volume{MountPath: "/data", SizeGiB: 1}
	service.Secrets = map[string]spec.SecretRef{"PASSWORD": {Ref: "db-password"}}
	if err := client.PutWorkloadSecret(ctx, target.Project, target.Environment, "other-app", "db-password", "wrong-scope"); err != nil {
		t.Fatal(err)
	}
	if err := client.prepareWorkloadSecrets(ctx, target, "api", service); err == nil {
		t.Fatal("cross-application secret resolved")
	}
	if err := client.PutWorkloadSecret(ctx, target.Project, target.Environment, target.Spec.Name, "db-password", "correct-scope"); err != nil {
		t.Fatal(err)
	}
	if err := client.prepareWorkloadSecrets(ctx, target, "api", service); err != nil {
		t.Fatal(err)
	}
	if err := client.prepareStorage(ctx, target, "api", service); err != nil {
		t.Fatal(err)
	}
	deployment := deployment(target, "api", service, time.Minute)
	if deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatal("persistent service uses concurrent rolling writers")
	}
	env := deployment.Spec.Template.Spec.Containers[0].Env
	if env[len(env)-1].Value != "" || env[len(env)-1].ValueFrom.SecretKeyRef.Name != "api-environment" {
		t.Fatal("secret not injected by reference")
	}
	service.Volume.SizeGiB = 2
	if err := client.prepareStorage(ctx, target, "api", service); err == nil {
		t.Fatal("volume resized silently")
	}
	_, err := client.kube.CoreV1().Secrets(PlatformNamespace).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "foreign"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.PutPlatformSecret(ctx, "foreign", corev1.SecretTypeOpaque, map[string][]byte{"value": []byte("private")}, nil); err == nil {
		t.Fatal("foreign credential overwritten")
	}
	if err = client.DeletePlatformSecret(ctx, "foreign"); err == nil {
		t.Fatal("foreign credential deleted")
	}
	service.GPU = &spec.GPU{Count: 1}
	if err = client.prepareStorage(ctx, target, "api", service); err == nil {
		t.Fatal("GPU requirement ignored")
	}
}
