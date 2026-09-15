package cluster

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveScheduledJobs(t *testing.T) {
	if os.Getenv("HAKOPOD_SCHEDULED_TEST") != "1" {
		t.Skip("opt-in scheduled-job acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named k3d-hakopod-dev context")
	}
	c, err := New(path, Options{RolloutTimeout: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	sample, _ := spec.Showcase()
	content := "scheduled-fixture"
	app, err := spec.Normalize(spec.Application{Name: fmt.Sprintf("schedule-%d", time.Now().Unix()), Services: map[string]spec.Service{"report": {
		Image: sample.Services["api"].Image, Command: []string{"python", "-c"}, Args: []string{"import time; assert open('/app/config').read()=='scheduled-fixture'; print('scheduled run ready',flush=True); time.sleep(85)"},
		Files: map[string]spec.File{"config": {MountPath: "/app/config", Content: &content}}, Job: &spec.Job{TimeoutSeconds: 120, Schedule: &spec.JobSchedule{Cron: "* * * * *"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: app.Name, OperationID: "schedule-1", Project: "acceptance", Environment: "test", Revision: 1, Spec: app}
	defer func() {
		clean, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		ns, e := c.kube.CoreV1().Namespaces().Get(clean, Namespace(target.ApplicationID), metav1.GetOptions{})
		if e == nil && owned(ns, target) == nil {
			if e = c.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(ns)); e != nil {
				t.Error(e)
			}
		}
	}()
	emit := func(e Event) { t.Log(e.Type, e.Service, e.Message) }
	o, err := c.Deploy(ctx, target, emit)
	if err != nil || o.Services[0].Status != "scheduled" {
		t.Fatal(o, err)
	}
	api := c.kube.BatchV1().CronJobs(Namespace(target.ApplicationID))
	j, err := api.Get(ctx, scheduledJobName("report"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if j.Spec.ConcurrencyPolicy != batchv1.ForbidConcurrent {
		t.Fatal("overlap prevention missing")
	}
	// Observe an actual CronJob-created run across the next scheduling boundary.
	started := time.Time{}
	var firstName string
	for started.IsZero() || time.Since(started) < 70*time.Second {
		status, e := c.observeScheduledJob(ctx, target, "report", target.Spec.Services["report"])
		if e != nil {
			t.Fatal(e)
		}
		active := 0
		for _, run := range status.JobRuns {
			if run.Status == "running" {
				active++
				if started.IsZero() {
					started = time.Now()
					firstName = run.Name
					t.Log("real CronJob run", firstName)
				}
			}
		}
		if active > 1 {
			t.Fatal("overlapping scheduled jobs", status)
		}
		if err = sleepContext(ctx, 2*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	// Pausing leaves the existing run intact, and keeps its file snapshot alive.
	target.Revision++
	s := target.Spec.Services["report"]
	s.Suspended = true
	target.Spec.Services["report"] = s
	if _, err = c.Deploy(ctx, target, emit); err != nil {
		t.Fatal(err)
	}
	run, err := c.kube.BatchV1().Jobs(j.Namespace).Get(ctx, firstName, metav1.GetOptions{})
	if err != nil {
		t.Fatal("pause removed active job", err)
	}
	for jobState(run) == "running" {
		if err = sleepContext(ctx, 2*time.Second); err != nil {
			t.Fatal(err)
		}
		run, err = c.kube.BatchV1().Jobs(j.Namespace).Get(ctx, firstName, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
	}
	if jobState(run) != "completed" {
		t.Fatal("configuration or job execution failed", run.Status)
	}
	// Resume with a failing command; the real run must surface a failure.
	target.Revision++
	s.Suspended = false
	s.Args = []string{"raise SystemExit(7)"}
	s.Job.TimeoutSeconds = 30
	target.Spec.Services["report"] = s
	if _, err = c.Deploy(ctx, target, emit); err != nil {
		t.Fatal(err)
	}
	for {
		status, e := c.observeScheduledJob(ctx, target, "report", s)
		if e != nil {
			t.Fatal(e)
		}
		if status.Status == "failed" {
			t.Log("failure surfaced", status.Message)
			break
		}
		if err = sleepContext(ctx, 2*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	target.Revision++
	target.Spec.Services = map[string]spec.Service{}
	if o, err = c.Deploy(ctx, target, emit); err != nil || o.Status != "empty" {
		t.Fatal(o, err)
	}
	jobs, err := api.List(ctx, metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 0 {
		t.Fatal("schedule survived removal", err)
	}
}
