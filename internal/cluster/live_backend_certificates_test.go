package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveBackendCertificateMountRotation(t *testing.T) {
	if os.Getenv("HAKOPOD_BACKEND_CERTIFICATES_TEST") != "1" {
		t.Skip("set HAKOPOD_BACKEND_CERTIFICATES_TEST=1 for the isolated development certificate fixture")
	}
	kubeconfig := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("certificate acceptance requires the named k3d-hakopod-dev context")
	}
	c, err := New(kubeconfig, Options{RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	app, err := spec.Normalize(spec.Application{Name: "backend-certificates", Services: map[string]spec.Service{"smtp": {
		Image: "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a", RunAsUser: 12345, RunAsGroup: 23456, FSGroup: 23456, ReadOnlyRootFilesystem: true,
		Command: []string{"python", "-B", "-c", "import time; time.sleep(600)"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "backend-certs-" + strconv.FormatInt(time.Now().UnixNano(), 10), Project: "certificate-test", Environment: "test", OperationID: "certificates-initial", Revision: 1, Spec: app}
	if err = c.bootstrap(ctx, target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		namespace, e := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
		if e == nil && owned(namespace, target) == nil {
			if e = c.kube.CoreV1().Namespaces().Delete(cleanup, namespace.Name, deleteOptions(namespace)); e != nil {
				t.Error(e)
			}
		}
	})
	hostname := "smtp.fixture.example.com"
	cert, key := testTLSCertificate(t, hostname, time.Now().Add(time.Hour))
	initial, err := c.PutBackendCertificate(ctx, target, "smtp", hostname, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	deploy := func(reference string) {
		t.Helper()
		svc := target.Spec.Services["smtp"]
		svc.CertificateMounts = []spec.CertificateMount{{Certificate: reference, Hostname: hostname, MountPath: "/certificates/smtp"}}
		target.Spec.Services["smtp"] = svc
		if observed, err := c.Deploy(ctx, target, nil); err != nil {
			t.Fatal(err)
		} else if observed.Status != "healthy" {
			t.Fatalf("ready certificate workload was not observed healthy: %+v", observed)
		}
	}
	probe := func(expectedCert []byte) {
		t.Helper()
		code := `import os,stat,ssl,hashlib,errno
assert os.getuid()==12345 and os.getgid()==23456
for p in ['/certificates/smtp/tls.crt','/certificates/smtp/tls.key']:
 s=os.stat(p)
 assert s.st_gid==23456 and stat.S_IMODE(s.st_mode)==0o440
 assert os.access(p,os.R_OK) and not os.access(p,os.W_OK)
 try: open(p,'wb'); raise AssertionError('certificate is writable')
 except OSError as e: assert e.errno in (errno.EROFS,errno.EACCES)
ctx=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain('/certificates/smtp/tls.crt','/certificates/smtp/tls.key')
assert not os.path.exists('/var/run/secrets/kubernetes.io/serviceaccount/token')
print(hashlib.sha256(open('/certificates/smtp/tls.crt','rb').read()).hexdigest())
`
		command := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "--context", "k3d-hakopod-dev", "-n", Namespace(target.ApplicationID), "exec", "deployment/smtp", "--", "python", "-B", "-c", code)
		out, err := command.CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != fmt.Sprintf("%x", sha256.Sum256(expectedCert)) {
			t.Fatalf("certificate permission/load probe failed: %v; %s", err, out)
		}
	}
	deploy(initial.Certificate)
	probe(cert)
	cert2, key2 := testTLSCertificate(t, hostname, time.Now().Add(2*time.Hour))
	rotated, err := c.PutBackendCertificate(ctx, target, "smtp", hostname, cert2, key2)
	if err != nil {
		t.Fatal(err)
	}
	target.Revision++
	deploy(rotated.Certificate)
	probe(cert2)
	target.Revision++
	deploy(initial.Certificate)
	probe(cert)
	t.Log("Non-root TLS loading, read-only 0440 certificate files, explicit rotation and rollback verified in the named development cluster")
}
