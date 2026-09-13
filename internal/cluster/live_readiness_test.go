package cluster

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveSMTPReadiness(t *testing.T) {
	if os.Getenv("HAKOPOD_READINESS_TEST") != "1" {
		t.Skip("set HAKOPOD_READINESS_TEST=1 and HAKOPOD_TEST_READINESS_IMAGE for an isolated SMTP readiness fixture")
	}
	kubeconfig := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("SMTP readiness acceptance requires k3d-hakopod-dev")
	}
	image := os.Getenv("HAKOPOD_TEST_READINESS_IMAGE")
	if image == "" || ValidateReadinessProbeImage(image) != nil {
		t.Fatal("provide an imported, digest-pinned HAKOPOD_TEST_READINESS_IMAGE")
	}
	c, err := New(kubeconfig, Options{RolloutTimeout: 90 * time.Second, ReadinessProbeImage: image})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	app, err := spec.Normalize(spec.Application{Name: "smtp-readiness", Services: map[string]spec.Service{"smtp": {
		Image: "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a", Port: 8080, Healthcheck: "/health", Ports: []spec.Port{{Name: "smtp", Port: 2525, TargetPort: 2525, Protocol: "TCP"}},
		Readiness: &spec.Readiness{Protocol: "smtp_starttls", Port: 2525, TLSServerName: "smtp.fixture.example.com", TLSCAFile: "/certificates/smtp/tls.crt", PeriodSeconds: 3, TimeoutSeconds: 2, FailureThreshold: 1},
		RunAsUser: 12345, RunAsGroup: 23456, FSGroup: 23456, ReadOnlyRootFilesystem: true, TemporaryMounts: []spec.TemporaryMount{{MountPath: "/control", SizeMiB: 1}}, Command: []string{"python", "-B", "-c", readinessFixturePython},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "smtp-readiness-" + strconv.FormatInt(time.Now().UnixNano(), 10), Project: "readiness-test", Environment: "test", OperationID: "readiness-test", Revision: 1, Spec: app}
	if err := c.bootstrap(ctx, target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		ns, e := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
		if e == nil && owned(ns, target) == nil {
			if e = c.kube.CoreV1().Namespaces().Delete(cleanup, ns.Name, deleteOptions(ns)); e != nil {
				t.Error(e)
			}
		}
	})
	cert, key := testTLSCertificate(t, "smtp.fixture.example.com", time.Now().Add(time.Hour))
	uploaded, err := c.PutBackendCertificate(ctx, target, "smtp", "smtp.fixture.example.com", cert, key)
	if err != nil {
		t.Fatal(err)
	}
	svc := target.Spec.Services["smtp"]
	svc.CertificateMounts = []spec.CertificateMount{{Certificate: uploaded.Certificate, Hostname: "smtp.fixture.example.com", MountPath: "/certificates/smtp"}}
	target.Spec.Services["smtp"] = svc
	if err := c.ValidateDelivery(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := c.applyService(ctx, target, "smtp", svc); err != nil {
		t.Fatal(err)
	}
	if _, err := c.applyDeployment(ctx, target, "smtp", svc); err != nil {
		t.Fatal(err)
	}
	var podName string
	wait := func(ready bool, requireStarted bool) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			pods, e := c.kube.CoreV1().Pods(Namespace(target.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: ownerKey + "=" + ownerID(target.ApplicationID), Limit: 10})
			if e != nil {
				t.Fatal(e)
			}
			for _, p := range pods.Items {
				if p.DeletionTimestamp != nil || len(p.Status.ContainerStatuses) != 1 {
					continue
				}
				status := p.Status.ContainerStatuses[0]
				if status.State.Running == nil || requireStarted && (status.Started == nil || !*status.Started) {
					continue
				}
				if status.Ready == ready {
					podName = p.Name
					return
				}
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("pod did not become ready=%v", ready)
	}
	execPython := func(code string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "--context", "k3d-hakopod-dev", "-n", Namespace(target.ApplicationID), "exec", podName, "-c", "app", "--", "python", "-B", "-c", code)
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("fixture control failed: %v: %s", e, out)
		}
		return strings.TrimSpace(string(out))
	}
	endpointReady := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			slices, e := c.kube.DiscoveryV1().EndpointSlices(Namespace(target.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=smtp", Limit: 20})
			if e != nil {
				t.Fatal(e)
			}
			found := false
			for _, slice := range slices.Items {
				for _, ep := range slice.Endpoints {
					found = found || ep.Conditions.Ready != nil && *ep.Conditions.Ready
				}
			}
			if found == want {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("Service endpoints did not become ready=%v", want)
	}
	wait(false, true)
	if result := execPython("import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:8080/health').status)"); result != "200" {
		t.Fatalf("HTTP fixture: %s", result)
	}
	time.Sleep(4 * time.Second)
	wait(false, true)
	endpointReady(false)
	execPython("from pathlib import Path; Path('/control/smtp-ready').touch()")
	wait(true, true)
	endpointReady(true)
	execPython("from pathlib import Path; Path('/control/smtp-ready').unlink()")
	wait(false, true)
	endpointReady(false)
	execPython("from pathlib import Path; Path('/control/smtp-ready').touch()")
	wait(true, true)
	endpointReady(true)
	execPython("from pathlib import Path; Path('/control/http-failed').touch()")
	wait(false, true)
	endpointReady(false)
	execPython("from pathlib import Path; Path('/control/http-failed').unlink()")
	wait(true, true)
	endpointReady(true)
	out := execPython(`import os,subprocess
assert os.getuid()==12345 and os.getgid()==23456
assert not os.path.exists('/var/run/secrets/kubernetes.io/serviceaccount/token')
args=['/var/run/secrets/hakopod-probe/hakopod-probe','--protocol=smtp_starttls','--port=2525','--timeout=2s','--tls-server-name=wrong.fixture.example.com','--tls-ca-file=/certificates/smtp/tls.crt']
r=subprocess.run(args,capture_output=True,text=True)
assert r.returncode!=0 and 'certificate or handshake' in r.stderr
print('verified')`)
	if out != "verified" {
		t.Fatal(out)
	}
	pod, err := c.kube.CoreV1().Pods(Namespace(target.ApplicationID)).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 1 || pod.Status.ContainerStatuses[0].RestartCount != 0 || pod.Status.Phase != corev1.PodRunning {
		t.Fatal("readiness added a sidecar or restarted the workload")
	}
	t.Log(fmt.Sprintf("Verified SMTP startup, listener failure/recovery, HTTP failure/recovery, STARTTLS certificate checks, endpoint readiness and nonroot helper with no sidecar or email sends (%s)", podName))
}

const readinessFixturePython = `import http.server,os,pathlib,socket,ssl,threading,time
class HTTP(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(503 if os.path.exists('/control/http-failed') else 200); self.end_headers()
 def log_message(self,*args): pass
def smtp(conn):
 try:
  conn.settimeout(2); conn.sendall(b'220 fixture ready\r\n'); stream=conn.makefile('rb')
  while True:
   line=stream.readline(1024)
   if not line: break
   if line.startswith(b'EHLO '): conn.sendall(b'250-fixture\r\n250 STARTTLS\r\n')
   elif line==b'STARTTLS\r\n':
    conn.sendall(b'220 Ready for TLS\r\n'); stream.close()
    context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER); context.load_cert_chain('/certificates/smtp/tls.crt','/certificates/smtp/tls.key')
    conn=context.wrap_socket(conn,server_side=True); stream=conn.makefile('rb')
   elif line==b'NOOP\r\n': conn.sendall(b'250 OK\r\n')
   else: conn.sendall(b'500 Not supported\r\n'); break
 except (OSError,ValueError): pass
 finally: conn.close()
def listener():
 while True:
  if not os.path.exists('/control/smtp-ready'): time.sleep(0.1); continue
  with socket.socket() as server:
   server.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); server.bind(('0.0.0.0',2525)); server.listen(8); server.settimeout(0.2)
   while os.path.exists('/control/smtp-ready'):
    try: conn,_=server.accept()
    except socket.timeout: continue
    threading.Thread(target=smtp,args=(conn,),daemon=True).start()
threading.Thread(target=listener,daemon=True).start()
http.server.ThreadingHTTPServer(('0.0.0.0',8080),HTTP).serve_forever()
`
