package cluster

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveDeploymentSecretSetup(t *testing.T) {
	if os.Getenv("HAKOPOD_SECRET_SETUP_TEST") != "1" {
		t.Skip("opt-in missing secret acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev context")
	}
	c, err := New(path, Options{RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	app, err := spec.Normalize(spec.Application{Name: fmt.Sprintf("secret-setup-%d", time.Now().UnixNano()), Services: map[string]spec.Service{
		"verify": {Image: "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a", Job: &spec.Job{TimeoutSeconds: 90}, Command: []string{"python", "-c"}, Args: []string{"import os; assert os.environ['PASSWORD'] == 'fixture-value'; assert open('/app/key').read() == 'fixture-file'"}, Secrets: map[string]spec.SecretRef{"PASSWORD": {Ref: "password"}}, Files: map[string]spec.File{"key": {MountPath: "/app/key", Secret: &spec.SecretRef{Ref: "file-key"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Project: "secret-acceptance", Environment: "test", ApplicationID: app.Name, OperationID: "secret-setup", Revision: 1, Spec: app}
	defer func() {
		clean, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		ns, e := c.kube.CoreV1().Namespaces().Get(clean, Namespace(target.ApplicationID), metav1.GetOptions{})
		if e == nil {
			if e = owned(ns, target); e != nil {
				t.Error(e)
				return
			}
			if e = c.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(ns)); e != nil {
				t.Error(e)
			}
		} else if !apierrors.IsNotFound(e) {
			t.Error(e)
		}
		for _, name := range []string{"password", "file-key"} {
			if e = c.DeleteWorkloadSecret(clean, target.Project, target.Environment, app.Name, name); e != nil {
				t.Error(e)
			}
		}
	}()
	missing, err := c.MissingWorkloadSecrets(ctx, target.Project, target.Environment, app)
	if err != nil || !reflect.DeepEqual(missing, []string{"file-key", "password"}) {
		t.Fatalf("missing secret names were not complete: %v, %v", missing, err)
	}
	var required *spec.MissingSecretsError
	if err = c.ValidateWorkloadSecrets(ctx, target.Project, target.Environment, app); !errors.As(err, &required) {
		t.Fatal("missing secrets passed acceptance")
	}
	if _, err = c.Deploy(ctx, target, nil); err == nil {
		t.Fatal("missing secrets reached deployment")
	}
	if _, err = c.kube.CoreV1().Namespaces().Get(ctx, Namespace(target.ApplicationID), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("missing secrets mutated runtime", err)
	}
	for name, value := range map[string]string{"password": "fixture-value", "file-key": "fixture-file"} {
		if err = c.CreateWorkloadSecret(ctx, target.Project, target.Environment, app.Name, name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err = c.CreateWorkloadSecret(ctx, target.Project, target.Environment, app.Name, "password", "replacement"); !apierrors.IsAlreadyExists(err) {
		t.Fatal("duplicate create did not preserve the existing credential", err)
	}
	if err = c.ValidateWorkloadSecrets(ctx, "different-project", target.Environment, app); !errors.As(err, &required) {
		t.Fatal("another project could use this application's secrets", err)
	}
	if err = c.ValidateWorkloadSecrets(ctx, target.Project, target.Environment, app); err != nil {
		t.Fatal(err)
	}
	result, err := c.Deploy(ctx, target, nil)
	if err != nil || result.Status != "healthy" {
		t.Fatalf("saved values did not reach the real job: %s, %v", result.Status, err)
	}
	t.Log("Missing values blocked runtime mutation; scoped creation preserved credentials; the real job verified environment and file values")
}
