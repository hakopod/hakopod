package cluster

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
)

func TestClickHouseKeeperServiceCertificateIdentity(t *testing.T) {
	ctx := context.Background()
	d := clickhouseFixture()
	d.Spec.Mode, d.Spec.Replicas = "cluster", 1
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "fixture-namespace", Labels: databaseLabels(d)}}
	client := fake.NewClientset(ns)
	next := 10
	client.PrependReactor("create", "services", func(action kubetesting.Action) (bool, runtime.Object, error) {
		service := action.(kubetesting.CreateAction).GetObject().(*corev1.Service)
		service.UID = types.UID("service-" + service.Name)
		service.Spec.ClusterIP = fmt.Sprintf("10.43.0.%d", next)
		service.Spec.ClusterIPs = []string{service.Spec.ClusterIP}
		next++
		return false, nil, nil
	})
	c := &Client{kube: client}
	if err := c.prepareDatabaseIdentity(ctx, d, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	leaf, err := client.CoreV1().Secrets(ns.Name).Get(ctx, "database-tls", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(leaf.Data["tls.crt"])
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(certificate.IPAddresses) != 3 {
		t.Fatal("Keeper address identities are missing", err)
	}
	for i := 0; i < 3; i++ {
		if err := certificate.VerifyHostname(fmt.Sprintf("10.43.0.%d", i+10)); err != nil {
			t.Fatal(err)
		}
		if err := certificate.VerifyHostname(clickhouseKeeperHost(d, i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.prepareDatabaseIdentity(ctx, d, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	same, _ := client.CoreV1().Secrets(ns.Name).Get(ctx, "database-tls", metav1.GetOptions{})
	if !reflect.DeepEqual(same.Data, leaf.Data) {
		t.Fatal("unchanged Keeper addresses rotated the certificate")
	}
	service, _ := client.CoreV1().Services(ns.Name).Get(ctx, "database-keeper-0", metav1.GetOptions{})
	service.Spec.Selector["statefulset.kubernetes.io/pod-name"] = "unrelated"
	if _, err := client.CoreV1().Services(ns.Name).Update(ctx, service, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.prepareDatabaseIdentity(ctx, d, func() error { return nil }); err == nil {
		t.Fatal("Keeper certificate accepted a service with a changed target")
	}
}
