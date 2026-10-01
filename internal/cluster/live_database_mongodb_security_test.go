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
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testMongoDBCredentialLogs(t *testing.T, ctx context.Context, c *Client, d database.Resource) {
	t.Helper()
	ns := DatabaseNamespace(d.ID)
	secrets, err := c.kube.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{Limit: 40})
	if err != nil || secrets.Continue != "" {
		t.Fatal("MongoDB log audit credential inventory unavailable")
	}
	var private [][]byte
	for _, secret := range secrets.Items {
		for key, value := range secret.Data {
			if len(value) >= 16 && (strings.Contains(strings.ToLower(key), "password") || strings.HasSuffix(key, ".key")) {
				private = append(private, value)
			}
		}
	}
	if len(private) < 5 {
		t.Fatal("MongoDB log audit did not load expected private material")
	}
	for namespace, selector := range map[string]string{ns: "", "mongodb-system": "app.kubernetes.io/name=mongodb-kubernetes-operator"} {
		pods, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 20})
		if err != nil || pods.Continue != "" || len(pods.Items) == 0 {
			t.Fatal("MongoDB log audit pod inventory unavailable")
		}
		for _, pod := range pods.Items {
			for _, container := range append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
				limit := int64(4 << 20)
				data, err := c.kube.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: container.Name, LimitBytes: &limit}).DoRaw(ctx)
				if err != nil || len(data) >= int(limit) {
					t.Fatal("MongoDB log audit could not inspect the complete bounded log")
				}
				for _, value := range private {
					if bytes.Contains(data, value) {
						t.Fatalf("MongoDB private material appeared in %s/%s logs", pod.Name, container.Name)
					}
				}
			}
		}
	}
	t.Log("MongoDB member, initialization, agent and controller logs contain none of this fixture's password or private-key values")
}

func testMongoDBCertificateRefusal(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation) {
	t.Helper()
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	member, err := mongodbObservedPrimary(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, wrongIssuer := range []bool{false, true} {
		config, err := redisTLSConfig(trust, mongodbMemberHost(d, member))
		if err != nil {
			t.Fatal(err)
		}
		if wrongIssuer {
			config.RootCAs = x509.NewCertPool()
		} else {
			config.ServerName = "incorrect.database.example"
		}
		step, stop := context.WithTimeout(ctx, 8*time.Second)
		conn, err := c.mongodbStream(step, step, d, member)
		if err != nil {
			stop()
			t.Fatal(err)
		}
		secured := tls.Client(conn, config)
		err = secured.HandshakeContext(step)
		_ = secured.Close()
		stop()
		var certificate *tls.CertificateVerificationError
		if !errors.As(err, &certificate) {
			t.Fatal("MongoDB TLS refusal was not a certificate verification failure")
		}
	}
	t.Log("MongoDB native TLS rejected an untrusted issuer and mismatched hostname")
}

func testMongoDBRenewal(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation) database.Observation {
	t.Helper()
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	root, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-ca", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(root.Data["ca.crt"], root.Data["ca.key"])
	if err != nil {
		t.Fatal("MongoDB fixture issuer is invalid")
	}
	ca, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	signer := pair.PrivateKey.(crypto.Signer)
	ca.NotAfter = time.Now().Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, signer.Public(), signer)
	if err != nil {
		t.Fatal("MongoDB fixture issuer expiry injection failed")
	}
	root.Data["ca.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if _, err = c.kube.CoreV1().Secrets(root.Namespace).Update(ctx, root, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.RenewDatabaseIdentity(ctx, d, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	wait, stop := context.WithTimeout(ctx, 6*time.Minute)
	defer stop()
	var next database.Observation
	for wait.Err() == nil {
		step, cancel := context.WithTimeout(wait, 35*time.Second)
		next, err = c.ObserveDatabase(step, d)
		cancel()
		if err == nil && next.Status == "ready" && next.TLS != nil && next.TLS.Verified && next.TLS.Fingerprint != o.TLS.Fingerprint && next.TLS.CAFingerprint != trust.Fingerprint {
			break
		}
		if sleepContext(wait, 5*time.Second) != nil {
			break
		}
	}
	if next.Status != "ready" || next.TLS == nil || !next.TLS.Verified || next.TLS.Fingerprint == o.TLS.Fingerprint || next.TLS.CAFingerprint == trust.Fingerprint {
		t.Fatal("MongoDB did not load renewed certificate and issuer", err)
	}
	for _, member := range next.Members {
		config, err := redisTLSConfig(trust, mongodbMemberHost(d, member))
		if err != nil {
			t.Fatal(err)
		}
		step, cancel := context.WithTimeout(ctx, 8*time.Second)
		conn, err := c.mongodbStream(step, step, d, member)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		secured := tls.Client(conn, config)
		err = secured.HandshakeContext(step)
		_ = secured.Close()
		cancel()
		if err != nil {
			t.Fatal("old MongoDB CA no longer trusts renewed member identity")
		}
	}
	t.Log("Every MongoDB member served a renewed identity with verified old-CA overlap")
	return next
}
