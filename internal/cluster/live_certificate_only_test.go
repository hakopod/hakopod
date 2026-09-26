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

// The named development cluster carries no issuer, so the fixture installs a
// self-signed one. Self-signed issuance needs no ACME account, no DNS and no
// inbound reachability, which keeps this test about hakopod's ingress shape.
const certificateOnlyIssuer = "hakopod-certificate-only-live"

const certificateOnlyIssuerYAML = `apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ` + certificateOnlyIssuer + `
spec:
  selfSigned: {}
`

// The service terminates TLS itself in production. The fixture only has to
// accept the raw connections the readiness check makes, so it stays a listener.
const certificateOnlyFixture = `import socket
listener = socket.socket()
listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
listener.bind(('0.0.0.0', 7000))
listener.listen(16)
while True:
 listener.accept()[0].close()
`

// TestLiveCertificateOnlyIngressIssuesAndMounts proves that a service published
// only over raw TCP receives an automatically issued certificate through
// hakopod's own reconciliation. Every Kubernetes object under test is created by
// the product code; the test writes no Ingress, Certificate or Secret YAML.
func TestLiveCertificateOnlyIngressIssuesAndMounts(t *testing.T) {
	if os.Getenv("HAKOPOD_CERTIFICATE_ONLY_TEST") != "1" {
		t.Skip("set HAKOPOD_CERTIFICATE_ONLY_TEST=1 and HAKOPOD_TEST_KUBECONFIG for the named development cluster")
	}
	kubeconfig := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing certificate-only ingress test outside named development cluster")
	}
	kube := func(args ...string) (string, error) {
		out, err := exec.Command("kubectl", append([]string{"--kubeconfig", kubeconfig, "--context", "k3d-hakopod-dev"}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	c, err := New(kubeconfig, Options{AppDomain: "127.0.0.1.sslip.io", IngressClass: "haproxy", TLSIssuer: certificateOnlyIssuer, RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	t.Cleanup(func() {
		if out, e := kube("delete", "clusterissuer", certificateOnlyIssuer, "--ignore-not-found"); e != nil {
			t.Error("cluster issuer cleanup failed:", out, e)
		}
	})
	apply := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "--context", "k3d-hakopod-dev", "apply", "-f", "-")
	apply.Stdin = strings.NewReader(certificateOnlyIssuerYAML)
	if out, e := apply.CombinedOutput(); e != nil {
		t.Fatalf("self-signed issuer could not be installed: %v %s", e, out)
	}
	app, err := spec.Normalize(spec.Application{Name: "certificate-only", Services: map[string]spec.Service{"tunnel": {
		Image: "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a",
		Port:  7000, RunAsUser: 12345, RunAsGroup: 23456, FSGroup: 23456, ReadOnlyRootFilesystem: true,
		PublicTCP: []spec.PublicTCPListener{{Port: 7443, TargetPort: 7000, SourceCIDRs: []string{"0.0.0.0/0"}}},
		Command:   []string{"python", "-B", "-c", certificateOnlyFixture},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: fmt.Sprintf("certificate-only-%d", time.Now().UnixNano()), Project: "certificate-only-test", Environment: "test", OperationID: "certificate-only-initial", Revision: 1, Spec: app}
	ns := Namespace(target.ApplicationID)
	if err = c.bootstrap(ctx, target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 90*time.Second)
		defer done()
		n, e := c.kube.CoreV1().Namespaces().Get(clean, ns, metav1.GetOptions{})
		if e == nil && owned(n, target) == nil {
			if e = c.kube.CoreV1().Namespaces().Delete(clean, ns, deleteOptions(n)); e != nil {
				t.Error(e)
			}
		}
	})
	host := c.hostname(target, "tunnel")
	svc := target.Spec.Services["tunnel"]
	svc.CertificateMounts = []spec.CertificateMount{{Source: "ingress", Hostname: host, MountPath: "/certificates/tunnel"}}
	target.Spec.Services["tunnel"] = svc
	if svc.Public {
		t.Fatal("fixture must not publish public HTTP")
	}

	// Hakopod's own reconciliation creates the certificate-only ingress.
	if err = c.reconcilePrivateIngress(ctx, target, "tunnel", svc); err != nil {
		t.Fatal(err)
	}
	ing, err := c.kube.NetworkingV1().Ingresses(ns).Get(ctx, "tunnel", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != host || ing.Spec.Rules[0].HTTP != nil {
		t.Fatalf("certificate-only ingress must carry one host-only rule: %+v", ing.Spec.Rules)
	}
	if len(ing.Spec.TLS) != 1 || len(ing.Spec.TLS[0].Hosts) != 1 || ing.Spec.TLS[0].Hosts[0] != host || ing.Spec.TLS[0].SecretName != "hakopod-tls-tunnel" {
		t.Fatalf("certificate-only ingress must request TLS for its hostname: %+v", ing.Spec.TLS)
	}
	if ing.Annotations["cert-manager.io/cluster-issuer"] != certificateOnlyIssuer {
		t.Fatalf("certificate-only ingress must ask the configured issuer: %+v", ing.Annotations)
	}
	shape, err := kube("-n", ns, "get", "ingress", "tunnel", "-o", "jsonpath={range .spec.rules[*]}rule host={.host} http={.http}{end} tls={.spec.tls}")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("observed ingress:", shape)

	// cert-manager's ingress-shim must issue into the ingress TLS secret.
	uid, err := waitCertificateReady(ctx, kube, ns, "hakopod-tls-tunnel")
	if err != nil {
		t.Fatal("cert-manager never reported the certificate ready:", err)
	}
	created, _ := kube("-n", ns, "get", "certificate", "hakopod-tls-tunnel", "-o", "jsonpath={.metadata.creationTimestamp}")
	t.Logf("certificate hakopod-tls-tunnel ready, uid %s created %s", uid, created)
	issued, err := c.kube.CoreV1().Secrets(ns).Get(ctx, "hakopod-tls-tunnel", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if issued.Type != corev1.SecretTypeTLS {
		t.Fatalf("issued secret has type %q, want kubernetes.io/tls", issued.Type)
	}
	block, _ := pem.Decode(issued.Data[corev1.TLSCertKey])
	if block == nil {
		t.Fatal("issued secret does not contain a PEM certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = leaf.VerifyHostname(host); err != nil {
		t.Fatalf("issued certificate does not cover %s: %v (SANs %v)", host, err, leaf.DNSNames)
	}
	t.Logf("issued certificate subject alternative names %v, not after %s", leaf.DNSNames, leaf.NotAfter.Format(time.RFC3339))

	// The mount has to arrive through hakopod's resolution path, not by hand.
	resolved, err := c.resolveBackendCertificates(ctx, target, "tunnel", svc, false)
	if err != nil {
		t.Fatal("resolution rejected the certificate-only ingress source:", err)
	}
	wantMount := automaticCertificateName("tunnel", host, issued.Data[corev1.TLSCertKey])
	if len(resolved.CertificateMounts) != 1 || resolved.CertificateMounts[0].Certificate != wantMount {
		t.Fatalf("resolution did not select the issued certificate: %+v", resolved.CertificateMounts)
	}
	generation, err := c.applyDeployment(ctx, target, "tunnel", svc)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.waitReady(ctx, target, "tunnel", generation); err != nil {
		t.Fatal(err)
	}
	digest, err := kube("-n", ns, "exec", "deployment/tunnel", "--", "python", "-B", "-c",
		"import hashlib;print(hashlib.sha256(open('/certificates/tunnel/tls.crt','rb').read()).hexdigest())")
	if err != nil || digest != fmt.Sprintf("%x", sha256.Sum256(issued.Data[corev1.TLSCertKey])) {
		t.Fatalf("issued certificate did not reach the mount: %v %s", err, digest)
	}
	t.Log("mounted /certificates/tunnel/tls.crt sha256", digest)

	// The failure mode this design avoids: destroying the ingress on every
	// release, which would re-request the certificate each time.
	for release := 2; release <= 4; release++ {
		target.Revision, target.OperationID = int64(release), fmt.Sprintf("certificate-only-release-%d", release)
		if err = c.reconcilePrivateIngress(ctx, target, "tunnel", target.Spec.Services["tunnel"]); err != nil {
			t.Fatal(err)
		}
	}
	again, err := c.kube.NetworkingV1().Ingresses(ns).Get(ctx, "tunnel", metav1.GetOptions{})
	if err != nil {
		t.Fatal("repeated reconciliation removed the certificate-only ingress:", err)
	}
	if again.UID != ing.UID {
		t.Fatalf("ingress was replaced across releases: %s then %s", ing.UID, again.UID)
	}
	if len(again.Spec.Rules) != 1 || again.Spec.Rules[0].HTTP != nil || len(again.Spec.TLS) != 1 {
		t.Fatalf("repeated reconciliation changed the ingress shape: %+v %+v", again.Spec.Rules, again.Spec.TLS)
	}
	uidAfter, err := kube("-n", ns, "get", "certificate", "hakopod-tls-tunnel", "-o", "jsonpath={.metadata.uid}")
	if err != nil {
		t.Fatal(err)
	}
	createdAfter, err := kube("-n", ns, "get", "certificate", "hakopod-tls-tunnel", "-o", "jsonpath={.metadata.creationTimestamp}")
	if err != nil {
		t.Fatal(err)
	}
	if uidAfter != uid || createdAfter != created {
		t.Fatalf("certificate was re-requested: uid %s/%s created %s/%s", uid, uidAfter, created, createdAfter)
	}
	t.Logf("after three further releases the certificate is unchanged: uid %s created %s", uidAfter, createdAfter)
}

// waitCertificateReady polls the issued Certificate within the caller's
// deadline and returns its UID once cert-manager reports Ready.
func waitCertificateReady(ctx context.Context, kube func(...string) (string, error), namespace, name string) (string, error) {
	last := "no certificate yet"
	for attempt := 0; attempt < 90; attempt++ {
		ready, err := kube("-n", namespace, "get", "certificate", name, "-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}")
		if err == nil {
			last = "Ready=" + ready
			if ready == "True" {
				return kube("-n", namespace, "get", "certificate", name, "-o", "jsonpath={.metadata.uid}")
			}
		} else {
			last = ready
		}
		if err = sleepContext(ctx, 2*time.Second); err != nil {
			return "", fmt.Errorf("%w (last: %s)", err, last)
		}
	}
	return "", fmt.Errorf("certificate %s never became ready (last: %s)", name, last)
}
