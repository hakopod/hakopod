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
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
)

// This test covers environment loading, not database authentication. The
// separate runtime connection test covers TLS and authenticated queries.
func TestLiveDatabaseBindingRotationAndSnapshots(t *testing.T) {
	if os.Getenv("HAKOPOD_BINDING_ROLLOUT_TEST") != "1" {
		t.Skip("requires the named development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing binding acceptance outside k3d-hakopod-dev")
	}
	c, err := New(path, Options{RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	const image = "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a"
	binding := map[string]spec.Binding{"DATABASE_URL": {Service: "database", Protocol: "postgres", Username: "fixture", Database: "fixture", Password: &spec.SecretRef{Ref: "database-password"}}}
	worker := spec.Service{Image: image, Command: []string{"python", "-B", "-c"}, Args: []string{"import time; time.sleep(1200)"}}
	bound := worker
	bound.Bindings = binding
	job := bound
	job.Args = []string{"import os; from urllib.parse import urlparse; assert urlparse(os.environ['DATABASE_URL']).password == 'fixture-original'"}
	job.Job = &spec.Job{TimeoutSeconds: 60}
	schedule := job
	schedule.Job = &spec.Job{TimeoutSeconds: 60, Schedule: &spec.JobSchedule{Cron: "0 0 1 1 *"}}
	app, err := spec.Normalize(spec.Application{Name: "binding-development-fixture", Services: map[string]spec.Service{
		"api": bound, "unbound": worker, "migrate": job, "report": schedule,
		"database": {Image: image, Port: 5432, Command: []string{"python", "-B", "-c"}, Args: []string{"import socket,time; s=socket.socket(); s.bind(('0.0.0.0',5432)); s.listen(); time.sleep(1200)"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: fmt.Sprintf("binding-fixture-%d", time.Now().UnixNano()), Project: "binding-fixture", Environment: "development", OperationID: "binding-initial", Revision: 1, Spec: app}
	ns := Namespace(target.ApplicationID)
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 45*time.Second)
		defer done()
		namespace, e := c.kube.CoreV1().Namespaces().Get(clean, ns, metav1.GetOptions{})
		if e == nil && owned(namespace, target) == nil {
			if e = c.kube.CoreV1().Namespaces().Delete(clean, ns, deleteOptions(namespace)); e != nil {
				t.Error("fixture namespace cleanup failed", e)
			}
		}
		if e = c.DeleteWorkloadSecret(clean, target.Project, target.Environment, app.Name, "database-password"); e != nil {
			t.Error("fixture credential cleanup failed", e)
		}
	})
	if err = c.PutWorkloadSecret(ctx, target.Project, target.Environment, app.Name, "database-password", "fixture-original"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	getDeployment := func(name string) *appsv1.Deployment {
		t.Helper()
		dep, e := c.kube.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if e != nil {
			t.Fatal(e)
		}
		return dep
	}
	podUID := func(name string) types.UID {
		t.Helper()
		pods, e := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: serviceKey + "=" + name, Limit: 10})
		if e != nil {
			t.Fatal(e)
		}
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp == nil && podReady(pod) {
				return pod.UID
			}
		}
		t.Fatal("no ready fixture pod", name)
		return ""
	}
	verifyLoaded := func(password string) {
		t.Helper()
		// The only output is a success exit status. No URL or password is
		// printed on failure; these values belong solely to this fixture.
		code := "import os; from urllib.parse import urlparse; assert urlparse(os.environ['DATABASE_URL']).password == '" + password + "'"
		if e := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", ns, "exec", "deployment/api", "--", "python", "-B", "-c", code).Run(); e != nil {
			t.Fatal("the running fixture did not load the expected binding")
		}
	}
	verifyLoaded("fixture-original")
	oldAPI, oldUnbound := getDeployment("api"), getDeployment("unbound")
	oldAPIUID, oldUnboundUID := podUID("api"), podUID("unbound")
	oldSnapshot := oldAPI.Spec.Template.Annotations[environmentSnapshotLabel]
	if oldSnapshot == "" {
		t.Fatal("deployment did not pin its environment")
	}
	jobs, err := c.kube.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{LabelSelector: serviceKey + "=migrate", Limit: 10})
	if err != nil || len(jobs.Items) != 1 {
		t.Fatal("completed fixture job unavailable")
	}
	oldJob := jobs.Items[0].DeepCopy()
	cron, err := c.kube.BatchV1().CronJobs(ns).Get(ctx, scheduledJobName("report"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	oldCronSnapshot := cron.Spec.JobTemplate.Spec.Template.Annotations[environmentSnapshotLabel]
	if err = c.PutWorkloadSecret(ctx, target.Project, target.Environment, app.Name, "database-password", "fixture-rotated"); err != nil {
		t.Fatal(err)
	}
	events := []Event{}
	emit := func(e Event) { events = append(events, e) }
	if err = c.RefreshDatabaseBindings(ctx, target, emit, "api"); err != nil {
		t.Fatal(err)
	}
	updated := getDeployment("api")
	if err = c.waitReady(ctx, target, "api", updated.Generation); err != nil {
		t.Fatal(err)
	}
	if err = c.RefreshDatabaseBindings(ctx, target, emit, "api"); err != nil {
		t.Fatal(err)
	}
	verifyLoaded("fixture-rotated")
	if podUID("api") == oldAPIUID || podUID("unbound") != oldUnboundUID || !reflect.DeepEqual(oldUnbound.Spec.Template, getDeployment("unbound").Spec.Template) {
		t.Fatal("binding rotation did not replace only the affected service")
	}
	updated = getDeployment("api")
	if updated.Annotations[bindingVerifiedSnapshot] != updated.Spec.Template.Annotations[environmentSnapshotLabel] {
		t.Fatal("ready binding was not recorded after the subsequent maintenance pass")
	}
	if len(events) != 2 || events[0].Type != "binding_rollout" || events[1].Type != "binding_ready" {
		t.Fatal("binding event stages are incomplete")
	}
	if _, err = c.kube.CoreV1().Secrets(ns).Get(ctx, oldSnapshot, metav1.GetOptions{}); err != nil {
		t.Fatal("retained ReplicaSet lost its environment snapshot")
	}
	if err = c.RefreshDatabaseBindings(ctx, target, emit, "report"); err != nil {
		t.Fatal(err)
	}
	cron, err = c.kube.BatchV1().CronJobs(ns).Get(ctx, scheduledJobName("report"), metav1.GetOptions{})
	if err != nil || cron.Spec.JobTemplate.Spec.Template.Annotations[environmentSnapshotLabel] == oldCronSnapshot {
		t.Fatal("future scheduled jobs did not receive the rotated binding")
	}
	if err = c.RefreshDatabaseBindings(ctx, target, emit, "migrate"); err != nil {
		t.Fatal(err)
	}
	keptJob, err := c.kube.BatchV1().Jobs(ns).Get(ctx, oldJob.Name, metav1.GetOptions{})
	if err != nil || keptJob.UID != oldJob.UID || !reflect.DeepEqual(keptJob.Spec, oldJob.Spec) {
		t.Fatal("binding rotation changed a completed migration job")
	}
	stableUID, stableGeneration := podUID("api"), getDeployment("api").Generation
	if err = c.RefreshDatabaseBindings(ctx, target, emit, "api"); err != nil || getDeployment("api").Generation != stableGeneration || podUID("api") != stableUID {
		t.Fatal("identical binding values caused another rollout")
	}
	t.Log("Credential rotation replaced the affected pod, verified its loaded environment, retained historical snapshots and completed jobs, and updated future scheduled jobs.")
}
