package cluster

import (
	"bytes"
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func TestMyDuckApplyCreatesCredentialsAcceptedByNativeIdentity(t *testing.T) {
	ctx := context.Background()
	d := myduckFixture()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "myduck-namespace", Labels: databaseLabels(d)}}
	c := &Client{kube: kubefake.NewClientset(ns), dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}
	password := bytes.Repeat([]byte("a"), 64)
	if err := c.ApplyDatabase(ctx, d, password, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	// The fake API does not assign the workload UID that Kubernetes supplies.
	workloads := c.dynamic.Resource(myduckDatabaseResource).Namespace(ns.Name)
	workload, err := workloads.Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	workload.SetUID("myduck-statefulset")
	if _, err = workloads.Update(ctx, workload, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	actual, identity, err := c.myduckClientIdentity(ctx, d)
	if err != nil || !bytes.Equal(actual, password) || identity == nil {
		t.Fatal("provisioned credential was rejected by the native identity reader", err)
	}
	if err = c.ApplyDatabase(ctx, d, password, func() error { return nil }); err != nil {
		t.Fatal("owned credential was rejected on retry", err)
	}
	secret, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*corev1.Secret){
		"missing owner":     func(s *corev1.Secret) { s.OwnerReferences = nil },
		"foreign namespace": func(s *corev1.Secret) { s.OwnerReferences[0].UID = "replacement" },
		"additional owner": func(s *corev1.Secret) {
			s.OwnerReferences = append(s.OwnerReferences, metav1.OwnerReference{APIVersion: "v1", Kind: "Secret", Name: "other", UID: "other"})
		},
		"mutable":        func(s *corev1.Secret) { s.Immutable = ptr(false) },
		"wrong type":     func(s *corev1.Secret) { s.Type = corev1.SecretTypeOpaque },
		"extra data":     func(s *corev1.Secret) { s.Data["extra"] = []byte("fixture") },
		"wrong username": func(s *corev1.Secret) { s.Data["username"] = []byte("other") },
		"wrong scope":    func(s *corev1.Secret) { s.Labels["hakopod.io/project"] = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := secret.DeepCopy()
			mutate(changed)
			if _, err := c.kube.CoreV1().Secrets(ns.Name).Update(ctx, changed, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			actions := len(c.kube.(*kubefake.Clientset).Actions())
			if err := c.ApplyDatabase(ctx, d, password, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "credential") {
				t.Fatal("unsafe existing credential was accepted", err)
			}
			for _, action := range c.kube.(*kubefake.Clientset).Actions()[actions:] {
				if action.GetVerb() != "get" {
					t.Fatal("unsafe existing credential allowed a runtime mutation")
				}
			}
			if _, _, err := c.myduckClientIdentity(ctx, d); err == nil {
				t.Fatal("unsafe credential was consumed by native health")
			}
		})
	}
}

func TestMyDuckCredentialCreationRechecksNamespaceAfterAuthorityFence(t *testing.T) {
	ctx := context.Background()
	d := myduckFixture()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "myduck-namespace", Labels: databaseLabels(d)}}
	c := &Client{kube: kubefake.NewClientset(ns), dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}
	err := c.ApplyDatabase(ctx, d, bytes.Repeat([]byte("a"), 64), func() error {
		changed := ns.DeepCopy()
		changed.UID = "replacement"
		_, err := c.kube.CoreV1().Namespaces().Update(ctx, changed, metav1.UpdateOptions{})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "namespace changed before creation") {
		t.Fatal("replaced namespace accepted", err)
	}
	for _, action := range c.kube.(*kubefake.Clientset).Actions() {
		if action.GetVerb() == "create" {
			t.Fatal("replaced namespace received a runtime resource")
		}
	}
}
