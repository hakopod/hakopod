package cluster

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// TestLiveBackendHTTP2 proves the proxy really speaks HTTP/2 to a service that
// asks for it, and returns to HTTP/1.1 once the service opts out again. The
// fixture reports the protocol it actually received, so the observation comes
// from the backend rather than from the generated configuration.
func TestLiveBackendHTTP2(t *testing.T) {
	if os.Getenv("HAKOPOD_BACKEND_HTTP2_TEST") != "1" {
		t.Skip("set HAKOPOD_BACKEND_HTTP2_TEST=1 and HAKOPOD_TEST_KUBECONFIG for the named development ingress")
	}
	kubeconfig := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing backend HTTP/2 test outside named development cluster")
	}
	c, err := New(kubeconfig, Options{AppDomain: "127.0.0.1.sslip.io", IngressClass: "haproxy", PublicPort: 18080, RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	app, err := spec.Normalize(spec.Application{Name: "backend-http2", Services: map[string]spec.Service{"echo": {
		Image:        "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a",
		Port:         8080,
		Public:       true,
		BackendHTTP2: true,
		Command:      []string{"python", "-B", "-c", backendHTTP2Fixture},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "backend-http2-" + strconv.FormatInt(time.Now().UnixNano(), 10), Project: "backend-http2-test", Environment: "test", OperationID: "initial", Revision: 1, Spec: app}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 60*time.Second)
		defer done()
		ns, e := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
		if e == nil && owned(ns, target) == nil {
			if e = c.kube.CoreV1().Namespaces().Delete(cleanup, ns.Name, deleteOptions(ns)); e != nil {
				t.Error(e)
			}
		}
	})
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	host := c.hostname(target, "echo")
	ingress, err := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "echo", metav1.GetOptions{})
	if err != nil || ingress.Annotations["haproxy.org/server-proto"] != "h2" {
		t.Fatal("deployed ingress did not request backend HTTP/2", err)
	}
	observed, err := waitForBackendProtocol(ctx, host, "HTTP/2.0")
	if err != nil {
		t.Fatal("backend HTTP/2 was never observed at the service:", err)
	}
	t.Logf("with backend_http2 = true the service received %q", observed)
	// Reconcile the same service with the field off; applyIngress must clear the
	// annotation on an existing Ingress, not only on a freshly created one.
	svc := target.Spec.Services["echo"]
	svc.BackendHTTP2 = false
	target.Spec.Services["echo"] = svc
	target.Revision, target.OperationID = 2, "opt-out"
	if err = c.applyIngress(ctx, target, "echo", svc); err != nil {
		t.Fatal(err)
	}
	ingress, err = c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "echo", metav1.GetOptions{})
	if err != nil || ingress.Annotations["haproxy.org/server-proto"] != "" {
		t.Fatal("reconcile left the backend HTTP/2 annotation in place", err)
	}
	downgraded, err := waitForBackendProtocol(ctx, host, "HTTP/1.1")
	if err != nil {
		t.Fatal("backend never returned to HTTP/1.1 after opting out:", err)
	}
	t.Logf("after clearing backend_http2 the service received %q", downgraded)
}

// waitForBackendProtocol drives requests through the development ingress until
// the fixture reports the wanted protocol, bounded by the caller's context.
func waitForBackendProtocol(ctx context.Context, host, want string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	defer client.CloseIdleConnections()
	last := "no response"
	for attempt := 0; attempt < 60; attempt++ {
		request, err := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:18080/", nil)
		if err != nil {
			return "", err
		}
		request.Host = host
		response, err := client.Do(request)
		if err != nil {
			last = err.Error()
		} else {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 256))
			response.Body.Close()
			last = fmt.Sprintf("HTTP %d %q", response.StatusCode, body)
			if response.StatusCode == 200 && strings.TrimSpace(string(body)) == want {
				return want, nil
			}
		}
		if err = sleepContext(ctx, time.Second); err != nil {
			return "", fmt.Errorf("%w (last: %s)", err, last)
		}
	}
	return "", fmt.Errorf("never reported %s (last: %s)", want, last)
}

// backendHTTP2Fixture serves both h2c and HTTP/1.1 on one plain port using only
// the standard library, and answers with the protocol the connection used. The
// HTTP/2 reply needs no HPACK table: static index 8 is ":status: 200".
const backendHTTP2Fixture = `import socket, threading
PREFACE = b'PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n'
def frame(kind, flags, stream, payload=b''):
 return len(payload).to_bytes(3, 'big') + bytes([kind, flags]) + stream.to_bytes(4, 'big') + payload
def http1(conn, seen):
 while b'\r\n\r\n' not in seen:
  chunk = conn.recv(4096)
  if not chunk: return
  seen += chunk
 body = b'HTTP/1.1'
 conn.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s' % (len(body), body))
def h2c(conn):
 conn.sendall(frame(4, 0, 0))
 buf = b''
 while True:
  while len(buf) < 9:
   chunk = conn.recv(4096)
   if not chunk: return
   buf += chunk
  length = int.from_bytes(buf[:3], 'big')
  kind, flags = buf[3], buf[4]
  stream = int.from_bytes(buf[5:9], 'big') & 0x7fffffff
  while len(buf) < 9 + length:
   chunk = conn.recv(4096)
   if not chunk: return
   buf += chunk
  buf = buf[9 + length:]
  if kind == 4 and not flags & 1:
   conn.sendall(frame(4, 1, 0))
  elif kind == 1:
   conn.sendall(frame(1, 4, stream, b'\x88') + frame(0, 1, stream, b'HTTP/2.0') + frame(7, 0, 0, stream.to_bytes(4, 'big') + b'\x00\x00\x00\x00'))
   return
def serve(conn):
 try:
  conn.settimeout(10)
  seen = b''
  while len(seen) < len(PREFACE):
   chunk = conn.recv(len(PREFACE) - len(seen))
   if not chunk: return
   seen += chunk
   if not PREFACE.startswith(seen): break
  h2c(conn) if seen == PREFACE else http1(conn, seen)
 except (OSError, ValueError): pass
 finally: conn.close()
listener = socket.socket()
listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
listener.bind(('0.0.0.0', 8080))
listener.listen(16)
while True:
 threading.Thread(target=serve, args=(listener.accept()[0],), daemon=True).start()
`
