package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func retiredSecretFixture(extra ...runtime.Object) (*Client, Target, *corev1.Secret) {
	t := Target{ApplicationID: "app", Spec: spec.Application{Name: "app", Services: map[string]spec.Service{"current": {}}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "retired-environment", Namespace: Namespace(t.ApplicationID), UID: types.UID("secret-uid"), ResourceVersion: "7", Labels: labelsFor(t, "retired")}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"DATABASE_URL": []byte("private-fixture-credential")}}
	objects := []runtime.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(t.ApplicationID), Labels: labelsFor(t, "")}}, secret}
	objects = append(objects, extra...)
	return &Client{kube: fake.NewClientset(objects...)}, t, secret
}
func assertRetiredSecretExists(t *testing.T, c *Client, target Target) {
	t.Helper()
	if _, err := c.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(context.Background(), "retired-environment", metav1.GetOptions{}); err != nil {
		t.Fatal("required credential was removed", err)
	}
}

func TestRetiredEnvironmentSecretCleanupPreservesCurrentAndForeign(t *testing.T) {
	c, target, old := retiredSecretFixture()
	for _, service := range []string{"current", "foreign", "retired-file"} {
		s := old.DeepCopy()
		s.Name = service + "-environment"
		s.Labels = labelsFor(target, service)
		if service == "foreign" {
			s.Labels[ownerKey] = "unrelated"
		}
		if service == "retired-file" {
			s.Labels[fileObjectLabel] = "true"
		}
		if _, err := c.kube.CoreV1().Secrets(s.Namespace).Create(context.Background(), s, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	var deletes int
	c.kube.(*fake.Clientset).PrependReactor("delete", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
		action := a.(ktesting.DeleteAction)
		p := action.GetDeleteOptions().Preconditions
		if action.GetName() != old.Name || p == nil || p.UID == nil || *p.UID != old.UID || p.ResourceVersion == nil || *p.ResourceVersion != old.ResourceVersion {
			t.Fatal("credential deletion lacked exact identity preconditions")
		}
		deletes++
		return false, nil, nil
	})
	if err := c.cleanupRetiredSecrets(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if deletes != 1 {
		t.Fatal("retired orphan was not removed", deletes)
	}
	for _, name := range []string{"current-environment", "foreign-environment", "retired-file-environment"} {
		if _, err := c.kube.CoreV1().Secrets(old.Namespace).Get(context.Background(), name, metav1.GetOptions{}); err != nil {
			t.Fatal("retained secret was removed", name, err)
		}
	}
	if err := c.cleanupRetiredSecrets(context.Background(), target); err != nil {
		t.Fatal("cleanup replay failed", err)
	}
}

func TestRetiredEnvironmentSecretKeepsWorkloadConsumers(t *testing.T) {
	meta := metav1.ObjectMeta{Name: "retired", Namespace: Namespace("app"), Labels: map[string]string{serviceKey: "retired"}}
	template := corev1.PodTemplateSpec{ObjectMeta: meta}
	consumers := map[string]runtime.Object{
		"pod": &corev1.Pod{ObjectMeta: meta}, "deployment": &appsv1.Deployment{ObjectMeta: meta, Spec: appsv1.DeploymentSpec{Template: template}},
		"statefulset": &appsv1.StatefulSet{ObjectMeta: meta}, "replicaset": &appsv1.ReplicaSet{ObjectMeta: meta}, "daemonset": &appsv1.DaemonSet{ObjectMeta: meta},
		"job": &batchv1.Job{ObjectMeta: meta}, "cronjob": &batchv1.CronJob{ObjectMeta: meta}, "replicationcontroller": &corev1.ReplicationController{ObjectMeta: meta, Spec: corev1.ReplicationControllerSpec{Template: &template}},
		"foreign-reference": &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: meta.Namespace}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "foreign", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "retired-environment"}}}}}}}},
		"serviceaccount":    &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: meta.Namespace}, ImagePullSecrets: []corev1.LocalObjectReference{{Name: "retired-environment"}}},
	}
	for name, object := range consumers {
		t.Run(name, func(t *testing.T) {
			c, target, _ := retiredSecretFixture(object)
			if err := c.cleanupRetiredSecrets(context.Background(), target); err == nil {
				t.Fatal("consumer did not block deletion")
			}
			assertRetiredSecretExists(t, c, target)
		})
	}
}

func TestRetiredEnvironmentSecretRejectsIncompleteAuthorityAndInventory(t *testing.T) {
	for _, kind := range []string{"namespace", "identity", "cancelled", "partial-list", "conflict"} {
		t.Run(kind, func(t *testing.T) {
			c, target, s := retiredSecretFixture()
			k := c.kube.(*fake.Clientset)
			switch kind {
			case "namespace":
				n, _ := k.CoreV1().Namespaces().Get(context.Background(), s.Namespace, metav1.GetOptions{})
				n.Labels[ownerKey] = "other"
				_, _ = k.CoreV1().Namespaces().Update(context.Background(), n, metav1.UpdateOptions{})
			case "identity":
				s.UID = ""
				_, _ = k.CoreV1().Secrets(s.Namespace).Update(context.Background(), s, metav1.UpdateOptions{})
			case "cancelled":
				target.BeforeStep = func(context.Context) error { return context.Canceled }
			case "partial-list":
				k.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, &batchv1.JobList{ListMeta: metav1.ListMeta{Continue: "next"}}, nil
				})
			case "conflict":
				k.PrependReactor("delete", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, s.Name, errors.New("rotated"))
				})
			}
			if err := c.cleanupRetiredSecrets(context.Background(), target); err == nil {
				t.Fatal("unsafe retirement accepted")
			}
			assertRetiredSecretExists(t, c, target)
		})
	}
}

func TestRetiredEnvironmentSecretWaitsForTerminatingConsumers(t *testing.T) {
	c, target, _ := retiredSecretFixture()
	calls := 0
	c.kube.(*fake.Clientset).PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls > 1 {
			return true, &corev1.PodList{}, nil
		}
		stamp := metav1.Now()
		return true, &corev1.PodList{Items: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "retired", DeletionTimestamp: &stamp}}}}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.cleanupRetiredSecrets(ctx, target); err != nil || calls < 2 {
		t.Fatal("terminating consumer was not awaited", calls, err)
	}
}

func TestRetiredSecretReferencesIncludeInitEphemeralAndProjected(t *testing.T) {
	name := "retired-environment"
	env := []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: "value"}}}}
	for _, pod := range []corev1.PodSpec{
		{InitContainers: []corev1.Container{{Env: env}}},
		{EphemeralContainers: []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Env: env}}}},
		{Volumes: []corev1.Volume{{VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}}}}}}},
		{Volumes: []corev1.Volume{{VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{NodePublishSecretRef: &corev1.LocalObjectReference{Name: name}}}}}},
	} {
		if !podReferencesRetiredSecret(pod, name) {
			t.Fatal("secret consumer was missed")
		}
	}
}
