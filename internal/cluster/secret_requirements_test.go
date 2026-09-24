package cluster

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestMissingSecretsScopedAndUnavailable(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: PlatformNamespace, Labels: map[string]string{managedBy: "hakopod"}}})
	c := &Client{kube: kube}
	app := spec.Application{Name: "app", Services: map[string]spec.Service{"web": {Secrets: map[string]spec.SecretRef{"TOKEN": {Ref: "token"}}}}}
	missing, err := c.MissingWorkloadSecrets(ctx, "one", "main", app)
	if err != nil || !reflect.DeepEqual(missing, []string{"token"}) {
		t.Fatalf("missing %v: %v", missing, err)
	}
	if err = c.CreateWorkloadSecret(ctx, "one", "main", "app", "token", "private-value"); err != nil {
		t.Fatal(err)
	}
	if err = c.ValidateWorkloadSecrets(ctx, "one", "main", app); err != nil {
		t.Fatal(err)
	}
	var missingErr *spec.MissingSecretsError
	if err = c.ValidateWorkloadSecrets(ctx, "another", "main", app); !errors.As(err, &missingErr) {
		t.Fatal("foreign scope reused a secret", err)
	}
	kube.PrependReactor("get", "secrets", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("offline") })
	if missing, err = c.MissingWorkloadSecrets(ctx, "one", "main", app); err == nil || missing != nil {
		t.Fatal("outage reported as missing", missing, err)
	}
}
