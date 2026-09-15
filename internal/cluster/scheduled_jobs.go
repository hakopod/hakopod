package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"

	"github.com/hakopod/hakopod/internal/spec"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

func scheduledJobName(name string) string {
	if len(name) <= 47 {
		return name + "-cron"
	}
	hash := sha256.Sum256([]byte(name))
	return name[:38] + "-" + fmt.Sprintf("%x", hash[:4]) + "-cron"
}
func (c *Client) applyScheduledJob(ctx context.Context, t Target, name string, s spec.Service) error {
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	if err := c.prepareStorage(ctx, t, name, s); err != nil {
		return err
	}
	if err := c.prepareWorkloadSecrets(ctx, t, name, s); err != nil {
		return err
	}
	if err := c.prepareFiles(ctx, t, name, s); err != nil {
		return err
	}
	d := deployment(t, name, s, c.options.RolloutTimeout)
	if err := c.prepareAWSIdentity(ctx, t, name, s, d); err != nil {
		return err
	}
	if err := c.prepareRegistryCredential(ctx, t, name, s, d); err != nil {
		return err
	}
	d.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyNever
	schedule := s.Job.Schedule
	labels := labelsFor(t, name)
	annotation := map[string]string{jobRevision: strconv.FormatInt(t.Revision, 10)}
	wanted := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: scheduledJobName(name), Namespace: Namespace(t.ApplicationID), Labels: labels, Annotations: annotation}, Spec: batchv1.CronJobSpec{
		Schedule: schedule.Cron, TimeZone: ptr(schedule.Timezone), ConcurrencyPolicy: batchv1.ForbidConcurrent, Suspend: ptr(true),
		StartingDeadlineSeconds: ptr(int64(60)), SuccessfulJobsHistoryLimit: ptr(schedule.HistoryLimit), FailedJobsHistoryLimit: ptr(schedule.HistoryLimit),
		JobTemplate: batchv1.JobTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotation}, Spec: batchv1.JobSpec{
			Template: d.Spec.Template, BackoffLimit: ptr(s.Job.Retries), ActiveDeadlineSeconds: ptr(s.Job.TimeoutSeconds), Parallelism: ptr(int32(1)), Completions: ptr(int32(1)),
		}},
	}}
	api := c.kube.BatchV1().CronJobs(Namespace(t.ApplicationID))
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := beforeStep(ctx, t); err != nil {
			return err
		}
		current, err := api.Get(ctx, wanted.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = api.Create(ctx, wanted, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		if err = owned(current, t); err != nil {
			return err
		}
		if current.Labels[serviceKey] != name {
			return fmt.Errorf("scheduled job belongs to another service")
		}
		next := current.DeepCopy()
		next.Spec = wanted.Spec
		if next.Annotations == nil {
			next.Annotations = map[string]string{}
		}
		next.Annotations[jobRevision] = annotation[jobRevision]
		_, err = api.Update(ctx, next, metav1.UpdateOptions{})
		return err
	})
}
func (c *Client) observeScheduledJob(ctx context.Context, t Target, name string, s spec.Service) (ServiceStatus, error) {
	status := ServiceStatus{Name: name, Status: "missing", Image: s.Image}
	j, err := c.kube.BatchV1().CronJobs(Namespace(t.ApplicationID)).Get(ctx, scheduledJobName(name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	if err = owned(j, t); err != nil {
		return status, err
	}
	if j.Annotations[jobRevision] != strconv.FormatInt(t.Revision, 10) {
		status.Status = "pending"
		return status, nil
	}
	if j.Spec.Suspend == nil || *j.Spec.Suspend != s.Suspended {
		status.Status = "pending"
		status.Message = "Schedule awaiting release activation"
		return status, nil
	}
	jobs, err := c.kube.BatchV1().Jobs(Namespace(t.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + serviceKey + "=" + name, Limit: 21})
	if err != nil {
		return status, err
	}
	if jobs.Continue != "" || len(jobs.Items) > 20 {
		return status, fmt.Errorf("too many scheduled runs for bounded observation")
	}
	sort.Slice(jobs.Items, func(a, b int) bool {
		return jobs.Items[a].CreationTimestamp.After(jobs.Items[b].CreationTimestamp.Time)
	})
	lastResult := ""
	for _, run := range jobs.Items {
		controlled := false
		for _, owner := range run.OwnerReferences {
			controlled = controlled || owner.Kind == "CronJob" && owner.UID == j.UID && owner.Controller != nil && *owner.Controller
		}
		if !controlled {
			continue
		}
		if err := owned(&run, t); err != nil {
			return status, err
		}
		revision, _ := strconv.ParseInt(run.Annotations[jobRevision], 10, 64)
		item := JobRun{Name: run.Name, Status: jobState(&run), Revision: revision, CreatedAt: run.CreationTimestamp.Time}
		for _, condition := range run.Status.Conditions {
			if condition.Status == corev1.ConditionTrue && (condition.Type == batchv1.JobFailed || condition.Type == batchv1.JobComplete) {
				stamp := condition.LastTransitionTime.Time
				item.FinishedAt = &stamp
			}
		}
		if len(status.JobRuns) < 6 {
			status.JobRuns = append(status.JobRuns, item)
		}
		if revision == t.Revision && lastResult == "" && item.Status != "running" {
			lastResult = item.Status
		}
	}
	status.Status = "scheduled"
	status.Message = "Schedule configured: " + j.Spec.Schedule + " (" + s.Job.Schedule.Timezone + "); overlapping runs are skipped"
	if j.Spec.Suspend != nil && *j.Spec.Suspend {
		status.Status = "stopped"
		status.Message = "Future runs paused; any active run can finish"
	}
	if lastResult == "failed" && !s.Suspended {
		status.Status = "failed"
		status.Message = "Scheduled job failed or exceeded its deadline; inspect service logs. Future runs remain enabled"
	}
	if j.Status.LastScheduleTime != nil {
		status.Message += "; last scheduled " + j.Status.LastScheduleTime.UTC().Format("2006-01-02T15:04:05Z")
	}
	return status, nil
}
func (c *Client) cleanupScheduledJobs(ctx context.Context, t Target) error {
	api := c.kube.BatchV1().CronJobs(Namespace(t.ApplicationID))
	items, err := api.List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID), Limit: 21})
	if err != nil {
		return err
	}
	if items.Continue != "" || len(items.Items) > 20 {
		return fmt.Errorf("too many scheduled jobs for bounded cleanup")
	}
	for _, j := range items.Items {
		if s, ok := t.Spec.Services[j.Labels[serviceKey]]; ok && s.Job != nil && s.Job.Schedule != nil {
			continue
		}
		if err := owned(&j, t); err != nil {
			return err
		}
		if err := beforeStep(ctx, t); err != nil {
			return err
		}
		opts := deleteOptions(&j)
		opts.PropagationPolicy = ptr(metav1.DeletePropagationForeground)
		if err := api.Delete(ctx, j.Name, opts); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// Publish schedules only after all workloads and removals have reconciled. An
// in-flight Job keeps its pod template; suspension affects future runs only.
func (c *Client) activateScheduledJobs(ctx context.Context, t Target) error {
	for _, name := range spec.Names(t.Spec) {
		s := t.Spec.Services[name]
		if s.Job == nil || s.Job.Schedule == nil {
			continue
		}
		api := c.kube.BatchV1().CronJobs(Namespace(t.ApplicationID))
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			if err := beforeStep(ctx, t); err != nil {
				return err
			}
			j, err := api.Get(ctx, scheduledJobName(name), metav1.GetOptions{})
			if err != nil {
				return err
			}
			if err := owned(j, t); err != nil {
				return err
			}
			if j.Annotations[jobRevision] != strconv.FormatInt(t.Revision, 10) {
				return fmt.Errorf("schedule revision changed before activation")
			}
			j.Spec.Suspend = ptr(s.Suspended)
			_, err = api.Update(ctx, j, metav1.UpdateOptions{})
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}
