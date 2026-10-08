package cluster

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSandboxPrivateRegistryCopyRotationAndUsage(t *testing.T) {
	c, r := sandboxFixture(t)
	ctx := context.Background()
	svc := r.Source.Services[r.Service]
	svc.RegistryCredential = "private"
	r.Source.Services[r.Service] = svc
	target, svc, err := sandboxTarget(r)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.applySessionTemplate(ctx, target, r.Service, svc); err != nil {
		t.Fatal(err)
	}
	current := "private-one"
	c.options.RegistrySecretName = func(context.Context, string, string, string) (string, error) { return current, nil }
	scope := RegistryScope(r.Project, r.Environment, "private")
	credential := RegistryCredential{Registry: "registry-1.docker.io", Username: "user", Password: "fixture-token-one", Revision: 1}
	if err = c.PutPlatformSecret(ctx, current, corev1.SecretTypeDockerConfigJson, RegistrySecretData(credential), map[string]string{registryScopeLabel: scope}); err != nil {
		t.Fatal(err)
	}
	r = startSandbox(t, c, r)
	secret, err := c.kube.CoreV1().Secrets(SandboxNamespace(r.ID)).Get(ctx, sandboxRegistrySecret, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if secret.Immutable == nil || !*secret.Immutable || secret.Labels[registryScopeLabel] != "" {
		t.Fatal("pull copy is mutable or enrolled in shared refresh")
	}
	pod, err := c.kube.CoreV1().Pods(SandboxNamespace(r.ID)).Get(ctx, sandboxPodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pod.Spec.ImagePullSecrets) != 1 || pod.Labels[registryScopeLabel] != scope {
		t.Fatal("private pull credential is not scoped")
	}
	for _, v := range pod.Spec.Volumes {
		if v.Secret != nil {
			t.Fatal("pull credential mounted into worker")
		}
	}
	if used, err := c.RegistryInUse(ctx, r.Project, r.Environment, "private"); err != nil || !used {
		t.Fatal("session registry usage not found", err)
	}
	current = "private-two"
	credential.Password = "fixture-token-two"
	credential.Revision = 2
	if err = c.PutPlatformSecret(ctx, current, corev1.SecretTypeDockerConfigJson, RegistrySecretData(credential), map[string]string{registryScopeLabel: scope}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ObserveSession(ctx, r); err == nil || !strings.Contains(err.Error(), "credential changed") {
		t.Fatal("rotated registry credential accepted", err)
	}
	secret, _ = c.kube.CoreV1().Secrets(SandboxNamespace(r.ID)).Get(ctx, sandboxRegistrySecret, metav1.GetOptions{})
	if strings.Contains(string(secret.Data[corev1.DockerConfigJsonKey]), "fixture-token-two") {
		t.Fatal("live pull copy was mutated")
	}
	secret.Labels[sandboxOwner] = "foreign"
	_, _ = c.kube.CoreV1().Secrets(secret.Namespace).Update(ctx, secret, metav1.UpdateOptions{})
	if err = c.sandboxRegistry(ctx, target, r, svc, false); err == nil {
		t.Fatal("foreign pull credential accepted")
	}
}
