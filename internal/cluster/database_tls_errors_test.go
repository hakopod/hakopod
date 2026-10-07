package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
)

func TestDatabaseCertificateLookupPreservesRequestErrors(t *testing.T) {
	for _, stage := range []string{"namespace", "controller"} {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(stage+"/"+cause.Error(), func(t *testing.T) {
				d := oracleFixture()
				namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: map[string]string{databaseOwner: d.ID, managedBy: "hakopod"}}}
				kube := fake.NewSimpleClientset(namespace)
				dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
				fail := func(kubetesting.Action) (bool, runtime.Object, error) { return true, nil, cause }
				if stage == "namespace" {
					kube.PrependReactor("get", "namespaces", fail)
				} else {
					dynamic.PrependReactor("get", "*", fail)
				}
				c := &Client{kube: kube, dynamic: dynamic}
				_, certificate, err := c.databaseCertificates(context.Background(), d)
				if !errors.Is(err, cause) || strings.Contains(err.Error(), "ownership changed") || len(certificate) != 0 {
					t.Fatal("certificate lookup hid a request failure or returned certificate data", err)
				}
			})
		}
	}
}

func TestDatabaseCertificateLookupRejectsForeignNamespace(t *testing.T) {
	d := oracleFixture()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: map[string]string{databaseOwner: "another-database", managedBy: "hakopod"}}}
	c := &Client{kube: fake.NewSimpleClientset(namespace)}
	_, certificate, err := c.databaseCertificates(context.Background(), d)
	if err == nil || !strings.Contains(err.Error(), "ownership changed") || len(certificate) != 0 {
		t.Fatal("certificate lookup accepted another database's namespace", err)
	}
}
