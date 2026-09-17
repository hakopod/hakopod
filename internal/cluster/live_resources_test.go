package cluster

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveExplicitResources(t *testing.T) {
	if os.Getenv("HAKOPOD_RESOURCE_TEST") != "1" {
		t.Skip("requires named development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev context")
	}
	c, err := New(path, Options{RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	const image = "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a"
	draft, err := spec.ImportCompose([]byte(`name: resource-fixture
services:
  api:
    image: `+image+`
    entrypoint: [python, -B, -u, -c]
    command: ["import http.server; http.server.ThreadingHTTPServer(('0.0.0.0',8080),http.server.SimpleHTTPRequestHandler).serve_forever()"]
    expose: [8080]
    deploy:
      replicas: 2
      resources:
        reservations: {cpus: '0.125', memory: 96m}
        limits: {cpus: '0.3', memory: 192m}
`), "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	app := draft.Spec
	defaultService := app.Services["api"]
	defaultService.Resources = nil
	defaultService.Replicas = 1
	defaultService.Port = 0
	defaultService.Args = []string{"import time; time.sleep(300)"}
	app.Services["profile"] = defaultService
	job := app.Services["api"]
	job.Port = 0
	job.Replicas = 1
	job.Args = []string{"print('resource fixture complete')"}
	job.Job = &spec.Job{TimeoutSeconds: 60}
	app.Services["migrate"] = job
	job.Job = &spec.Job{TimeoutSeconds: 60, Schedule: &spec.JobSchedule{Cron: "* * * * *"}}
	app.Services["scheduled"] = job
	app, err = spec.Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: fmt.Sprintf("resources-%d", time.Now().UnixNano()), Project: "resource-test", Environment: "test", OperationID: "resources-1", Revision: 1, Spec: app}
	ns := Namespace(target.ApplicationID)
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 25*time.Second)
		defer done()
		n, e := c.kube.CoreV1().Namespaces().Get(clean, ns, metav1.GetOptions{})
		if e == nil && owned(n, target) == nil {
			if e = c.kube.CoreV1().Namespaces().Delete(clean, ns, deleteOptions(n)); e != nil {
				t.Error(e)
			}
		}
	})
	if _, err = c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Service, e.Message) }); err != nil {
		t.Fatal(err)
	}
	check := func(pod corev1.PodSpec) {
		t.Helper()
		r := pod.Containers[0].Resources
		if r.Requests.Cpu().MilliValue() != 125 || r.Limits.Cpu().MilliValue() != 300 || r.Requests.Memory().Value() != 96<<20 || r.Limits.Memory().Value() != 192<<20 {
			t.Fatal("resource fields not applied", r)
		}
	}
	d, err := c.kube.AppsV1().Deployments(ns).Get(ctx, "api", metav1.GetOptions{})
	if err != nil || d.Status.ReadyReplicas != 2 {
		t.Fatal("deployment not ready", err)
	}
	check(d.Spec.Template.Spec)
	j, err := c.kube.BatchV1().Jobs(ns).Get(ctx, jobName("migrate"), metav1.GetOptions{})
	if err != nil || j.Status.Succeeded != 1 {
		t.Fatal("job failed", err)
	}
	check(j.Spec.Template.Spec)
	cron, err := c.kube.BatchV1().CronJobs(ns).Get(ctx, scheduledJobName("scheduled"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	check(cron.Spec.JobTemplate.Spec.Template.Spec)
	for {
		jobs, err := c.kube.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		done := false
		for _, j := range jobs.Items {
			for _, owner := range j.OwnerReferences {
				if owner.UID == cron.UID && j.Status.Succeeded == 1 {
					check(j.Spec.Template.Spec)
					done = true
				}
			}
		}
		if done {
			break
		}
		if err := sleepContext(ctx, time.Second); err != nil {
			t.Fatal("scheduled resource job did not complete", err)
		}
	}
	pods, err := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil || len(pods.Items) < 5 {
		t.Fatal("expected actual deployment and job pods", err)
	}
	for _, pod := range pods.Items {
		if pod.Labels[serviceKey] == "profile" {
			r := pod.Spec.Containers[0].Resources
			if r.Requests.Cpu().MilliValue() != 120 || r.Limits.Cpu().MilliValue() != 600 || r.Requests.Memory().Value() != 154<<20 || r.Limits.Memory().Value() != 308<<20 {
				t.Fatal("inherited small profile was not increased", r)
			}
		} else {
			check(pod.Spec)
		}
	}
	t.Log("Compose -> TOML -> two ready replicas, completed Job and completed CronJob run: explicit budgets unchanged and increased small defaults verified on actual pods")
}
