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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveVirtualNetworksAcrossApplications(t *testing.T) {
	if os.Getenv("HAKOPOD_VIRTUAL_NETWORK_TEST") != "1" {
		t.Skip("set HAKOPOD_VIRTUAL_NETWORK_TEST=1 for isolated cross-application network acceptance")
	}
	kubeconfig := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("virtual network acceptance requires the named k3d-hakopod-dev context")
	}
	c, err := New(kubeconfig, Options{RolloutTimeout: 90 * time.Second, VirtualNetworks: func(_ context.Context, _, _ string, app spec.Application) (map[string]string, error) {
		result := map[string]string{}
		for local, network := range app.Networks {
			if network.VirtualNetwork != "" {
				result[local] = "isolated-fixture-network/" + network.Segment
			}
		}
		return result, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	image := "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a"
	serverCode := `import http.server,socket,threading,time
def serve_http(port): http.server.ThreadingHTTPServer(('0.0.0.0',port),http.server.SimpleHTTPRequestHandler).serve_forever()
def udp():
 s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind(('0.0.0.0',9998))
 while True:
  data,addr=s.recvfrom(100);s.sendto(data,addr)
threading.Thread(target=serve_http,args=(8080,),daemon=True).start()
threading.Thread(target=serve_http,args=(8081,),daemon=True).start()
threading.Thread(target=udp,daemon=True).start()
while True: time.sleep(1)
`
	stamp := strconv.FormatInt(time.Now().UnixNano(), 10)
	targets := map[string]Target{}
	makeTarget := func(name, environment, segment string, services map[string]spec.Service) Target {
		t.Helper()
		app, err := spec.Normalize(spec.Application{Name: name, Networks: map[string]spec.Network{"private": {Internal: true, VirtualNetwork: "commerce", Segment: segment}}, Services: services})
		if err != nil {
			t.Fatal(err)
		}
		target := Target{ApplicationID: "vnet-fixture-" + name + "-" + environment + "-" + segment + "-" + stamp, Project: "network-fixture", Environment: environment, OperationID: "vnet-initial", Revision: 1, Spec: app}
		t.Cleanup(func() {
			cleanup, done := context.WithTimeout(context.Background(), 25*time.Second)
			defer done()
			ns, err := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
			if err == nil {
				if err := owned(ns, target); err != nil {
					t.Error(err)
					return
				}
				if err := c.kube.CoreV1().Namespaces().Delete(cleanup, ns.Name, deleteOptions(ns)); err != nil {
					t.Error(err)
				}
			}
		})
		if _, err := c.Deploy(ctx, target, nil); err != nil {
			inspect, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			pods, _ := c.kube.CoreV1().Pods(Namespace(target.ApplicationID)).List(inspect, metav1.ListOptions{Limit: 10})
			if pods != nil {
				for _, pod := range pods.Items {
					t.Logf("fixture pod %s: phase=%s conditions=%+v containers=%+v", pod.Name, pod.Status.Phase, pod.Status.Conditions, pod.Status.ContainerStatuses)
					out, _ := exec.CommandContext(inspect, "kubectl", "--kubeconfig", kubeconfig, "--context", "k3d-hakopod-dev", "-n", pod.Namespace, "logs", pod.Name, "--tail=30").CombinedOutput()
					t.Logf("fixture log: %s", out)
				}
			}
			t.Fatal(err)
		}
		return target
	}
	worker := spec.Service{Image: image, Networks: []string{"private"}, Command: []string{"python", "-B", "-c", "import time;time.sleep(600)"}, ReadOnlyRootFilesystem: true}
	targets["server"] = makeTarget("database", "test", "data", map[string]spec.Service{"db": {Image: image, Networks: []string{"private"}, Port: 8080, Ports: []spec.Port{{Name: "sql", Port: 9090, TargetPort: 8081, Protocol: "TCP"}, {Name: "discovery", Port: 9999, TargetPort: 9998, Protocol: "UDP"}}, Command: []string{"python", "-B", "-u", "-c", serverCode}, NetworkAccess: &spec.NetworkAccess{From: []string{}, FromApplications: []string{"orders/worker"}}, ReadOnlyRootFilesystem: true}})
	targets["allowed"] = makeTarget("orders", "test", "data", map[string]spec.Service{"worker": worker, "unlisted": worker})
	targets["segment"] = makeTarget("orders", "test", "frontend", map[string]spec.Service{"worker": worker})
	targets["environment"] = makeTarget("orders", "production", "data", map[string]spec.Service{"worker": worker})
	server := targets["server"]
	host := "db." + Namespace(server.ApplicationID) + ".svc.cluster.local"
	probe := func(which, service string, port int, protocol string, want bool) {
		t.Helper()
		code := `import socket,sys
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM if sys.argv[3]=='UDP' else socket.SOCK_STREAM);s.settimeout(2)
try:
 s.connect((sys.argv[1],int(sys.argv[2])))
 if sys.argv[3]=='UDP':s.send(b'probe');assert s.recv(100)==b'probe'
 print('connected')
except OSError:print('blocked')
finally:s.close()
`
		argv := []string{"--kubeconfig", kubeconfig, "--context", "k3d-hakopod-dev", "-n", Namespace(targets[which].ApplicationID), "exec", "deployment/" + service, "--", "python", "-B", "-c", code, host, strconv.Itoa(port), protocol}
		out, err := exec.CommandContext(ctx, "kubectl", argv...).CombinedOutput()
		if err != nil || (strings.TrimSpace(string(out)) == "connected") != want {
			t.Fatalf("%s/%s %s:%d/%s expected allowed=%v: %v %s", which, service, host, port, protocol, want, err, out)
		}
	}
	for _, protocol := range []string{"TCP", "UDP"} {
		port := 9090
		if protocol == "UDP" {
			port = 9999
		}
		probe("allowed", "worker", port, protocol, true)
		probe("allowed", "unlisted", port, protocol, false)
		probe("segment", "worker", port, protocol, false)
		probe("environment", "worker", port, protocol, false)
	}
	service, err := c.kube.CoreV1().Services(Namespace(server.ApplicationID)).Get(ctx, "db", metav1.GetOptions{})
	if err != nil || string(service.Spec.Type) != "ClusterIP" || len(service.Spec.Ports) != 3 {
		t.Fatal("shared service is not private", err)
	}
	next := server.Spec.Services["db"]
	next.NetworkAccess.FromApplications = []string{}
	server.Spec.Services["db"] = next
	server.Revision++
	server.OperationID = "vnet-revoke"
	if _, err := c.Deploy(ctx, server, nil); err != nil {
		t.Fatal(err)
	}
	probe("allowed", "worker", 9090, "TCP", false)
	probe("allowed", "worker", 9999, "UDP", false)
	t.Log(fmt.Sprintf("Cross-application DNS, private TCP/UDP mapping, service peer restriction, segment/environment isolation and access removal verified across %d isolated applications", len(targets)))
}
