package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestExternalSecretSnapshotFailurePreservesEveryLastGoodSecret(t *testing.T) {
	ctx := context.Background()
	target := testTarget(t)
	service := target.Spec.Services["api"]
	service.Secrets = map[string]spec.SecretRef{"PASSWORD": {Provider: "company", Path: "shop", Key: "password"}}
	target.Spec.Services["api"] = service
	kube := fake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "api-environment", Namespace: Namespace(target.ApplicationID), Labels: labelsFor(target, "api")}, Data: map[string][]byte{"PASSWORD": []byte("last-good")}})
	called := false
	client := &Client{kube: kube, options: Options{AppDomain: "example.test", ExternalSecrets: func(context.Context, string, string, spec.Application) (map[string]map[string][]byte, error) {
		called = true
		return nil, errors.New("sensitive upstream failure")
	}}}
	_, err := client.Deploy(ctx, target, nil)
	if err == nil || !called || strings.Contains(err.Error(), "sensitive upstream failure") {
		t.Fatal("failed provider accepted")
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatalf("failed snapshot mutated Kubernetes: %s", action.GetVerb())
		}
	}
	secret, err := kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, "api-environment", metav1.GetOptions{})
	if err != nil || string(secret.Data["PASSWORD"]) != "last-good" {
		t.Fatal("last-good secret overwritten")
	}
	if target.secretValues != nil {
		t.Fatal("failed snapshot attached partial values")
	}
}

func TestExternalSnapshotNativeIsolationOwnershipAndRefresh(t *testing.T) {
	ctx := context.Background()
	target := testTarget(t)
	service := target.Spec.Services["api"]
	service.Secrets = map[string]spec.SecretRef{"PASSWORD": {Provider: "company", Key: "password"}, "LOCAL": {Ref: "native"}}
	target.Spec.Services["api"] = service
	kube := fake.NewClientset()
	value := "first"
	client := &Client{kube: kube, options: Options{ExternalSecrets: func(_ context.Context, p, e string, app spec.Application) (map[string]map[string][]byte, error) {
		if p != target.Project || e != target.Environment {
			t.Error("scope lost")
		}
		return map[string]map[string][]byte{"api": {"PASSWORD": []byte(value)}}, nil
	}}}
	if err := client.PutWorkloadSecret(ctx, target.Project, target.Environment, target.Spec.Name, "native", "native-value"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first", "rotated"} {
		value = want
		if err := client.snapshotWorkloadSecrets(ctx, &target); err != nil {
			t.Fatal(err)
		}
		if err := client.prepareWorkloadSecrets(ctx, target, "api", service); err != nil {
			t.Fatal(err)
		}
		stored, err := kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, "api-environment", metav1.GetOptions{})
		if err != nil || string(stored.Data["PASSWORD"]) != want || string(stored.Data["LOCAL"]) != "native-value" {
			t.Fatal("snapshot or native reference changed")
		}
	}
	stored, _ := kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, "api-environment", metav1.GetOptions{})
	stored.Labels = map[string]string{}
	if _, err := kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Update(ctx, stored, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := client.prepareWorkloadSecrets(ctx, target, "api", service); err == nil {
		t.Fatal("external secret overwrote foreign resource")
	}
}
