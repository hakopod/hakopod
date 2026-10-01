package cluster

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDatabaseIdentityOwnershipRenewalAndTrust(t *testing.T) {
	ctx := context.Background()
	d := database.Resource{ID: strings.Repeat("a", 32), Project: "demo", Environment: "development", Spec: database.Spec{Engine: "redis", TLS: &database.TLSConfig{Mode: "required"}}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-fixture", Labels: databaseLabels(d)}}
	c := &Client{kube: fake.NewClientset(ns)}
	blocked := errors.New("lost claim")
	if err := c.prepareDatabaseIdentity(ctx, d, func() error { return blocked }); !errors.Is(err, blocked) {
		t.Fatal("lost claim did not prevent issuance", err)
	}
	before := func() error { return nil }
	if err := c.prepareDatabaseIdentity(ctx, d, before); err != nil {
		t.Fatal(err)
	}
	api := c.kube.CoreV1().Secrets(ns.Name)
	root, err := api.Get(ctx, "database-ca", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := api.Get(ctx, "database-tls", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	trust, ca, err := database.ParsePublicTrust(root.Data["ca.crt"], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	host := redisMemberHostname(d, database.Member{Name: "database-leader-0"})
	if _, err = database.VerifyServerCertificate(leaf.Data["tls.crt"], ca, []string{host, "database-leader." + ns.Name + ".svc"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = c.prepareDatabaseIdentity(ctx, d, before); err != nil {
		t.Fatal(err)
	}
	same, _ := api.Get(ctx, "database-tls", metav1.GetOptions{})
	if !reflect.DeepEqual(same.Data, leaf.Data) {
		t.Fatal("unchanged identity was rotated")
	}
	// Force renewal while retaining the real key; existing CA trust must continue
	// verifying the newly issued leaf throughout the overlap.
	pair, err := tls.X509KeyPair(root.Data["ca.crt"], root.Data["ca.key"])
	if err != nil {
		t.Fatal(err)
	}
	signer := pair.PrivateKey.(crypto.Signer)
	ca.NotAfter = time.Now().Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, signer.Public(), signer)
	if err != nil {
		t.Fatal(err)
	}
	root.Data["ca.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if _, err = api.Update(ctx, root, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.prepareDatabaseIdentity(ctx, d, before); err != nil {
		t.Fatal(err)
	}
	renewed, _ := api.Get(ctx, "database-tls", metav1.GetOptions{})
	_, oldCA, _ := database.ParsePublicTrust([]byte(trust.CertificatePEM), time.Now())
	if _, err = database.VerifyServerCertificate(renewed.Data["tls.crt"], oldCA, []string{host}, time.Now()); err != nil {
		t.Fatal("CA overlap broke existing clients", err)
	}
	if reflect.DeepEqual(renewed.Data["tls.key"], leaf.Data["tls.key"]) {
		t.Fatal("leaf private key was not rotated")
	}
	ns.UID = "replacement-namespace"
	if _, err = c.kube.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.prepareDatabaseIdentity(ctx, d, before); err == nil {
		t.Fatal("foreign namespace identity was adopted")
	}
}

func TestMySQLIdentityCoversRouterMetadataBootstrapAndMembers(t *testing.T) {
	d := mysqlUnitFixture()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "mysql-namespace-fixture", Labels: databaseLabels(d)}}
	c := &Client{kube: fake.NewClientset(ns)}
	ctx := context.Background()
	if err := c.prepareDatabaseIdentity(ctx, d, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	leaf, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, "database-tls", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, ca, err := database.ParsePublicTrust(leaf.Data["ca.crt"], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"database", "database-instances", "database-0.database-instances"} {
		for _, suffix := range []string{".svc", ".svc.cluster.local"} {
			if _, err := database.VerifyServerCertificate(leaf.Data["tls.crt"], ca, []string{prefix + "." + ns.Name + suffix}, time.Now()); err != nil {
				t.Fatal("Router metadata or database route is not covered by the issued identity", err)
			}
		}
	}
}

func TestRedisTLSControllerPolicyAndManifest(t *testing.T) {
	pod := redisControllerFixture().Spec.Template
	if safeRedisTLSController(pod) {
		t.Fatal("unpatched controller accepted TLS")
	}
	pod.Annotations["hakopod.io/redis-tls-policy"] = redisControllerTLSPolicy
	pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{Name: "FEATURE_GATES", Value: "GenerateConfigInInitContainer=true"}, corev1.EnvVar{Name: "INIT_CONTAINER_IMAGE", Value: pod.Spec.Containers[0].Image})
	if !safeRedisTLSController(pod) {
		t.Fatal("verified controller policy rejected")
	}
	for _, mode := range []string{"standalone", "cluster"} {
		d := database.Resource{ID: strings.Repeat("a", 32), Revision: 1, Spec: database.Spec{SchemaVersion: 1, Name: "secure-fixture", Engine: "redis", Version: "8", Mode: mode, Shards: 1, CPU: "100m", Memory: "128Mi", StorageGiB: 1, TLS: &database.TLSConfig{Mode: "required"}}}
		if mode == "cluster" {
			d.Spec.Shards = 3
			d.Spec.Replicas = 1
		}
		object, err := DatabaseObject(d)
		if err != nil {
			t.Fatal(err)
		}
		spec := object.Object["spec"].(map[string]any)
		if spec["TLS"] == nil {
			t.Fatal("required security missing from controller object")
		}
		if mode == "standalone" {
			if spec["redisConfig"].(map[string]any)["additionalRedisConfig"] != "database-security" {
				t.Fatal("standalone security config missing")
			}
		} else {
			if spec["redisConfig"] != nil {
				t.Fatal("cluster has an unsupported top-level configuration field")
			}
			for _, role := range []string{"redisLeader", "redisFollower"} {
				if spec[role].(map[string]any)["redisConfig"].(map[string]any)["additionalRedisConfig"] != "database-security" {
					t.Fatal("cluster role security config missing", role)
				}
			}
		}
	}
}
