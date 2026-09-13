package cluster

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestTemplateSecretsCreateAndOwnedReads(t *testing.T) {
	ctx := context.Background()
	c := &Client{kube: fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: PlatformNamespace, Labels: map[string]string{managedBy: "hakopod"}}})}
	if err := c.CreateWorkloadSecret(ctx, "project", "test", "app", "password", "initial secret value"); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateWorkloadSecret(ctx, "project", "test", "app", "password", "replacement secret value"); !apierrors.IsAlreadyExists(err) {
		t.Fatal("generation replaced an existing reference", err)
	}
	values, err := c.ReadWorkloadSecrets(ctx, "project", "test", "app", []string{"password"})
	if err != nil || values["password"] != "initial secret value" {
		t.Fatal("owned secret read failed", err)
	}
	if _, err = c.ReadWorkloadSecrets(ctx, "project", "test", "another", []string{"password"}); err == nil {
		t.Fatal("another application read a secret")
	}
	secret, err := c.kube.CoreV1().Secrets(PlatformNamespace).Get(ctx, workloadSecretName("project", "test", "app", "password"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret.Labels["hakopod.io/secret-scope"] = secretScope("project", "test", "another")
	if _, err = c.kube.CoreV1().Secrets(PlatformNamespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ReadWorkloadSecrets(ctx, "project", "test", "app", []string{"password"}); err == nil {
		t.Fatal("foreign secret labels were ignored")
	}
}
