package cluster

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
	"strings"
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

func TestProvisionedWorkloadSecretRejectsRacedWriterAndPermitsExactReplay(t *testing.T) {
	ctx := context.Background()
	client := &Client{kube: fake.NewClientset()}
	target := testTarget(t)
	operationID := strings.Repeat("a", 32)

	if err := client.PutWorkloadSecret(ctx, target.Project, target.Environment, target.Spec.Name, "raced-password", "foreign-value"); err != nil {
		t.Fatal(err)
	}
	if err := client.PutProvisionedWorkloadSecret(ctx, target.Project, target.Environment, target.Spec.Name, "raced-password", "generated-value", operationID); !errors.Is(err, ErrProvisionedWorkloadSecretConflict) {
		t.Fatal("provisioning overwrote a secret created after review")
	}
	raced, err := client.GetPlatformSecret(ctx, workloadSecretName(target.Project, target.Environment, target.Spec.Name, "raced-password"))
	if err != nil || string(raced.Data["value"]) != "foreign-value" {
		t.Fatalf("raced secret value changed: %v", err)
	}

	if err = client.PutProvisionedWorkloadSecret(ctx, target.Project, target.Environment, target.Spec.Name, "owned-password", "generated-value", operationID); err != nil {
		t.Fatal(err)
	}
	if err = client.PutProvisionedWorkloadSecret(ctx, target.Project, target.Environment, target.Spec.Name, "owned-password", "generated-value", operationID); err != nil {
		t.Fatalf("exact provisioning retry failed: %v", err)
	}
	if err = client.PutProvisionedWorkloadSecret(ctx, target.Project, target.Environment, target.Spec.Name, "owned-password", "changed-value", operationID); !errors.Is(err, ErrProvisionedWorkloadSecretConflict) {
		t.Fatal("same operation replaced its reviewed credential")
	}
	if err = client.PutProvisionedWorkloadSecret(ctx, target.Project, target.Environment, target.Spec.Name, "owned-password", "generated-value", strings.Repeat("b", 32)); !errors.Is(err, ErrProvisionedWorkloadSecretConflict) {
		t.Fatal("another operation reused the owned provisioning secret")
	}

	raceKube := fake.NewClientset()
	raceClient := &Client{kube: raceKube}
	raceKube.PrependReactor("create", "secrets", func(action kubetesting.Action) (bool, runtime.Object, error) {
		created := action.(kubetesting.CreateAction).GetObject().(*corev1.Secret)
		if created.Name != workloadSecretName(target.Project, target.Environment, target.Spec.Name, "concurrent-password") {
			return false, nil, nil
		}
		foreign := created.DeepCopy()
		foreign.Annotations = map[string]string{"hakopod.io/updated-at": time.Now().UTC().Format(time.RFC3339)}
		foreign.Data = map[string][]byte{"value": []byte("concurrent-value")}
		if err := raceKube.Tracker().Create(corev1.SchemeGroupVersion.WithResource("secrets"), foreign, PlatformNamespace); err != nil {
			t.Fatal(err)
		}
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, created.Name)
	})
	if err = raceClient.PutProvisionedWorkloadSecret(ctx, target.Project, target.Environment, target.Spec.Name, "concurrent-password", "generated-value", operationID); !errors.Is(err, ErrProvisionedWorkloadSecretConflict) {
		t.Fatal("provisioning overwrote a secret created between review and create")
	}
	concurrent, err := raceClient.GetPlatformSecret(ctx, workloadSecretName(target.Project, target.Environment, target.Spec.Name, "concurrent-password"))
	if err != nil || string(concurrent.Data["value"]) != "concurrent-value" {
		t.Fatalf("concurrent secret value changed: %v", err)
	}
}
