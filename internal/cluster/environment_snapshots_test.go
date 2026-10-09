package cluster

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func environmentSnapshotFixture(t *testing.T) (*Client, Target, *corev1.Secret, *appsv1.Deployment) {
	t.Helper()
	target := testTarget(t)
	svc := target.Spec.Services["api"]
	svc.Bindings = map[string]spec.Binding{"DATABASE_URL": {ManagedDatabase: strings.Repeat("a", 32), Protocol: "postgres", Endpoint: "read_write"}}
	svc.Secrets = map[string]spec.SecretRef{"TOKEN": {Ref: "token"}}
	target.Spec.Services["api"] = svc
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "api-environment", Namespace: Namespace(target.ApplicationID), Labels: labelsFor(target, "api")}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"DATABASE_URL": []byte("postgres://app:fixture@database:5432/app?sslmode=verify-full"), "TOKEN": []byte("fixture-token")}}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: secret.Namespace, Labels: labelsFor(target, "")}}
	return &Client{kube: fake.NewClientset(namespace, secret)}, target, secret, deployment(target, "api", svc, time.Minute)
}

func TestEnvironmentSnapshotPinsEveryValueAndReusesIdenticalContent(t *testing.T) {
	c, target, canonical, dep := environmentSnapshotFixture(t)
	ctx := context.Background()
	changed, err := c.pinWorkloadEnvironment(ctx, target, "api", &dep.Spec.Template)
	if err != nil || !changed {
		t.Fatal("initial pin failed", changed, err)
	}
	snapshot := dep.Spec.Template.Annotations[environmentSnapshotLabel]
	secret, err := c.kube.CoreV1().Secrets(canonical.Namespace).Get(ctx, snapshot, metav1.GetOptions{})
	if err != nil || environmentSnapshotOwned(secret, target, "api") != nil || !reflect.DeepEqual(secret.Data, canonical.Data) {
		t.Fatal("snapshot did not preserve the resolved environment")
	}
	for _, env := range dep.Spec.Template.Spec.Containers[0].Env {
		if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.Name != snapshot {
			t.Fatal("a managed environment value still uses a mutable reference")
		}
	}
	before := dep.Spec.Template.DeepCopy()
	changed, err = c.pinWorkloadEnvironment(ctx, target, "api", &dep.Spec.Template)
	if err != nil || changed || !reflect.DeepEqual(before, &dep.Spec.Template) {
		t.Fatal("identical values caused another rollout", err)
	}
	// Keep the old workload, then change only resolved credentials. Its pod
	// template must stay pinned even though the canonical Secret changes.
	if _, err = c.kube.AppsV1().Deployments(canonical.Namespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	canonical.Data["DATABASE_URL"] = []byte("postgres://new:rotated@database:5432/restored?sslmode=verify-ca")
	if _, err = c.kube.CoreV1().Secrets(canonical.Namespace).Update(ctx, canonical, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	changed, err = c.pinWorkloadEnvironment(ctx, target, "api", &dep.Spec.Template)
	if err != nil || !changed || dep.Spec.Template.Annotations[environmentSnapshotLabel] == snapshot {
		t.Fatal("credential-only change did not change the template", err)
	}
	old, err := c.kube.CoreV1().Secrets(canonical.Namespace).Get(ctx, snapshot, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(old.Data, secret.Data) {
		t.Fatal("old workload lost its original environment")
	}
	other := target
	other.ApplicationID = "another-application"
	if environmentSnapshot(other, "api", canonical.Data).Name == environmentSnapshot(target, "api", canonical.Data).Name || environmentSnapshot(target, "worker", canonical.Data).Name == environmentSnapshot(target, "api", canonical.Data).Name {
		t.Fatal("snapshot identity is not scoped to its application and service")
	}
}

func TestEnvironmentSnapshotRejectsDriftBeforeAnyMutation(t *testing.T) {
	for _, kind := range []string{"canonical-owner", "canonical-service", "missing-key", "extra-key", "wrong-key", "optional-key", "duplicate-key", "foreign-reference", "forged-annotation", "bulk-reference", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			c, target, canonical, dep := environmentSnapshotFixture(t)
			ctx := context.Background()
			ref := dep.Spec.Template.Spec.Containers[0].Env[0].ValueFrom.SecretKeyRef
			switch kind {
			case "canonical-owner":
				canonical.Labels[ownerKey] = "foreign"
			case "canonical-service":
				canonical.Labels[serviceKey] = "worker"
			case "missing-key":
				delete(canonical.Data, "TOKEN")
			case "extra-key":
				canonical.Data["UNDECLARED"] = []byte("not-authorized")
			case "wrong-key":
				ref.Key = "OTHER"
			case "optional-key":
				ref.Optional = ptr(true)
			case "duplicate-key":
				dep.Spec.Template.Spec.Containers[0].Env = append(dep.Spec.Template.Spec.Containers[0].Env, dep.Spec.Template.Spec.Containers[0].Env[0])
			case "foreign-reference":
				ref.Name = "other-secret"
			case "forged-annotation":
				ref.Name = "other-secret"
				dep.Spec.Template.Annotations = map[string]string{environmentSnapshotLabel: ref.Name}
				foreign := canonical.DeepCopy()
				foreign.Name = ref.Name
				if _, err := c.kube.CoreV1().Secrets(canonical.Namespace).Create(ctx, foreign, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "bulk-reference":
				dep.Spec.Template.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: canonical.Name}}}}
			case "cancelled":
				target.BeforeStep = func(context.Context) error { return context.Canceled }
			}
			if _, err := c.kube.CoreV1().Secrets(canonical.Namespace).Update(ctx, canonical, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			before := dep.Spec.Template.DeepCopy()
			k := c.kube.(*fake.Clientset)
			k.ClearActions()
			if _, err := c.pinWorkloadEnvironment(ctx, target, "api", &dep.Spec.Template); err == nil {
				t.Fatal("invalid environment accepted")
			}
			if !reflect.DeepEqual(before, &dep.Spec.Template) {
				t.Fatal("failed pin modified the template")
			}
			for _, action := range k.Actions() {
				if action.GetVerb() == "create" || action.GetVerb() == "update" || action.GetVerb() == "delete" {
					t.Fatal("invalid pin mutated Kubernetes")
				}
			}
		})
	}
}

func TestEnvironmentSnapshotCleanupKeepsAllWorkloadReferences(t *testing.T) {
	for _, kind := range []string{"pod", "replicaset", "job", "cronjob", "statefulset", "daemonset", "controller", "serviceaccount"} {
		t.Run(kind, func(t *testing.T) {
			c, target, _, dep := environmentSnapshotFixture(t)
			ctx := context.Background()
			if _, err := c.pinWorkloadEnvironment(ctx, target, "api", &dep.Spec.Template); err != nil {
				t.Fatal(err)
			}
			name := dep.Spec.Template.Annotations[environmentSnapshotLabel]
			secret, _ := c.kube.CoreV1().Secrets(dep.Namespace).Get(ctx, name, metav1.GetOptions{})
			secret.UID, secret.ResourceVersion = "original-uid", "3"
			if _, err := c.kube.CoreV1().Secrets(dep.Namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			meta := metav1.ObjectMeta{Name: "retained", Namespace: dep.Namespace}
			var object runtime.Object
			switch kind {
			case "pod":
				meta.DeletionTimestamp = ptr(metav1.Now())
				object = &corev1.Pod{ObjectMeta: meta, Spec: dep.Spec.Template.Spec}
			case "replicaset":
				object = &appsv1.ReplicaSet{ObjectMeta: meta, Spec: appsv1.ReplicaSetSpec{Replicas: ptr(int32(0)), Template: dep.Spec.Template}}
			case "job":
				object = &batchv1.Job{ObjectMeta: meta, Spec: batchv1.JobSpec{Template: dep.Spec.Template}}
			case "cronjob":
				object = &batchv1.CronJob{ObjectMeta: meta, Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: dep.Spec.Template}}}}
			case "statefulset":
				object = &appsv1.StatefulSet{ObjectMeta: meta, Spec: appsv1.StatefulSetSpec{Template: dep.Spec.Template}}
			case "daemonset":
				object = &appsv1.DaemonSet{ObjectMeta: meta, Spec: appsv1.DaemonSetSpec{Template: dep.Spec.Template}}
			case "controller":
				object = &corev1.ReplicationController{ObjectMeta: meta, Spec: corev1.ReplicationControllerSpec{Template: &dep.Spec.Template}}
			case "serviceaccount":
				object = &corev1.ServiceAccount{ObjectMeta: meta, Secrets: []corev1.ObjectReference{{Name: name}}}
			}
			if err := c.kube.(*fake.Clientset).Tracker().Add(object); err != nil {
				t.Fatal(err)
			}
			if err := c.cleanupEnvironmentSnapshots(ctx, target, "future-snapshot"); err != nil {
				t.Fatal(err)
			}
			if _, err := c.kube.CoreV1().Secrets(dep.Namespace).Get(ctx, name, metav1.GetOptions{}); err != nil {
				t.Fatal("a retained workload lost its credentials", err)
			}
		})
	}
}

func TestEnvironmentSnapshotCleanupRequiresCompleteInventoryAndIdentity(t *testing.T) {
	for _, kind := range []string{"partial", "cancelled", "foreign", "delete"} {
		t.Run(kind, func(t *testing.T) {
			c, target, canonical, _ := environmentSnapshotFixture(t)
			ctx := context.Background()
			snapshot := environmentSnapshot(target, "api", canonical.Data)
			snapshot.UID, snapshot.ResourceVersion = types.UID("original-uid"), "8"
			if kind == "foreign" {
				snapshot.Immutable = ptr(false)
			}
			if _, err := c.kube.CoreV1().Secrets(canonical.Namespace).Create(ctx, snapshot, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			k := c.kube.(*fake.Clientset)
			if kind == "partial" {
				k.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, &batchv1.JobList{ListMeta: metav1.ListMeta{Continue: "next"}}, nil
				})
			}
			if kind == "cancelled" {
				target.BeforeStep = func(context.Context) error { return context.Canceled }
			}
			deleted := false
			k.PrependReactor("delete", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
				if kind != "delete" {
					t.Fatal("cleanup deleted a Secret before its checks passed")
				}
				options := action.(ktesting.DeleteAction).GetDeleteOptions()
				if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != snapshot.UID || options.Preconditions.ResourceVersion == nil || *options.Preconditions.ResourceVersion != snapshot.ResourceVersion {
					t.Fatal("cleanup did not fence the exact Secret")
				}
				deleted = true
				return false, nil, nil
			})
			err := c.cleanupEnvironmentSnapshots(ctx, target, "future-snapshot")
			if kind == "delete" {
				if err != nil || !deleted {
					t.Fatal("orphan snapshot was not removed", err)
				}
			} else if err == nil {
				t.Fatal("unsafe cleanup succeeded")
			}
		})
	}
}

func TestBindingRefreshVerifiesOnLaterPassWithoutChangingOtherWorkloads(t *testing.T) {
	c, target, canonical, dep := environmentSnapshotFixture(t)
	ctx := context.Background()
	c.options.DatabaseBindings = func(context.Context, string, string, spec.Application) (map[string]map[string]DatabaseConnection, error) {
		return map[string]map[string]DatabaseConnection{"api": {"DATABASE_URL": {URL: "postgres://app:rotated@database:5432/app", Port: 5432}}}, nil
	}
	if err := c.PutWorkloadSecret(ctx, target.Project, target.Environment, target.Spec.Name, "token", "fixture-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.pinWorkloadEnvironment(ctx, target, "api", &dep.Spec.Template); err != nil {
		t.Fatal(err)
	}
	dep.Generation = 2
	if _, err := c.kube.AppsV1().Deployments(canonical.Namespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	events := []Event{}
	emit := func(e Event) { events = append(events, e) }
	if err := c.RefreshDatabaseBindings(ctx, target, emit, "api"); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "binding_rollout" {
		t.Fatal("refresh reported loaded values before pod readiness", events)
	}
	updated, _ := c.kube.AppsV1().Deployments(canonical.Namespace).Get(ctx, "api", metav1.GetOptions{})
	if updated.Spec.Template.Annotations[environmentSnapshotLabel] == dep.Spec.Template.Annotations[environmentSnapshotLabel] || updated.Spec.Template.Spec.Containers[0].Image != dep.Spec.Template.Spec.Containers[0].Image || *updated.Spec.Replicas != *dep.Spec.Replicas {
		t.Fatal("refresh did not limit its changes to environment references")
	}
	if err := c.RefreshDatabaseBindings(ctx, target, emit, "api"); err != nil || len(events) != 1 {
		t.Fatal("pending rollout was incorrectly verified", err)
	}
	updated.Status = appsv1.DeploymentStatus{ObservedGeneration: updated.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
	if _, err := c.kube.AppsV1().Deployments(canonical.Namespace).UpdateStatus(ctx, updated, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-new", Namespace: canonical.Namespace, Labels: updated.Spec.Template.Labels, Annotations: updated.Spec.Template.Annotations}, Spec: updated.Spec.Template.Spec, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	if _, err := c.kube.CoreV1().Pods(canonical.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.RefreshDatabaseBindings(ctx, target, emit, "api"); err != nil || len(events) != 2 || events[1].Type != "binding_ready" {
		t.Fatal("ready replacement was not verified", err, events)
	}
	if err := c.RefreshDatabaseBindings(ctx, target, emit, "api"); err != nil || len(events) != 2 {
		t.Fatal("verification was not idempotent", err)
	}
	// The durable claim must fence every mutation, including maintenance.
	target.BeforeStep = func(context.Context) error { return errors.New("claim lost") }
	if err := c.RefreshDatabaseBindings(ctx, target, emit, "api"); err == nil {
		t.Fatal("maintenance continued after losing its claim")
	}
	if _, err := c.kube.CoreV1().Secrets(canonical.Namespace).Get(ctx, "api-environment", metav1.GetOptions{}); apierrors.IsNotFound(err) {
		t.Fatal("claim loss removed the current environment")
	}
}
