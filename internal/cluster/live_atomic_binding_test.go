package cluster

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveAtomicDatabaseBindingAndTrustRotation(t *testing.T) {
	if os.Getenv("HAKOPOD_ATOMIC_BINDING_TEST") != "1" {
		t.Skip("requires the named development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing atomic binding acceptance outside k3d-hakopod-dev")
	}
	c, err := New(path, Options{RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	const (
		image      = "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a"
		databaseID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		variable   = "DB_CONNECTION_URI"
	)
	oldURL := "postgres://fixture:original@database:5432/app?sslmode=verify-full"
	newURL := "postgres://fixture:rotated@database:5432/app?sslmode=verify-full"
	oldCA, newCA := trustFixtureCA(t), trustFixtureCA(t)
	currentURL, currentCA := oldURL, oldCA
	c.SetDatabaseBindingResolver(func(context.Context, string, string, spec.Application) (map[string]map[string]DatabaseConnection, error) {
		return map[string]map[string]DatabaseConnection{"api": {variable: {URL: currentURL, Port: 5432, CA: currentCA}}}, nil
	})

	service := spec.Service{
		Image: image, Command: []string{"python", "-B", "-c"}, Args: []string{"import time; time.sleep(1200)"},
		Bindings:               map[string]spec.Binding{variable: {ManagedDatabase: databaseID, Protocol: "postgres", Endpoint: "read_write"}},
		DatabaseClientProfiles: map[string]string{variable: spec.DatabaseClientInfisicalPostgresV1},
	}
	application, err := spec.Normalize(spec.Application{Name: "atomic-binding-development-fixture", Services: map[string]spec.Service{"api": service}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: fmt.Sprintf("atomic-binding-fixture-%d", time.Now().UnixNano()), Project: "binding-fixture", Environment: "development", OperationID: "binding-initial", Revision: 1, Spec: application}
	ns := Namespace(target.ApplicationID)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 150*time.Second)
		defer done()
		claims, e := c.kube.CoreV1().PersistentVolumeClaims(ns).List(cleanup, metav1.ListOptions{Limit: 2})
		if e != nil || claims.Continue != "" || len(claims.Items) != 0 {
			t.Error("fixture unexpectedly created persistent volume claims")
		}
		namespace, e := c.kube.CoreV1().Namespaces().Get(cleanup, ns, metav1.GetOptions{})
		if e == nil && owned(namespace, target) == nil {
			if e = c.kube.CoreV1().Namespaces().Delete(cleanup, ns, deleteOptions(namespace)); e != nil && !apierrors.IsNotFound(e) {
				t.Error("fixture namespace cleanup failed", e)
			}
		}
		for cleanup.Err() == nil {
			_, e = c.kube.CoreV1().Namespaces().Get(cleanup, ns, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				volumes, listErr := c.kube.CoreV1().PersistentVolumes().List(cleanup, metav1.ListOptions{Limit: 512})
				if listErr != nil || volumes.Continue != "" {
					t.Error("could not verify bounded fixture volume cleanup")
					return
				}
				retained := false
				for _, volume := range volumes.Items {
					retained = retained || volume.Spec.ClaimRef != nil && volume.Spec.ClaimRef.Namespace == ns
				}
				if !retained {
					t.Log("fixture namespace removed; no fixture PVC or PV remains")
					return
				}
			}
			_ = sleepContext(cleanup, time.Second)
		}
		t.Error("fixture namespace or persistent volumes did not finish cleanup")
	})
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}

	verifyLoaded := func(wantURL, wantCA string) {
		t.Helper()
		code := `import base64,os,sys
root=base64.b64decode(os.environ['DB_ROOT_CERT']).decode()
mounted=open(sys.argv[3]).read()
assert os.environ['DB_CONNECTION_URI']==sys.argv[1]
assert root==sys.argv[2]
assert mounted==sys.argv[2]`
		command := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", ns, "exec", "deployment/api", "--", "python", "-B", "-c", code, wantURL, wantCA, DatabaseTrustPath(databaseID))
		if err := command.Run(); err != nil {
			t.Fatal("the running fixture did not load one matching URL and CA generation")
		}
	}
	verifyLoaded(oldURL, oldCA)

	canonical, err := c.kube.CoreV1().Secrets(ns).Get(ctx, "api-environment", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	originalCanonical := canonical.Data
	if decoded, e := base64.StdEncoding.DecodeString(string(originalCanonical["DB_ROOT_CERT"])); e != nil || string(decoded) != oldCA || string(originalCanonical[variable]) != oldURL {
		t.Fatal("initial canonical environment does not contain one matching URL and CA generation")
	}
	dep, err := c.kube.AppsV1().Deployments(ns).Get(ctx, "api", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range dep.Spec.Template.Spec.Containers[0].Env {
		ref := dep.Spec.Template.Spec.Containers[0].Env[i].ValueFrom
		if ref != nil && ref.SecretKeyRef != nil {
			ref.SecretKeyRef.Name = canonical.Name
		}
	}
	delete(dep.Spec.Template.Annotations, environmentSnapshotLabel)
	legacy, err := c.kube.AppsV1().Deployments(ns).Update(ctx, dep, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.waitReady(ctx, target, "api", legacy.Generation); err != nil {
		t.Fatal(err)
	}
	legacy, err = c.kube.AppsV1().Deployments(ns).Get(ctx, "api", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	legacyGeneration := legacy.Generation
	legacyTemplate := legacy.Spec.Template.DeepCopy()
	zero := int32(0)
	jobTemplate := legacyTemplate.DeepCopy()
	jobTemplate.Spec.RestartPolicy = "Never"
	retainedJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "retained-legacy", Namespace: ns, Labels: labelsFor(target, "api")},
		Spec:       batchv1.JobSpec{Parallelism: &zero, Template: *jobTemplate},
	}
	retainedJob, err = c.kube.BatchV1().Jobs(ns).Create(ctx, retainedJob, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	replicas, err := c.kube.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{LabelSelector: serviceKey + "=api", Limit: 10})
	if err != nil || replicas.Continue != "" {
		t.Fatal("legacy ReplicaSet inventory unavailable", err)
	}
	var retainedReplica *appsv1.ReplicaSet
	for i := range replicas.Items {
		usesCanonical := true
		for _, environment := range replicas.Items[i].Spec.Template.Spec.Containers[0].Env {
			if environment.ValueFrom != nil && environment.ValueFrom.SecretKeyRef != nil && environment.ValueFrom.SecretKeyRef.Name != canonical.Name {
				usesCanonical = false
			}
		}
		if usesCanonical {
			retainedReplica = replicas.Items[i].DeepCopy()
			break
		}
	}
	if retainedReplica == nil {
		t.Fatal("legacy ReplicaSet was not retained")
	}

	currentURL, currentCA = newURL, newCA
	if err = c.RefreshDatabaseBindings(ctx, target, nil, "api"); err != nil {
		t.Fatal(err)
	}
	updated, err := c.kube.AppsV1().Deployments(ns).Get(ctx, "api", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Generation != legacyGeneration+1 {
		t.Fatalf("expected one atomic Deployment template update, generation changed from %d to %d", legacyGeneration, updated.Generation)
	}
	if err = c.waitReady(ctx, target, "api", updated.Generation); err != nil {
		t.Fatal(err)
	}
	verifyLoaded(newURL, newCA)

	keptReplica, err := c.kube.AppsV1().ReplicaSets(ns).Get(ctx, retainedReplica.Name, metav1.GetOptions{})
	if err != nil || keptReplica.UID != retainedReplica.UID || !reflect.DeepEqual(keptReplica.Spec.Template, retainedReplica.Spec.Template) {
		t.Fatal("refresh changed the retained legacy ReplicaSet references", err)
	}
	keptJob, err := c.kube.BatchV1().Jobs(ns).Get(ctx, retainedJob.Name, metav1.GetOptions{})
	if err != nil || keptJob.UID != retainedJob.UID || !reflect.DeepEqual(keptJob.Spec.Template, retainedJob.Spec.Template) {
		t.Fatal("refresh changed the retained legacy Job references", err)
	}
	keptCanonical, err := c.kube.CoreV1().Secrets(ns).Get(ctx, canonical.Name, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(keptCanonical.Data, originalCanonical) {
		t.Fatal("refresh changed the canonical Secret used by retained workloads", err)
	}
	t.Log("The replacement pod loaded one matching URL and CA generation while retained workloads kept their original references.")
}
