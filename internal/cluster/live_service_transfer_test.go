package cluster

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveServiceTransferAndCatalogAddition(t *testing.T) {
	if os.Getenv("HAKOPOD_SERVICE_TRANSFER_TEST") != "1" {
		t.Skip("requires explicitly named development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires k3d-hakopod-dev")
	}
	c, err := New(path, Options{RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	const image = "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a"
	create := func(name string) Target {
		a, e := spec.Normalize(spec.Application{Name: name, InjectEnv: true, Env: map[string]string{"APP_MODE": "fixture"}, Services: map[string]spec.Service{"api": {Image: image, Port: 8080, Command: []string{"python", "-B", "-u", "-c"}, Args: []string{"import http.server; http.server.ThreadingHTTPServer(('0.0.0.0',8080),http.server.SimpleHTTPRequestHandler).serve_forever()"}}}})
		if e != nil {
			t.Fatal(e)
		}
		target := Target{ApplicationID: fmt.Sprintf("service-transfer-%s-%d", name, time.Now().UnixNano()), Project: "service-transfer-test", Environment: "test", OperationID: "initial-" + name, Revision: 1, Spec: a}
		t.Cleanup(func() {
			clean, done := context.WithTimeout(context.Background(), 20*time.Second)
			defer done()
			ns, e := c.kube.CoreV1().Namespaces().Get(clean, Namespace(target.ApplicationID), metav1.GetOptions{})
			if e == nil && owned(ns, target) == nil {
				if e = c.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(ns)); e != nil {
					t.Error(e)
				}
			}
		})
		if _, e = c.Deploy(ctx, target, nil); e != nil {
			t.Fatal(e)
		}
		return target
	}
	source, destination := create("source"), create("destination")
	original, err := c.kube.AppsV1().Deployments(Namespace(destination.ApplicationID)).Get(ctx, "api", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	left, right, err := spec.MoveService(source.Spec, destination.Spec, "api", "moved")
	if err != nil {
		t.Fatal(err)
	}
	destinationBefore := destination.Spec
	destination.Previous = &destinationBefore
	destination.Spec = right
	destination.Revision = 2
	destination.OperationID = "move-destination"
	if _, err = c.Deploy(ctx, destination, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = c.kube.AppsV1().Deployments(Namespace(source.ApplicationID)).Get(ctx, "api", metav1.GetOptions{}); err != nil {
		t.Fatal("source disappeared before finish", err)
	}
	moved, err := c.kube.AppsV1().Deployments(Namespace(destination.ApplicationID)).Get(ctx, "moved", metav1.GetOptions{})
	if err != nil || moved.Status.ReadyReplicas != 1 {
		t.Fatal("destination not ready", err)
	}
	kept, err := c.kube.AppsV1().Deployments(Namespace(destination.ApplicationID)).Get(ctx, "api", metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(kept.Spec.Template, original.Spec.Template) {
		t.Fatal("untouched destination service changed", err)
	}
	cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", Namespace(destination.ApplicationID), "exec", "deployment/moved", "--", "python", "-B", "-c", "import os; assert os.environ['APP_MODE']=='fixture'; import urllib.request; assert urllib.request.urlopen('http://api:8080',timeout=5).status==200")
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("destination inherited environment/private DNS check: %v %s", e, out)
	}
	sourceBefore := source.Spec
	source.Previous = &sourceBefore
	source.Spec = left
	source.Revision = 2
	source.OperationID = "move-finish"
	if _, err = c.Deploy(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = c.kube.AppsV1().Deployments(Namespace(source.ApplicationID)).Get(ctx, "api", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("original deployment retained after finish", err)
	}
	template, err := spec.PlanTemplate("nginx", spec.TemplateOptions{Name: "template"})
	if err != nil {
		t.Fatal(err)
	}
	// Run the reviewed template shape with an already downloaded immutable fixture
	// image. This isolates additive orchestration from third-party tag availability.
	for name, svc := range template.Services {
		svc.Image = image
		svc.Command = []string{"python", "-B", "-u", "-c"}
		svc.Args = []string{fmt.Sprintf("import http.server; http.server.ThreadingHTTPServer(('0.0.0.0',%d),http.server.SimpleHTTPRequestHandler).serve_forever()", svc.Port)}
		template.Services[name] = svc
	}
	next, err := spec.AddServices(destination.Spec, template)
	if err != nil {
		t.Fatal(err)
	}
	previous := destination.Spec
	destination.Previous = &previous
	destination.Spec = next
	destination.Revision = 3
	destination.OperationID = "catalog-add"
	if _, err = c.Deploy(ctx, destination, nil); err != nil {
		t.Fatal(err)
	}
	still, err := c.kube.AppsV1().Deployments(Namespace(destination.ApplicationID)).Get(ctx, "moved", metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(still.Spec.Template, moved.Spec.Template) {
		t.Fatal("catalog addition rolled existing service", err)
	}
	t.Log("verified staged move, original removal, unchanged peer pod templates, inherited environment, private DNS and additive catalog deployment")
}
