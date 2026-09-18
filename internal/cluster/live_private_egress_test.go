package cluster

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// The external fixture must be a disposable TCP listener on ports 5432 and 5433
// in the named development cluster's Docker network, never an actual database.
func TestLivePrivateEgress(t *testing.T) {
	if os.Getenv("HAKOPOD_CLUSTER_TEST") != "1" {
		t.Skip("requires explicit isolated development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatalf("refusing context %q", config.CurrentContext)
	}
	host := os.Getenv("HAKOPOD_TEST_PRIVATE_EGRESS_IP")
	addr, err := netip.ParseAddr(host)
	if err != nil || !addr.Is4() || !addr.IsPrivate() {
		t.Fatal("explicit private development fixture IPv4 required")
	}
	c, err := New(path, Options{RolloutTimeout: 90 * time.Second, PolicySettleTime: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	image := "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a"
	service := spec.Service{Image: image, Command: []string{"python", "-c", "import time; time.sleep(600)"}}
	app, err := spec.Normalize(spec.Application{Name: "private-egress-test", Services: map[string]spec.Service{"api": service, "worker": service}, Networks: map[string]spec.Network{"default": {Internal: true}}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "private-egress-integration-dedicated-v1", Project: "runtime-test", Environment: "test", OperationID: "test-private-egress", Revision: 1, Spec: app}
	ns := Namespace(target.ApplicationID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		n, err := c.kube.CoreV1().Namespaces().Get(cleanup, ns, metav1.GetOptions{})
		if err == nil {
			if owned(n, target) != nil {
				t.Error("refusing unowned cleanup")
				return
			}
			if err := c.kube.CoreV1().Namespaces().Delete(cleanup, ns, deleteOptions(n)); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err := c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	probe := func(service string, port int, want bool) {
		t.Helper()
		items, err := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: serviceKey + "=" + service, Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		pod := ""
		for _, p := range items.Items {
			if podReady(p) && p.DeletionTimestamp == nil {
				pod = p.Name
				break
			}
		}
		if pod == "" {
			t.Fatal("ready fixture pod missing")
		}
		code := "import socket,sys\ntry:\n socket.create_connection((sys.argv[1],int(sys.argv[2])),2).close();print('connected')\nexcept OSError:\n print('blocked')\n"
		out, err := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "-n", ns, "exec", pod, "--", "python", "-c", code, host, fmt.Sprint(port)).CombinedOutput()
		if err != nil {
			t.Fatalf("probe: %v %s", err, out)
		}
		if got := strings.TrimSpace(string(out)) == "connected"; got != want {
			t.Fatalf("%s -> fixture:%d want %v got %s", service, port, want, out)
		}
		t.Logf("%s -> fixture:%d allowed=%v", service, port, want)
	}
	probe("api", 5432, false)
	b := privateBindingFixture(target)
	b.CIDRs = []string{host + "/32"}
	c.options.PrivateEgressBindings = []PrivateEgressBinding{b}
	target = withPrivateRef(target)
	target.Revision++
	if _, err := c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	probe("api", 5432, true)
	probe("api", 5433, false)
	probe("worker", 5432, false)
	s := target.Spec.Services["api"]
	s.PrivateEgress = nil
	target.Spec.Services["api"] = s
	target.Revision++
	if _, err := c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	probe("api", 5432, false)
}
