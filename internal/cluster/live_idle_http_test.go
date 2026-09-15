package cluster

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"testing"
	"time"
)

func TestLiveIdleHTTP(t *testing.T) {
	if os.Getenv("HAKOPOD_IDLE_HTTP_TEST") != "1" {
		t.Skip("requires named development cluster and shared sandbox fixture")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named development context")
	}
	c, err := New(path, Options{DeploymentMode: DeploymentManagedCloud, OperatorNodeLimit: 2, AppDomain: "127.0.0.1.sslip.io", IngressClass: "haproxy", PublicPort: 18080, RolloutTimeout: 90 * time.Second, WorkloadPolicy: func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
		return WorkloadPolicy{NodeName: "k3d-shared-free-test-0", Pool: "free", RuntimeClass: "runsc", IdleHTTP: true, MemoryRequest: "256Mi"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	app, err := spec.Parse([]byte("name='idle-http-fixture'\n[services.web]\nimage='python:3.13-alpine'\npublic=true\nport=8080\ncommand=['python','-m','http.server','8080']"))
	if err != nil {
		t.Fatal(err)
	}
	app, err = c.Resolve(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "idle-http-live-fixture", Project: "idle-fixture", Environment: "production", Revision: 1, Spec: app}
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		ns, err := c.kube.CoreV1().Namespaces().Get(clean, Namespace(target.ApplicationID), metav1.GetOptions{})
		if err == nil && owned(ns, target) == nil {
			c.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(ns))
		}
	})
	if _, err = c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Message) }); err != nil {
		t.Fatal(err)
	}
	if err = c.SetHTTPIdle(ctx, target, "web", true); err != nil {
		t.Fatal(err)
	}
	for {
		o, err := c.Observe(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		if o.Services[0].Status == "sleeping" {
			t.Log("real observation: sleeping at zero replicas")
			break
		}
		if err = sleepContext(ctx, 250*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	d, err := c.kube.AppsV1().Deployments(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
	if err != nil || !idleDeployment(d) {
		t.Fatal("sleep not persisted", err)
	}
	pods, err := c.kube.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp == nil {
			t.Fatal("pod still active")
		}
	}
	if err = c.SetHTTPIdle(ctx, target, "web", false); err != nil {
		t.Fatal(err)
	}
	for {
		ready, err := c.HTTPAwake(ctx, target, "web")
		if err != nil {
			t.Fatal(err)
		}
		if ready {
			break
		}
		if err = sleepContext(ctx, 250*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	if err = c.SetHTTPIdle(ctx, target, "web", false); err != nil {
		t.Fatal(err)
	}
	t.Log("wake restored ready pods and published endpoints")
	if target.Spec.Services["web"].Replicas != 1 {
		t.Fatal("saved replicas changed")
	}
	for {
		err = c.SetHTTPIdle(ctx, target, "web", true)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrIdleIneligible) {
			t.Fatal(err)
		}
		if err = sleepContext(ctx, 250*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	target.Revision++
	svc := target.Spec.Services["web"]
	svc.Suspended = true
	target.Spec.Services["web"] = svc
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	if err = c.SetHTTPIdle(ctx, target, "web", false); !errors.Is(err, ErrIdleIneligible) {
		t.Fatal("manual stop woke", err)
	}
	d, err = c.kube.AppsV1().Deployments(d.Namespace).Get(ctx, "web", metav1.GetOptions{})
	if err != nil || *d.Spec.Replicas != 0 || d.Annotations[idleHTTPAnnotation] != "" {
		t.Fatal("manual stop lost", err)
	}
	t.Log("new manual stop cleared marker and remained at zero")
}
