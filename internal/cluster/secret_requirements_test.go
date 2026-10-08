package cluster

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
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
	kube.PrependReactor("list", "secrets", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("offline") })
	if missing, err = c.MissingWorkloadSecrets(ctx, "one", "main", app); err == nil || missing != nil {
		t.Fatal("outage reported as missing", missing, err)
	}
}

func TestMissingSecretsFullApplicationUsesBoundedScopedRead(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: PlatformNamespace, Labels: map[string]string{managedBy: "hakopod"}}}
	objects := []runtime.Object{ns}
	refs := map[string]spec.SecretRef{}
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("token-%03d", i)
		refs[fmt.Sprintf("TOKEN%d", i)] = spec.SecretRef{Ref: name}
		objects = append(objects, preflightSecret("one", name))
	}
	// Another application's secrets must not consume the selected scope's limit.
	objects = append(objects, preflightSecret("another", "foreign"))
	kube := fake.NewClientset(objects...)
	c := &Client{kube: kube}
	app := spec.Application{Name: "app", Services: map[string]spec.Service{"web": {Secrets: refs}}}
	missing, err := c.MissingWorkloadSecrets(context.Background(), "one", "main", app)
	if err != nil || len(missing) != 0 {
		t.Fatalf("complete application rejected: %v %v", missing, err)
	}
	actions := kube.Actions()
	if len(actions) != 2 || !actions[0].Matches("get", "namespaces") || !actions[1].Matches("list", "secrets") {
		t.Fatalf("preflight made %d backend calls; want namespace GET and scoped LIST", len(actions))
	}
	list := actions[1].(ktesting.ListAction)
	if list.GetListRestrictions().Labels.String() != "hakopod.io/secret-scope="+secretScope("one", "main", "app") {
		t.Fatal("preflight did not restrict its list to the application")
	}
	if actions[1].(interface{ GetListOptions() metav1.ListOptions }).GetListOptions().Limit != 101 {
		t.Fatal("preflight did not bound the list size")
	}
}

func preflightSecret(project, name string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: workloadSecretName(project, "main", "app", name), Namespace: PlatformNamespace,
		Labels: map[string]string{managedBy: "hakopod", platformSecretLabel: "true", "hakopod.io/secret-scope": secretScope(project, "main", "app"), "hakopod.io/secret-name": name},
	}, Data: map[string][]byte{"value": []byte("private-test-value")}}
}

func TestMissingSecretsOwnershipAndEmptyValues(t *testing.T) {
	for _, label := range []string{managedBy, platformSecretLabel, "hakopod.io/secret-scope", "hakopod.io/secret-name"} {
		t.Run(label, func(t *testing.T) {
			secret := preflightSecret("one", "token")
			secret.Labels[label] = "foreign"
			kube := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: PlatformNamespace, Labels: map[string]string{managedBy: "hakopod"}}}, secret)
			c := &Client{kube: kube}
			app := spec.Application{Name: "app", Services: map[string]spec.Service{"web": {Secrets: map[string]spec.SecretRef{"TOKEN": {Ref: "token"}}}}}
			missing, err := c.MissingWorkloadSecrets(context.Background(), "one", "main", app)
			if err == nil || missing != nil || strings.Contains(err.Error(), "private-test-value") {
				t.Fatal("foreign secret did not fail closed with a redacted error")
			}
		})
	}
	secret := preflightSecret("one", "token")
	secret.Data = nil
	kube := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: PlatformNamespace, Labels: map[string]string{managedBy: "hakopod"}}}, secret)
	c := &Client{kube: kube}
	app := spec.Application{Name: "app", Services: map[string]spec.Service{"web": {Secrets: map[string]spec.SecretRef{"TOKEN": {Ref: "token"}}}}}
	missing, err := c.MissingWorkloadSecrets(context.Background(), "one", "main", app)
	if err != nil || !reflect.DeepEqual(missing, []string{"token"}) {
		t.Fatal("empty value was not reported missing")
	}
	kube.PrependReactor("list", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.SecretList{ListMeta: metav1.ListMeta{Continue: "next-page"}}, nil
	})
	if missing, err := c.MissingWorkloadSecrets(context.Background(), "one", "main", app); err == nil || missing != nil {
		t.Fatal("partial list was accepted")
	}
	kube.PrependReactor("list", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		items := make([]corev1.Secret, 101)
		for i := range items {
			items[i] = *preflightSecret("one", fmt.Sprintf("token-%03d", i))
		}
		return true, &corev1.SecretList{Items: items}, nil
	})
	if missing, err := c.MissingWorkloadSecrets(context.Background(), "one", "main", app); err == nil || missing != nil {
		t.Fatal("oversized list was accepted")
	}
}
