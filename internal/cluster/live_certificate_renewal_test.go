package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveAutomaticBackendCertificateRenewal(t *testing.T) {
	if os.Getenv("HAKOPOD_CERTIFICATE_RENEWAL_TEST") != "1" {
		t.Skip("requires isolated automatic certificate acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("renewal acceptance requires named k3d-hakopod-dev context")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// An isolated mutable TLS source stands in for cert-manager issuance. No ACME
	// request is made and the test never changes a real issuer or sends email.
	c, err := New(path, Options{AppDomain: "127.0.0.1.sslip.io", TLSIssuer: "hakopod-renewal-fixture", RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: fmt.Sprintf("certificate-renewal-%d", time.Now().UnixNano()), Project: "renewal-fixture", Environment: "test", Revision: 1, OperationID: "renewal-fixture"}
	target.Spec, err = spec.Normalize(spec.Application{Name: "renewal-fixture", Services: map[string]spec.Service{"smtp": {Image: "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a", Port: 2525, Public: true, UpdateStrategy: "recreate", TerminationGraceSeconds: 1, RunAsUser: 12345, RunAsGroup: 23456, FSGroup: 23456, ReadOnlyRootFilesystem: true, Command: []string{"python", "-B", "-c", smtpFixturePython}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.bootstrap(ctx, target); err != nil {
		t.Fatal(err)
	}
	ns := Namespace(target.ApplicationID)
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		n, e := c.kube.CoreV1().Namespaces().Get(clean, ns, metav1.GetOptions{})
		if e == nil && owned(n, target) == nil {
			if e = c.kube.CoreV1().Namespaces().Delete(clean, ns, deleteOptions(n)); e != nil {
				t.Error(e)
			}
		}
	})
	host := c.hostname(target, "smtp")
	cert, key := testTLSCertificate(t, host, time.Now().Add(time.Hour))
	source, err := c.kube.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "hakopod-tls-smtp", Namespace: ns, Labels: labelsFor(target, "smtp")}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := c.PutBackendCertificate(ctx, target, "smtp", host, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	svc := target.Spec.Services["smtp"]
	svc.CertificateMounts = []spec.CertificateMount{{Certificate: uploaded.Certificate, Hostname: host, MountPath: "/smtp-cert"}}
	target.Spec.Services["smtp"] = svc
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	// Explicit opt-in is a reviewed revision. Later renewals need no new revision.
	target.Revision++
	svc.CertificateMounts = []spec.CertificateMount{{Source: "ingress", Hostname: host, MountPath: "/smtp-cert"}}
	target.Spec.Services["smtp"] = svc
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	probe := func(expected []byte) {
		t.Helper()
		block, _ := pem.Decode(expected)
		leaf, e := x509.ParseCertificate(block.Bytes)
		if e != nil {
			t.Fatal(e)
		}
		code := `import smtplib,ssl,hashlib,os,stat
assert os.getuid()==12345 and not os.path.exists('/var/run/secrets/kubernetes.io/serviceaccount/token')
assert stat.S_IMODE(os.stat('/smtp-cert/tls.key').st_mode)==0o440
context=ssl.create_default_context(cafile='/smtp-cert/tls.crt')
client=smtplib.SMTP('127.0.0.1',2525,timeout=4)
client._host=` + fmt.Sprintf("%q", host) + `
client.starttls(context=context)
print(hashlib.sha256(client.sock.getpeercert(binary_form=True)).hexdigest())
client.quit()
`
		out, e := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", ns, "exec", "deployment/smtp", "--", "python", "-B", "-c", code).CombinedOutput()
		if e != nil || strings.TrimSpace(string(out)) != fmt.Sprintf("%x", sha256.Sum256(leaf.Raw)) {
			t.Fatalf("STARTTLS certificate probe failed: %v %s", e, out)
		}
	}
	probe(cert)
	cert2, key2 := testTLSCertificate(t, host, time.Now().Add(2*time.Hour))
	source.Data = map[string][]byte{corev1.TLSCertKey: cert2, corev1.TLSPrivateKeyKey: key2}
	source, err = c.kube.CoreV1().Secrets(ns).Update(ctx, source, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.RenewBackendCertificates(ctx, target, func(e Event) { t.Log(e.Type, e.Message) }); err != nil {
		t.Fatal(err)
	}
	d, err := c.kube.AppsV1().Deployments(ns).Get(ctx, "smtp", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.waitReady(ctx, target, "smtp", d.Generation); err != nil {
		t.Fatal(err)
	}
	probe(cert2)
	if d.Annotations["hakopod.io/revision"] != "2" {
		t.Fatal("automatic renewal changed application revision")
	}
	if string(d.Spec.Strategy.Type) != "Recreate" || d.Spec.Strategy.RollingUpdate != nil {
		t.Fatal("renewal did not retain singleton Recreate strategy")
	}
	previous := d.Generation
	source.Data[corev1.TLSPrivateKeyKey] = []byte("invalid fixture replacement")
	source, err = c.kube.CoreV1().Secrets(ns).Update(ctx, source, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.RenewBackendCertificates(ctx, target, nil); err == nil {
		t.Fatal("invalid source accepted")
	}
	d, _ = c.kube.AppsV1().Deployments(ns).Get(ctx, "smtp", metav1.GetOptions{})
	if d.Generation != previous {
		t.Fatal("invalid source caused rollout")
	}
	probe(cert2)
	// Replacing an uploaded ingress reference must finish its backend restart
	// within the same reviewed deployment, rather than report premature success.
	source.Data = map[string][]byte{corev1.TLSCertKey: cert2, corev1.TLSPrivateKeyKey: key2}
	if source, err = c.kube.CoreV1().Secrets(ns).Update(ctx, source, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	cert3, key3 := testTLSCertificate(t, host, time.Now().Add(3*time.Hour))
	ingressReference, err := c.PutTLSCertificate(ctx, target, "smtp", cert3, key3)
	if err != nil {
		t.Fatal(err)
	}
	svc = target.Spec.Services["smtp"]
	svc.TLS = &spec.TLSConfig{Certificate: ingressReference}
	target.Spec.Services["smtp"] = svc
	target.Revision++
	observed, err := c.Deploy(ctx, target, nil)
	if err != nil || observed.Status != "healthy" {
		t.Fatal("reviewed ingress replacement did not finish renewal", observed.Status, err)
	}
	probe(cert3)
	t.Log("Automatic ingress renewal rolled non-root SMTP pods at the same application revision; verified STARTTLS served the new certificate and invalid source retained the working certificate. No email sent.")
}
