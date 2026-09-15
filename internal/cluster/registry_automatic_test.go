package cluster

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAutomaticRegistryCredentials(t *testing.T) {
	for _, host := range []string{"ghcr.io", "gcr.io", "docker.io", "index.docker.io"} {
		for _, selected := range []string{"", "bad"} {
			t.Run(host+"/"+selected, func(t *testing.T) {
				target := testTarget(t)
				image := host + "/team/private:latest"
				canonical, _ := NormalizeRegistry(host)
				target.Spec.Services = map[string]spec.Service{"api": {Image: image, RegistryCredential: selected}, "worker": {Image: image, RegistryCredential: selected}}
				kube := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "test"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64"}}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}})
				namesCalled := 0
				attempts := []string{}
				client := &Client{kube: kube, options: Options{
					RegistryCredentialNames: func(_ context.Context, p, e, h string) ([]string, error) {
						if p != target.Project || e != target.Environment || h != canonical {
							t.Fatal("lookup escaped scope")
						}
						namesCalled++
						return []string{"bad", "foreign", "good"}, nil
					},
					RegistrySecretName: func(_ context.Context, p, e, n string) (string, error) {
						if p != target.Project || e != target.Environment {
							return "", errors.New("scope")
						}
						return "automatic-test-" + n, nil
					},
				}, http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Host != canonical {
						t.Fatal("credentials sent to another host")
					}
					username, _, _ := r.BasicAuth()
					attempts = append(attempts, username)
					status, body := 403, "denied"
					if username == "good" {
						status = 200
						body = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"platform":{"architecture":"amd64","os":"linux"}}]}`
					}
					return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}}
				for _, name := range []string{"bad", "foreign", "good"} {
					registry := canonical
					if name == "foreign" {
						registry = "unrelated.example"
					}
					if err := client.PutPlatformSecret(context.Background(), "automatic-test-"+name, corev1.SecretTypeDockerConfigJson, RegistrySecretData(RegistryCredential{Registry: registry, Username: name, Password: "fixture-password", Revision: 1}), map[string]string{registryScopeLabel: RegistryScope(target.Project, target.Environment, name)}); err != nil {
						t.Fatal(err)
					}
				}
				resolved, err := client.ResolveScoped(context.Background(), target.Spec, target.Project, target.Environment)
				if err != nil {
					t.Fatal(err)
				}
				wantAttempts := []string{"", "bad", "good"}
				if selected != "" {
					wantAttempts = []string{"bad", "", "good"}
				}
				if !reflect.DeepEqual(attempts, wantAttempts) || namesCalled != 1 {
					t.Fatalf("wrong retry/cache behavior: %v, lists=%d", attempts, namesCalled)
				}
				for name, svc := range resolved.Services {
					if svc.RegistryCredential != "good" || !strings.Contains(svc.Image, "@sha256:") {
						t.Fatal("resolved credential/digest missing", name)
					}
					wanted := deployment(target, name, svc, 0)
					if err := client.prepareRegistryCredential(context.Background(), target, name, svc, wanted); err != nil || len(wanted.Spec.Template.Spec.ImagePullSecrets) != 1 {
						t.Fatal("selected credential not delivered to kubelet", err)
					}
				}
				if target.Spec.Services["api"].RegistryCredential != selected {
					t.Fatal("desired automatic selection mutated")
				}
			})
		}
	}
}

func TestRegistryAutomaticDoesNotRetryTransientErrors(t *testing.T) {
	for _, tokenService := range []bool{false, true} {
		calls := 0
		c := &Client{options: Options{RegistryCredentialNames: func(context.Context, string, string, string) ([]string, error) {
			t.Fatal("transient failure tried other secrets")
			return nil, nil
		}}, http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if tokenService && calls == 1 {
				return &http.Response{StatusCode: 401, Header: http.Header{"Www-Authenticate": {`Bearer realm="https://ghcr.io/token"`}}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}}
		if _, _, err := c.resolveWithCredentials(context.Background(), "ghcr.io/team/image:latest", []string{"amd64"}, "project", "env", ""); err == nil || errors.Is(err, errRegistryAccess) {
			t.Fatal("transient error misclassified", err)
		}
	}
}
