package cluster

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestScheduledJobLifecycle(t *testing.T) {
	ctx := context.Background()
	target := testTarget(t)
	s := target.Spec.Services["worker"]
	s.Job = &spec.Job{TimeoutSeconds: 30, Retries: 1, Schedule: &spec.JobSchedule{Cron: "0 * * * *", Timezone: "Asia/Kolkata", HistoryLimit: 2}}
	target.Spec.Services = map[string]spec.Service{"worker": s}
	kube := fake.NewClientset()
	c := &Client{kube: kube, options: Options{RolloutTimeout: time.Second}}
	if err := c.applyScheduledJob(ctx, target, "worker", s); err != nil {
		t.Fatal(err)
	}
	api := kube.BatchV1().CronJobs(Namespace(target.ApplicationID))
	j, err := api.Get(ctx, scheduledJobName("worker"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !*j.Spec.Suspend || *j.Spec.TimeZone != "Asia/Kolkata" || j.Spec.ConcurrencyPolicy != batchv1.ForbidConcurrent || *j.Spec.SuccessfulJobsHistoryLimit != 2 || *j.Spec.FailedJobsHistoryLimit != 2 || *j.Spec.JobTemplate.Spec.ActiveDeadlineSeconds != 30 || *j.Spec.JobTemplate.Spec.BackoffLimit != 1 || *j.Spec.JobTemplate.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatalf("unsafe schedule: %+v", j.Spec)
	}
	j.UID = types.UID("schedule-uid")
	if _, err = api.Update(ctx, j, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	pending, err := c.observeScheduledJob(ctx, target, "worker", s)
	if err != nil || pending.Status != "pending" {
		t.Fatal(pending, err)
	}
	if err = c.activateScheduledJobs(ctx, target); err != nil {
		t.Fatal(err)
	}
	observed, err := c.Observe(ctx, target)
	if err != nil || observed.Status != "healthy" || observed.Services[0].Status != "scheduled" {
		t.Fatal(observed, err)
	}
	// A failed run is not a healthy schedule; a later success clears that failure.
	for i, condition := range []batchv1.JobConditionType{batchv1.JobFailed, batchv1.JobComplete} {
		stamp := metav1.NewTime(time.Now().Add(time.Duration(i) * time.Minute))
		run := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("run-%d", i), Namespace: j.Namespace, Labels: labelsFor(target, "worker"), Annotations: map[string]string{jobRevision: fmt.Sprint(target.Revision)}, CreationTimestamp: stamp, OwnerReferences: []metav1.OwnerReference{{Kind: "CronJob", UID: j.UID, Controller: ptr(true)}}}, Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: condition, Status: corev1.ConditionTrue, LastTransitionTime: stamp}}}}
		if _, err = kube.BatchV1().Jobs(j.Namespace).Create(ctx, run, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		status, err := c.observeScheduledJob(ctx, target, "worker", s)
		if err != nil {
			t.Fatal(err)
		}
		expected := "failed"
		if i == 1 {
			expected = "scheduled"
		}
		if status.Status != expected || len(status.JobRuns) != i+1 || status.JobRuns[0].Name != run.Name {
			t.Fatal(status)
		}
	}
	s.Suspended = true
	target.Spec.Services["worker"] = s
	target.Revision++
	if err = c.applyScheduledJob(ctx, target, "worker", s); err != nil {
		t.Fatal(err)
	}
	if err = c.activateScheduledJobs(ctx, target); err != nil {
		t.Fatal(err)
	}
	status, err := c.observeScheduledJob(ctx, target, "worker", s)
	if err != nil || status.Status != "stopped" || len(status.JobRuns) != 2 {
		t.Fatal(status, err)
	}
	target.Spec.Services = map[string]spec.Service{}
	if err = c.cleanupScheduledJobs(ctx, target); err != nil {
		t.Fatal(err)
	}
	list, _ := api.List(ctx, metav1.ListOptions{})
	if len(list.Items) != 0 {
		t.Fatal("schedule not removed")
	}
}

func TestScheduledJobOwnershipAndNames(t *testing.T) {
	name := strings.Repeat("a", 63)
	if len(scheduledJobName(name)) > 52 || scheduledJobName(name) == scheduledJobName(name[:62]+"b") {
		t.Fatal("invalid or colliding CronJob name")
	}
	target := testTarget(t)
	s := target.Spec.Services["worker"]
	s.Job = &spec.Job{Schedule: &spec.JobSchedule{Cron: "* * * * *", Timezone: "UTC", HistoryLimit: 1}}
	kube := fake.NewClientset(&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: scheduledJobName("worker"), Namespace: Namespace(target.ApplicationID)}})
	c := &Client{kube: kube}
	if err := c.applyScheduledJob(context.Background(), target, "worker", s); err == nil {
		t.Fatal("unowned schedule overwritten")
	}
}
