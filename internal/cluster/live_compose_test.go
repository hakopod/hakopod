package cluster

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveComposeImport(t *testing.T) {
	if os.Getenv("HAKOPOD_COMPOSE_TEST") != "1" {
		t.Skip("requires named development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing Compose acceptance outside named development cluster")
	}
	c, err := New(path, Options{RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	const image = "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a"
	draft, err := spec.ImportCompose([]byte(`name: compose-fixture
services:
  api:
    image: `+image+`
    entrypoint: [python, -B, -u, -c]
    command: ["import http.server; http.server.ThreadingHTTPServer(('0.0.0.0',8080),http.server.SimpleHTTPRequestHandler).serve_forever()"]
    expose: [8080]
    environment: {COMPOSE_MODE: ready}
    deploy: {replicas: 2}
  data:
    image: `+image+`
    entrypoint: [python, -B, -c]
    command: ["import time; time.sleep(600)"]
    volumes: [data:/data]
volumes:
  data: {}
`), "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse([]byte(draft.TOML))
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: fmt.Sprintf("compose-fixture-%d", time.Now().UnixNano()), Project: "compose-test", Environment: "test", OperationID: "compose-initial", Revision: 1, Spec: app}
	ns := Namespace(target.ApplicationID)
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 25*time.Second)
		defer done()
		namespace, e := c.kube.CoreV1().Namespaces().Get(clean, ns, metav1.GetOptions{})
		if e == nil && owned(namespace, target) == nil {
			if e = c.kube.CoreV1().Namespaces().Delete(clean, ns, deleteOptions(namespace)); e != nil {
				t.Error(e)
			}
		}
	})
	if _, err = c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Service, e.Message) }); err != nil {
		t.Fatal(err)
	}
	api, err := c.kube.AppsV1().Deployments(ns).Get(ctx, "api", metav1.GetOptions{})
	if err != nil || api.Status.ReadyReplicas != 2 {
		t.Fatal("converted replicas did not become ready", err)
	}
	run := func(service, code string) string {
		t.Helper()
		out, e := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", ns, "exec", "deployment/"+service, "--", "python", "-B", "-c", code).CombinedOutput()
		if e != nil {
			t.Fatalf("fixture exec: %s %v", out, e)
		}
		return strings.TrimSpace(string(out))
	}
	if run("api", "import os; print(os.environ['COMPOSE_MODE'])") != "ready" {
		t.Fatal("runtime variable missing")
	}
	run("data", "open('/data/proof','w').write('retained'); import urllib.request; assert urllib.request.urlopen('http://api:8080',timeout=5).status==200")
	claim, err := c.kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, "hakopod-volume-data", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	addition, err := spec.ImportCompose([]byte("services:\n  worker:\n    image: "+image+"\n    entrypoint: [python, -B, -c]\n    command: ['import time; time.sleep(600)']\n"), app.Name, nil, &app)
	if err != nil {
		t.Fatal(err)
	}
	target.Previous = &app
	target.Spec = addition.Spec
	target.Revision = 2
	target.OperationID = "compose-add-service"
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	after, err := c.kube.AppsV1().Deployments(ns).Get(ctx, "api", metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(api.Spec.Template, after.Spec.Template) {
		t.Fatal("adding a service changed an existing pod template", err)
	}
	kept, err := c.kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, claim.Name, metav1.GetOptions{})
	if err != nil || kept.UID != claim.UID || run("data", "print(open('/data/proof').read())") != "retained" {
		t.Fatal("existing data not retained", err)
	}
	t.Log("Compose argv, runtime environment, two replicas, private DNS, persistent data and additive service import verified")
}
