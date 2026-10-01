package cluster

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func vitessLegacyCredentialFixture() (database.Resource, *corev1.Namespace, *corev1.Secret, []byte) {
	d := database.Resource{ID: "0123456789abcdef0123456789abcdef", Project: "demo", Environment: "development", Spec: database.Spec{Engine: "vitess"}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
	password := bytes.Repeat([]byte("a"), 64)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "database-credentials", Namespace: ns.Name, UID: "credential-uid", ResourceVersion: "1", Labels: databaseLabels(d)}, Type: corev1.SecretTypeBasicAuth, Immutable: ptr(true), Data: map[string][]byte{"username": []byte("app"), "password": password}}
	return d, ns, secret, password
}

func TestVitessCredentialOwnershipBindsLegacySecretWithoutChangingData(t *testing.T) {
	d, ns, secret, password := vitessLegacyCredentialFixture()
	c := &Client{kube: kubefake.NewSimpleClientset(ns, secret)}
	before := 0
	err := c.reconcileVitessCredentialOwnership(context.Background(), d, ns, secret, password, func() error { before++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	bound, err := c.kube.CoreV1().Secrets(ns.Name).Get(context.Background(), secret.Name, metav1.GetOptions{})
	if err != nil || bound.UID != secret.UID || !reflect.DeepEqual(bound.Data, secret.Data) || before != 1 {
		t.Fatal("legacy binding changed credential identity", err)
	}
	if _, err = vitessPublicEndpointPassword(bound, d, ns.UID); err != nil {
		t.Fatal(err)
	}
	actions := len(c.kube.(*kubefake.Clientset).Actions())
	if err = c.reconcileVitessCredentialOwnership(context.Background(), d, ns, bound, password, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, action := range c.kube.(*kubefake.Clientset).Actions()[actions:] {
		if action.GetVerb() == "update" {
			t.Fatal("owned credential was updated again")
		}
	}
}

func TestVitessCredentialOwnershipRefusesUncertainLegacyIdentity(t *testing.T) {
	for name, mutate := range map[string]func(*corev1.Namespace, *corev1.Secret){
		"foreign owner": func(ns *corev1.Namespace, s *corev1.Secret) {
			s.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: ns.Name, UID: "foreign"}}
		},
		"wrong password":        func(_ *corev1.Namespace, s *corev1.Secret) { s.Data["password"] = bytes.Repeat([]byte("b"), 64) },
		"wrong scope":           func(_ *corev1.Namespace, s *corev1.Secret) { s.Labels["hakopod.io/project"] = "foreign" },
		"mutable":               func(_ *corev1.Namespace, s *corev1.Secret) { s.Immutable = ptr(false) },
		"missing uid":           func(_ *corev1.Namespace, s *corev1.Secret) { s.UID = "" },
		"missing revision":      func(_ *corev1.Namespace, s *corev1.Secret) { s.ResourceVersion = "" },
		"terminating namespace": func(ns *corev1.Namespace, _ *corev1.Secret) { now := metav1.Now(); ns.DeletionTimestamp = &now },
		"terminating secret":    func(_ *corev1.Namespace, s *corev1.Secret) { now := metav1.Now(); s.DeletionTimestamp = &now },
	} {
		t.Run(name, func(t *testing.T) {
			d, ns, s, password := vitessLegacyCredentialFixture()
			mutate(ns, s)
			c := &Client{kube: kubefake.NewSimpleClientset(ns, s)}
			if err := c.reconcileVitessCredentialOwnership(context.Background(), d, ns, s, password, func() error { t.Fatal("unsafe credential reached mutation fence"); return nil }); err == nil {
				t.Fatal("unsafe legacy credential accepted")
			}
		})
	}
}

func TestVitessCredentialOwnershipRechecksNamespaceAfterAuthorityFence(t *testing.T) {
	d, ns, s, password := vitessLegacyCredentialFixture()
	c := &Client{kube: kubefake.NewSimpleClientset(ns, s)}
	err := c.reconcileVitessCredentialOwnership(context.Background(), d, ns, s, password, func() error {
		changed := ns.DeepCopy()
		changed.UID = "replacement"
		_, err := c.kube.CoreV1().Namespaces().Update(context.Background(), changed, metav1.UpdateOptions{})
		return err
	})
	if err == nil {
		t.Fatal("replaced namespace accepted")
	}
	got, _ := c.kube.CoreV1().Secrets(ns.Name).Get(context.Background(), s.Name, metav1.GetOptions{})
	if len(got.OwnerReferences) != 0 {
		t.Fatal("replaced namespace gained credential ownership")
	}
	d, ns, s, password = vitessLegacyCredentialFixture()
	c = &Client{kube: kubefake.NewSimpleClientset(ns, s)}
	denied := errors.New("authority revoked")
	if err = c.reconcileVitessCredentialOwnership(context.Background(), d, ns, s, password, func() error { return denied }); !errors.Is(err, denied) {
		t.Fatal("authority error lost", err)
	}
}

func TestVitessCredentialOwnershipRejectsReplacedNamespaceForNewSecret(t *testing.T) {
	d, ns, s, password := vitessLegacyCredentialFixture()
	s.OwnerReferences = databaseIdentityMeta(d, ns.UID, s.Name).OwnerReferences
	replacement := ns.DeepCopy()
	replacement.UID = "replacement"
	c := &Client{kube: kubefake.NewSimpleClientset(replacement, s)}
	if err := c.reconcileVitessCredentialOwnership(context.Background(), d, ns, s, password, func() error { return nil }); err == nil {
		t.Fatal("new credential in replacement namespace accepted")
	}
	for _, action := range c.kube.(*kubefake.Clientset).Actions() {
		if action.GetVerb() == "update" {
			t.Fatal("replacement namespace credential was mutated")
		}
	}
}
