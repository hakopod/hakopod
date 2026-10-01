package cluster

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestClickHouseMaximumTopologyIdentityRemainsBounded(t *testing.T) {
	d := clickhouseFixture()
	d.Spec.Mode, d.Spec.Shards, d.Spec.Replicas = "cluster", 8, 5
	d.PublicEndpointNames = []string{"native.example.test", "https.example.test", "a.example.test", "b.example.test"}
	if d.Spec.Members() != database.MaxMembers {
		t.Fatal("maximum topology fixture drifted")
	}
	if err := d.Spec.Validate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace", Labels: databaseLabels(d)}}
	kube := kubefake.NewClientset(ns)
	// The fake API does not allocate Service IPs. Model that allocation explicitly.
	next := 10
	kube.PrependReactor("create", "services", func(action clienttesting.Action) (bool, runtime.Object, error) {
		service := action.(clienttesting.CreateAction).GetObject().(*corev1.Service)
		service.UID = types.UID("service-" + service.Name)
		service.Spec.ClusterIP = fmt.Sprintf("10.43.0.%d", next)
		service.Spec.ClusterIPs = []string{service.Spec.ClusterIP}
		next++
		return false, nil, nil
	})
	c := &Client{kube: kube}
	allow := func() error { return nil }
	if err := c.prepareDatabaseIdentity(ctx, d, allow); err != nil {
		t.Fatal("maximum shared identity rejected", err)
	}
	if err := c.prepareClickHouseClientIdentity(ctx, d, allow); err != nil {
		t.Fatal("maximum client identity rejected", err)
	}
	names, err := clickhouseClientIdentityNames(d)
	if err != nil || len(names) != 4*database.MaxMembers+11 || len(names) > database.MaxTLSVerificationNames {
		t.Fatal("identity bound does not cover supported topology", len(names), err)
	}
	leaf := clickhouseIdentitySecret(t, c, d, clickhouseClientTLSSecret)
	_, ca, err := database.ParsePublicTrust(leaf.Data["ca.crt"], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.VerifyServerCertificate(leaf.Data["tls.crt"], ca, names, time.Now()); err != nil {
		t.Fatal("maximum topology leaf failed verification", err)
	}
	tooMany := make([]string, database.MaxTLSVerificationNames+1)
	for i := range tooMany {
		tooMany[i] = names[0]
	}
	if _, err := database.VerifyServerCertificate(leaf.Data["tls.crt"], ca, tooMany, time.Now()); err == nil {
		t.Fatal("certificate verification accepted an unbounded host list")
	}
}

func clickhouseClientIdentityFixture(t *testing.T) (*Client, *kubefake.Clientset, database.Resource) {
	t.Helper()
	d := database.Resource{ID: "client-identity", Spec: database.Spec{Engine: "clickhouse", Mode: "standalone", Shards: 1, TLS: &database.TLSConfig{Mode: "required"}}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "identity-namespace", Labels: databaseLabels(d)}}
	kube := kubefake.NewClientset(ns)
	c := &Client{kube: kube}
	if err := c.prepareDatabaseIdentity(context.Background(), d, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	// Keep a stable IP SAN to exercise the separate client leaf's copy path.
	root := clickhouseIdentitySecret(t, c, d, "database-ca")
	internal := clickhouseIdentitySecret(t, c, d, "database-tls")
	pair, err := tls.X509KeyPair(root.Data["ca.crt"], root.Data["ca.key"])
	if err != nil {
		t.Fatal(err)
	}
	_, ca, err := database.ParsePublicTrust(root.Data["ca.crt"], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	leafPair, err := tls.X509KeyPair(internal.Data["tls.crt"], internal.Data["tls.key"])
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(leafPair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	cert.IPAddresses = []net.IP{net.ParseIP("10.43.1.20")}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca, leafPair.PrivateKey.(crypto.Signer).Public(), pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	internal.Data["tls.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if _, err = kube.CoreV1().Secrets(ns.Name).Update(context.Background(), internal, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	kube.ClearActions()
	return c, kube, d
}

func clickhouseIdentitySecret(t *testing.T, c *Client, d database.Resource, name string) *corev1.Secret {
	t.Helper()
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func assertClickhouseIdentityWrites(t *testing.T, kube *kubefake.Clientset, allowed bool) {
	t.Helper()
	for _, action := range kube.Actions() {
		if action.GetVerb() == "get" || action.GetVerb() == "list" {
			continue
		}
		if !allowed || action.GetResource().Resource != "secrets" {
			t.Fatalf("unexpected mutation: %s %s", action.GetVerb(), action.GetResource().Resource)
		}
		var secret *corev1.Secret
		switch write := action.(type) {
		case clienttesting.CreateAction:
			secret = write.GetObject().(*corev1.Secret)
		case clienttesting.UpdateAction:
			secret = write.GetObject().(*corev1.Secret)
		default:
			t.Fatalf("unexpected secret mutation: %s", action.GetVerb())
		}
		if secret.Name != clickhouseClientTLSSecret {
			t.Fatalf("shared identity mutation: %s", secret.Name)
		}
	}
}

func TestClickHouseClientSANChangesPreserveInternalAndKeeperIdentity(t *testing.T) {
	c, kube, d := clickhouseClientIdentityFixture(t)
	ctx := context.Background()
	root := clickhouseIdentitySecret(t, c, d, "database-ca")
	original := clickhouseIdentitySecret(t, c, d, "database-tls")
	keeper := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "database-keeper", Namespace: DatabaseNamespace(d.ID), UID: "keeper"}, Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"hakopod.io/tls-fingerprint": "unchanged"}}}}}
	if _, err := kube.AppsV1().StatefulSets(keeper.Namespace).Create(ctx, keeper, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	keeper, err := kube.AppsV1().StatefulSets(keeper.Namespace).Get(ctx, keeper.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, ca, err := database.ParsePublicTrust(root.Data["ca.crt"], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	allow := func() error { return nil }
	kube.ClearActions()
	host := "native.database.example.test"
	for _, public := range [][]string{{host, host}, nil} {
		d.PublicEndpointNames = public
		if err := c.prepareClickHouseClientIdentity(ctx, d, allow); err != nil {
			t.Fatal(err)
		}
		leaf := clickhouseIdentitySecret(t, c, d, clickhouseClientTLSSecret)
		names, err := clickhouseClientIdentityNames(d)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.VerifyServerCertificate(leaf.Data["tls.crt"], ca, append(names, "10.43.1.20"), time.Now()); err != nil {
			t.Fatal("client identity did not retain trust or internal IP", err)
		}
		if bytes.Equal(leaf.Data["tls.crt"], original.Data["tls.crt"]) || bytes.Equal(leaf.Data["tls.key"], original.Data["tls.key"]) {
			t.Fatal("client identity reused internal leaf or key")
		}
		if len(public) == 0 {
			if _, err := database.VerifyServerCertificate(leaf.Data["tls.crt"], ca, []string{host}, time.Now()); err == nil {
				t.Fatal("removed public name still verifies")
			}
		}
		if !reflect.DeepEqual(original, clickhouseIdentitySecret(t, c, d, "database-tls")) || !reflect.DeepEqual(root, clickhouseIdentitySecret(t, c, d, "database-ca")) {
			t.Fatal("SAN change modified original identity")
		}
		currentKeeper, err := kube.AppsV1().StatefulSets(keeper.Namespace).Get(ctx, keeper.Name, metav1.GetOptions{})
		if err != nil || !reflect.DeepEqual(keeper, currentKeeper) {
			t.Fatal("SAN change modified Keeper", err)
		}
	}
	assertClickhouseIdentityWrites(t, kube, true)
	kube.ClearActions()
	if err := c.prepareClickHouseClientIdentity(ctx, d, allow); err != nil {
		t.Fatal(err)
	}
	assertClickhouseIdentityWrites(t, kube, false)
}

func TestClickHouseClientIdentityRefusesLeaseLossAndForeignSecrets(t *testing.T) {
	for _, name := range []string{"lease-create", "lease-update", "database-ca", "database-tls", clickhouseClientTLSSecret, "namespace", "invalid-name", "oversized-key"} {
		t.Run(name, func(t *testing.T) {
			c, kube, d := clickhouseClientIdentityFixture(t)
			ctx := context.Background()
			allow := func() error { return nil }
			if name != "lease-create" {
				if err := c.prepareClickHouseClientIdentity(ctx, d, allow); err != nil {
					t.Fatal(err)
				}
			}
			d.PublicEndpointNames = []string{"https.database.example.test"}
			before := allow
			lost := errors.New("lease lost")
			switch name {
			case "lease-create", "lease-update":
				before = func() error { return lost }
			case "namespace":
				ns, err := kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				ns.UID = "replacement"
				if _, err = kube.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "invalid-name":
				d.PublicEndpointNames = []string{"*.example.test"}
			case "oversized-key":
				secret := clickhouseIdentitySecret(t, c, d, "database-ca")
				secret.Data["ca.key"] = make([]byte, 33<<10)
				if _, err := kube.CoreV1().Secrets(secret.Namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			default:
				secret := clickhouseIdentitySecret(t, c, d, name)
				secret.OwnerReferences[0].UID = "foreign"
				if _, err := kube.CoreV1().Secrets(secret.Namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			kube.ClearActions()
			err := c.prepareClickHouseClientIdentity(ctx, d, before)
			if err == nil {
				t.Fatal("unsafe identity mutation accepted")
			}
			if (name == "lease-create" || name == "lease-update") && !errors.Is(err, lost) {
				t.Fatal("lease refusal not propagated", err)
			}
			assertClickhouseIdentityWrites(t, kube, false)
		})
	}
}

func TestClickHouseClientIdentityRenewalRetainsOldCATrust(t *testing.T) {
	c, kube, d := clickhouseClientIdentityFixture(t)
	ctx := context.Background()
	d.PublicEndpointNames = []string{"native.database.example.test"}
	allow := func() error { return nil }
	if err := c.prepareClickHouseClientIdentity(ctx, d, allow); err != nil {
		t.Fatal(err)
	}
	oldLeaf := clickhouseIdentitySecret(t, c, d, clickhouseClientTLSSecret)
	root := clickhouseIdentitySecret(t, c, d, "database-ca")
	original := clickhouseIdentitySecret(t, c, d, "database-tls")
	_, oldCA, err := database.ParsePublicTrust(root.Data["ca.crt"], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(root.Data["ca.crt"], root.Data["ca.key"])
	if err != nil {
		t.Fatal(err)
	}
	// Model maintenance's same-key renewal while the old CA is still valid.
	renewed := *oldCA
	renewed.SerialNumber = new(big.Int).Add(oldCA.SerialNumber, big.NewInt(1))
	renewed.NotAfter = oldCA.NotAfter.Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, &renewed, &renewed, pair.PrivateKey.(crypto.Signer).Public(), pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	root.Data["ca.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if _, err = kube.CoreV1().Secrets(root.Namespace).Update(ctx, root, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	root = clickhouseIdentitySecret(t, c, d, "database-ca")
	kube.ClearActions()
	if err := c.prepareClickHouseClientIdentity(ctx, d, allow); err != nil {
		t.Fatal(err)
	}
	leaf := clickhouseIdentitySecret(t, c, d, clickhouseClientTLSSecret)
	if bytes.Equal(oldLeaf.Data["tls.crt"], leaf.Data["tls.crt"]) || !bytes.Equal(root.Data["ca.crt"], leaf.Data["ca.crt"]) {
		t.Fatal("client identity did not consume issuer renewal")
	}
	names, err := clickhouseClientIdentityNames(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.VerifyServerCertificate(leaf.Data["tls.crt"], oldCA, names, time.Now()); err != nil {
		t.Fatal("old CA no longer trusts renewed client leaf", err)
	}
	if !reflect.DeepEqual(original, clickhouseIdentitySecret(t, c, d, "database-tls")) || !reflect.DeepEqual(root, clickhouseIdentitySecret(t, c, d, "database-ca")) {
		t.Fatal("client renewal modified shared issuer or internal identity")
	}
	assertClickhouseIdentityWrites(t, kube, true)
}

func TestClickHouseClientLeafExpiryRenewsOnlyClientIdentity(t *testing.T) {
	c, kube, d := clickhouseClientIdentityFixture(t)
	ctx := context.Background()
	allow := func() error { return nil }
	if err := c.prepareClickHouseClientIdentity(ctx, d, allow); err != nil {
		t.Fatal(err)
	}
	root := clickhouseIdentitySecret(t, c, d, "database-ca")
	leaf := clickhouseIdentitySecret(t, c, d, clickhouseClientTLSSecret)
	pair, err := tls.X509KeyPair(root.Data["ca.crt"], root.Data["ca.key"])
	if err != nil {
		t.Fatal(err)
	}
	_, ca, err := database.ParsePublicTrust(root.Data["ca.crt"], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	leafPair, err := tls.X509KeyPair(leaf.Data["tls.crt"], leaf.Data["tls.key"])
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(leafPair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	cert.NotAfter = time.Now().Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, cert, ca, leafPair.PrivateKey.(crypto.Signer).Public(), pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf.Data["tls.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if _, err = kube.CoreV1().Secrets(leaf.Namespace).Update(ctx, leaf, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	kube.ClearActions()
	if err := c.prepareClickHouseClientIdentity(ctx, d, allow); err != nil {
		t.Fatal(err)
	}
	renewed := clickhouseIdentitySecret(t, c, d, clickhouseClientTLSSecret)
	observation, err := database.VerifyServerCertificate(renewed.Data["tls.crt"], ca, databaseIdentityNames(d), time.Now())
	if err != nil || observation.ExpiresAt == nil || observation.ExpiresAt.Before(time.Now().Add(7*24*time.Hour)) {
		t.Fatal("client expiry did not renew", err)
	}
	assertClickhouseIdentityWrites(t, kube, true)
}
