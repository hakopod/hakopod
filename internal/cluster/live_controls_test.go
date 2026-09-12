package cluster

import (
	"context"
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

func TestLiveMountsPortsAndPeerPermissions(t *testing.T) {
	if os.Getenv("HAKOPOD_CONTROLS_TEST") != "1" {
		t.Skip("set HAKOPOD_CONTROLS_TEST=1 for isolated development runtime acceptance")
	}
	kubeconfig := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	if config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing runtime acceptance outside the named development cluster")
	}
	c, err := New(kubeconfig, Options{RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	image := "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a"
	serverCode := `import http.server,socket,threading,time
def serve_http(port):
 http.server.ThreadingHTTPServer(('0.0.0.0',port),http.server.SimpleHTTPRequestHandler).serve_forever()
def udp():
 s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind(('0.0.0.0',9998))
 while True:
  data,addr=s.recvfrom(100);s.sendto(data,addr)
threading.Thread(target=serve_http,args=(8080,),daemon=True).start()
threading.Thread(target=serve_http,args=(8081,),daemon=True).start()
threading.Thread(target=udp,daemon=True).start()
while True: time.sleep(1)
`
	app, err := spec.Normalize(spec.Application{Name: "runtime-controls", Networks: map[string]spec.Network{"private": {Internal: true}}, Volumes: map[string]spec.NamedVolume{"data": {SizeGiB: 1}}, Services: map[string]spec.Service{
		"server": {Image: image, Port: 8080, Networks: []string{"private"}, Command: []string{"python", "-B", "-u", "-c", serverCode}, RunAsUser: 12345, RunAsGroup: 23456, FSGroup: 23456, ReadOnlyRootFilesystem: true, WorkingDir: "/data", TerminationGraceSeconds: 35,
			Ports:  []spec.Port{{Name: "metrics", Port: 9090, TargetPort: 8081, Protocol: "TCP"}, {Name: "discovery", Port: 9999, TargetPort: 9998, Protocol: "UDP"}},
			Mounts: []spec.Mount{{Volume: "data", MountPath: "/data"}, {Volume: "data", MountPath: "/readback", ReadOnly: true}}, TemporaryMounts: []spec.TemporaryMount{{MountPath: "/tmp", SizeMiB: 8, Memory: true}}, NetworkAccess: &spec.NetworkAccess{From: []string{"client"}}},
		"client": {Image: image, Networks: []string{"private"}, ReadOnlyRootFilesystem: true, Command: []string{"python", "-B", "-c", "import time; time.sleep(600)"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "runtime-controls-" + strconv.FormatInt(time.Now().UnixNano(), 10), Project: "controls-test", Environment: "test", OperationID: "controls-initial", Revision: 1, Spec: app}
	ns := Namespace(target.ApplicationID)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 25*time.Second)
		defer done()
		namespace, e := c.kube.CoreV1().Namespaces().Get(cleanup, ns, metav1.GetOptions{})
		if e == nil {
			if e = owned(namespace, target); e != nil {
				t.Error(e)
				return
			}
			if e = c.kube.CoreV1().Namespaces().Delete(cleanup, ns, deleteOptions(namespace)); e != nil {
				t.Error(e)
			}
		}
	})
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		pods, _ := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: 10})
		for _, p := range pods.Items {
			t.Logf("fixture pod %s: phase=%s conditions=%+v containers=%+v", p.Name, p.Status.Phase, p.Status.Conditions, p.Status.ContainerStatuses)
		}
		events, _ := c.kube.CoreV1().Events(ns).List(ctx, metav1.ListOptions{Limit: 30})
		for _, e := range events.Items {
			t.Logf("fixture event %s: %s", e.Reason, e.Message)
		}
		t.Fatal(err)
	}
	execute := func(service, code string, args ...string) string {
		t.Helper()
		argv := []string{"--kubeconfig", kubeconfig, "--context", "k3d-hakopod-dev", "-n", ns, "exec", "deployment/" + service, "--", "python", "-B", "-c", code}
		argv = append(argv, args...)
		out, e := exec.CommandContext(ctx, "kubectl", argv...).CombinedOutput()
		if e != nil {
			t.Fatalf("runtime fixture execution: %v %s", e, out)
		}
		return strings.TrimSpace(string(out))
	}
	verifyCode := `import os,errno
assert os.getuid()==12345 and os.getgid()==23456
assert os.getcwd()=='/data'
open('/data/probe','w').write('survives')
assert open('/readback/probe').read()=='survives'
open('/tmp/probe','w').write('temporary')
for p in ['/readback/cannot-write','/cannot-write']:
 try: open(p,'w').write('bad'); raise AssertionError('write allowed: '+p)
 except OSError as e: assert e.errno in (errno.EROFS,errno.EACCES)
print('mounts and identities enforced')
`
	if got := execute("server", verifyCode); got != "mounts and identities enforced" {
		t.Fatal(got)
	}
	t.Log("Real read-only root and data mount, writable bounded tmpfs, UID/GID and working directory verified")
	probe := func(host string, port int, protocol string, want bool) {
		t.Helper()
		code := `import socket,sys
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM if sys.argv[3]=='UDP' else socket.SOCK_STREAM);s.settimeout(2)
try:
 s.connect((sys.argv[1],int(sys.argv[2])))
 if sys.argv[3]=='UDP': s.send(b'probe');assert s.recv(100)==b'probe'
 print('connected')
except OSError: print('blocked')
finally: s.close()
`
		result := execute("client", code, host, strconv.Itoa(port), protocol)
		if (result == "connected") != want {
			t.Fatalf("%s:%d/%s expected allowed=%v, got %s", host, port, protocol, want, result)
		}
	}
	probe("server", 9090, "TCP", true)
	probe("server", 9999, "UDP", true)
	service, err := c.kube.CoreV1().Services(ns).Get(ctx, "server", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(service.Spec.Ports) != 3 || service.Spec.Type != "ClusterIP" {
		t.Fatal("private ports were not provisioned correctly")
	}
	claim, err := c.kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, "hakopod-volume-data", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	server := target.Spec.Services["server"]
	server.RestartNonce = "verify-data-retained"
	server.NetworkAccess = &spec.NetworkAccess{From: []string{}}
	target.Spec.Services["server"] = server
	target.Revision++
	target.OperationID = "controls-deny"
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	probe("server", 9090, "TCP", false)
	probe("server", 9999, "UDP", false)
	if execute("server", "print(open('/data/probe').read())") != "survives" {
		t.Fatal("data lost after deployment")
	}
	next, err := c.kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, claim.Name, metav1.GetOptions{})
	if err != nil || next.UID != claim.UID {
		t.Fatal("deployment replaced persistent volume", err)
	}
	t.Log("Private TCP/UDP target-port routing, explicit peer deny and persistent claim reuse verified")
}
