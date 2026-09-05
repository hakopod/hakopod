package cluster

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/clientcmd"
)

func testTLSCertificate(t *testing.T, hostname string, expiry time.Time, extraHosts ...string) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: hostname}, DNSNames: []string{hostname}, NotBefore: time.Now().Add(-time.Minute), NotAfter: expiry, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	template.DNSNames = append(template.DNSNames, extraHosts...)
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}
func TestTLSValidationAndOwnedAttachment(t *testing.T) {
	ctx := context.Background()
	target := testTarget(t)
	client := &Client{kube: fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}}), options: Options{AppDomain: "example.com", IngressClass: "haproxy", PublicPort: 18080, PublicHTTPSPort: 18443}}
	hostname := client.hostname(target, "web")
	cert, key := testTLSCertificate(t, hostname, time.Now().Add(time.Hour))
	if _, err := validateTLSCertificate(cert, key, "other.example.com", time.Now()); err == nil {
		t.Fatal("hostname mismatch accepted")
	}
	if _, err := validateTLSCertificate(cert, key, hostname, time.Now().Add(2*time.Hour)); err == nil {
		t.Fatal("expired certificate accepted")
	}
	_, otherKey := testTLSCertificate(t, hostname, time.Now().Add(time.Hour))
	if _, err := validateTLSCertificate(cert, otherKey, hostname, time.Now()); err == nil {
		t.Fatal("mismatched key accepted")
	}
	name, err := client.PutTLSCertificate(ctx, target, "web", cert, key)
	if err != nil {
		t.Fatal(err)
	}
	svc := target.Spec.Services["web"]
	svc.TLS = &spec.TLSConfig{Certificate: name}
	target.Spec.Services["web"] = svc
	if err = client.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	status, err := client.ServiceTLS(ctx, target, "web")
	if err != nil || !status.Ready || !status.Enabled || status.SecretName != name {
		t.Fatalf("TLS not attached: %+v %v", status, err)
	}
	if url := client.serviceURL(target, "web"); !strings.Contains(url, "https://") || !strings.HasSuffix(url, ":18443") {
		t.Fatal("incorrect HTTPS port", url)
	}
	if other, err := client.PutTLSCertificate(ctx, target, "web", cert, key); err != nil || other != name {
		t.Fatal("identical upload did not reuse immutable secret")
	}
	other := target.Spec.Services["api"]
	other.Public = true
	other.TLS = svc.TLS
	if err = client.applyIngress(ctx, target, "api", other); err == nil {
		t.Fatal("another service used uploaded private key")
	}
}

func TestTLSLiveUploadAndStatus(t *testing.T) {
	config := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	if config == "" {
		t.Skip("set HAKOPOD_TEST_KUBECONFIG for dedicated live TLS fixture")
	}
	configuration, err := clientcmd.LoadFromFile(config)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("live TLS test requires the named k3d-hakopod-dev context")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := New(config, Options{AppDomain: "localhost", IngressClass: "haproxy", PublicPort: 18080, PublicHTTPSPort: 18443})
	if err != nil {
		t.Fatal(err)
	}
	target := testTarget(t)
	target.ApplicationID = "tls-upload-live-fixture-v1"
	ns, err := client.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.kube.CoreV1().Namespaces().Delete(clean, ns.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &ns.UID}})
	}()
	cert, key := testTLSCertificate(t, client.hostname(target, "web"), time.Now().Add(time.Hour))
	name, err := client.PutTLSCertificate(ctx, target, "web", cert, key)
	if err != nil {
		t.Fatal(err)
	}
	svc := target.Spec.Services["web"]
	svc.TLS = &spec.TLSConfig{Certificate: name}
	target.Spec.Services["web"] = svc
	if err = client.applyService(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	if err = client.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	status, err := client.ServiceTLS(ctx, target, "web")
	if err != nil || !status.Ready {
		t.Fatalf("live TLS attachment failed: %+v %v", status, err)
	}
	trust := x509.NewCertPool()
	trust.AppendCertsFromPEM(cert)
	deadline := time.Now().Add(15 * time.Second)
	for {
		connection, dialErr := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", "127.0.0.1:18443", &tls.Config{RootCAs: trust, ServerName: client.hostname(target, "web"), MinVersion: tls.VersionTLS12})
		if dialErr == nil {
			connection.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("HAProxy did not present the uploaded hostname-matching certificate")
		}
		time.Sleep(300 * time.Millisecond)
	}
	issuers, err := client.TLSIssuers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("uploaded hostname-matching certificate served by real HAProxy TLS listener; cert-manager installed=%v", issuers.Installed)
}
