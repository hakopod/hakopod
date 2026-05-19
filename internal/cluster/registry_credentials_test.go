package cluster

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRegistryCredentialTokenScopeAndRealm(t *testing.T) {
	credential := &RegistryCredential{Registry: "ghcr.io", Username: "user", Password: "private-test-token", Repository: "team/image"}
	requests := 0
	client := &Client{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Host != "ghcr.io" || r.URL.Query().Get("scope") != "repository:team/image:pull" {
			t.Fatalf("credential target/scope escaped: %s", r.URL)
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "user" || password != credential.Password {
			t.Fatal("missing token authentication")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"token":"short-token"}`))}, nil
	})}}
	if _, err := client.registryToken(context.Background(), `Bearer realm="https://attacker.example/token",scope="repository:other:push"`, credential); err == nil || requests != 0 {
		t.Fatal("credential sent to untrusted realm")
	}
	token, err := client.registryToken(context.Background(), `Bearer realm="https://ghcr.io/token",scope="repository:other:push"`, credential)
	if err != nil || token != "short-token" || requests != 1 {
		t.Fatal(err)
	}
}
func TestRegistryCredentialScopeCopiesAndRotation(t *testing.T) {
	ctx := context.Background()
	target := testTarget(t)
	kube := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}})
	current := "registry-one"
	client := &Client{kube: kube, options: Options{RegistrySecretName: func(context.Context, string, string, string) (string, error) { return current, nil }}}
	credential := RegistryCredential{Registry: "registry-1.docker.io", Username: "user", Password: "test-secret-one", Revision: 1}
	scope := RegistryScope(target.Project, target.Environment, "private")
	if err := client.PutPlatformSecret(ctx, current, corev1.SecretTypeDockerConfigJson, RegistrySecretData(credential), map[string]string{registryScopeLabel: scope}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RegistryCredential(ctx, "other", target.Environment, "private", "python:3"); err == nil {
		t.Fatal("cross-project credential accepted")
	}
	if _, err := client.RegistryCredential(ctx, target.Project, target.Environment, "private", "ghcr.io/team/app:1"); err == nil {
		t.Fatal("credential used for another host")
	}
	svc := target.Spec.Services["api"]
	svc.RegistryCredential = "private"
	wanted := deployment(target, "api", svc, 0)
	if err := client.prepareRegistryCredential(ctx, target, "api", svc, wanted); err != nil {
		t.Fatal(err)
	}
	if len(wanted.Spec.Template.Spec.ImagePullSecrets) != 1 || wanted.Spec.Selector.MatchLabels[registryScopeLabel] != "" {
		t.Fatal("pull secret missing or immutable selector changed")
	}
	current = "registry-two"
	credential.Password = "test-secret-two"
	credential.Revision = 2
	if err := client.PutPlatformSecret(ctx, current, corev1.SecretTypeDockerConfigJson, RegistrySecretData(credential), map[string]string{registryScopeLabel: scope}); err != nil {
		t.Fatal(err)
	}
	if count, err := client.RefreshRegistryCopies(ctx, target.Project, target.Environment, "private", false); err != nil || count != 1 {
		t.Fatalf("rotation failed: %d %v", count, err)
	}
	copied, err := kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, wanted.Spec.Template.Spec.ImagePullSecrets[0].Name, metav1.GetOptions{})
	if err != nil || !strings.Contains(string(copied.Data[corev1.DockerConfigJsonKey]), "test-secret-two") {
		t.Fatal("copy not rotated")
	}
	if _, err := kube.AppsV1().Deployments(Namespace(target.ApplicationID)).Create(ctx, wanted, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if used, err := client.RegistryInUse(ctx, target.Project, target.Environment, "private"); err != nil || !used {
		t.Fatal("live reference not detected")
	}
}
