package cluster

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
)

const jobRevision = "hakopod.io/job-revision"
const jobCreation = "hakopod.io/job-creation"

func jobName(service string) string { return service + "-job" }

// A stable Job per service bounds retained history. Reconciliation of the same
// revision reuses it, including failure. A new revision replaces it explicitly.
func (c *Client) runJob(ctx context.Context, t Target, name string, s spec.Service) (resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.Job.TimeoutSeconds+30)*time.Second)
	defer cancel()
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	api := c.kube.BatchV1().Jobs(Namespace(t.ApplicationID))
	revision := strconv.FormatInt(t.Revision, 10)
	current, err := api.Get(ctx, jobName(name), metav1.GetOptions{})
	if err == nil {
		if err := owned(current, t); err != nil {
			return err
		}
		if current.Labels[serviceKey] != name {
			return fmt.Errorf("job belongs to another service")
		}
		if current.Annotations[jobRevision] != revision {
			if err := beforeStep(ctx, t); err != nil {
				return err
			}
			opts := deleteOptions(current)
			opts.PropagationPolicy = ptr(metav1.DeletePropagationForeground)
			if err := api.Delete(ctx, current.Name, opts); err != nil {
				return err
			}
			for {
				if err := beforeStep(ctx, t); err != nil {
					return err
				}
				_, err = api.Get(ctx, current.Name, metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					break
				}
				if err != nil {
					return err
				}
				if err := sleepContext(ctx, 500*time.Millisecond); err != nil {
					return err
				}
			}
		}
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if apierrors.IsNotFound(err) {
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
		creation := string(uuid.NewUUID())
		wanted := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: jobName(name), Namespace: Namespace(t.ApplicationID), Labels: labelsFor(t, name), Annotations: map[string]string{jobRevision: revision, jobCreation: creation}}, Spec: batchv1.JobSpec{Template: d.Spec.Template, BackoffLimit: ptr(s.Job.Retries), ActiveDeadlineSeconds: ptr(s.Job.TimeoutSeconds), Completions: ptr(int32(1)), Parallelism: ptr(int32(1))}}
		if err := beforeStep(ctx, t); err != nil {
			return err
		}
		current, err = api.Create(ctx, wanted, metav1.CreateOptions{})
		if err != nil {
			// The API may have committed the Job before its response was lost.
			// A per-request marker prevents cleanup from adopting another attempt.
			c.cancelJob(t, name, "", creation)
			return err
		}
	}
	uid := current.UID
	defer func() {
		if resultErr == nil || jobState(current) == "completed" || jobState(current) == "failed" {
			return
		}
		c.cancelJob(t, name, uid, "")
	}()
	for {
		if err := beforeStep(ctx, t); err != nil {
			return err
		}
		if current.UID != uid || current.DeletionTimestamp != nil {
			return fmt.Errorf("%s: job changed while waiting for completion", name)
		}
		switch jobState(current) {
		case "completed":
			return nil
		case "failed":
			return fmt.Errorf("%s: job failed or exceeded its deadline; inspect service logs; deploy a new revision to retry", name)
		}
		if err := sleepContext(ctx, 500*time.Millisecond); err != nil {
			return fmt.Errorf("%s: job completion wait stopped: %w", name, err)
		}
		current, err = api.Get(ctx, jobName(name), metav1.GetOptions{})
		if err != nil {
			return err
		}
	}
}

// Cancellation or authority loss stops only this exact active Job. Status
// updates may race the resource-version precondition, so cleanup retries them.
func (c *Client) cancelJob(t Target, name string, uid types.UID, creation string) {
	clean, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	api := c.kube.BatchV1().Jobs(Namespace(t.ApplicationID))
	for {
		latest, err := api.Get(clean, jobName(name), metav1.GetOptions{})
		if apierrors.IsNotFound(err) && creation != "" && clean.Err() == nil {
			// A cancelled HTTP request can finish committing after our first read.
			if sleepContext(clean, 100*time.Millisecond) == nil {
				continue
			}
		}
		if err != nil || owned(latest, t) != nil || latest.Labels[serviceKey] != name || latest.Annotations[jobRevision] != strconv.FormatInt(t.Revision, 10) {
			return
		}
		if uid != "" && latest.UID != uid || uid == "" && (creation == "" || latest.Annotations[jobCreation] != creation) {
			return
		}
		if latest.DeletionTimestamp != nil || jobState(latest) == "completed" || jobState(latest) == "failed" {
			return
		}
		opts := deleteOptions(latest)
		opts.PropagationPolicy = ptr(metav1.DeletePropagationForeground)
		err = api.Delete(clean, latest.Name, opts)
		if !apierrors.IsConflict(err) || sleepContext(clean, 100*time.Millisecond) != nil {
			return
		}
	}
}

func jobState(j *batchv1.Job) string {
	if j == nil {
		return "unknown"
	}
	for _, condition := range j.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		if condition.Type == batchv1.JobFailed {
			return "failed"
		}
		if condition.Type == batchv1.JobComplete {
			return "completed"
		}
	}
	return "running"
}

func (c *Client) observeJob(ctx context.Context, t Target, name string, s spec.Service) (ServiceStatus, error) {
	status := ServiceStatus{Name: name, Status: "missing", Desired: 1, Image: s.Image}
	j, err := c.kube.BatchV1().Jobs(Namespace(t.ApplicationID)).Get(ctx, jobName(name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	if err := owned(j, t); err != nil {
		return status, err
	}
	if j.Annotations[jobRevision] != strconv.FormatInt(t.Revision, 10) {
		status.Status = "pending"
		return status, nil
	}
	status.Status = jobState(j)
	if status.Status == "completed" {
		status.Ready = 1
		status.Message = "Job completed successfully"
	}
	if status.Status == "failed" {
		status.Message = "Job failed or exceeded its deadline; inspect service logs"
	}
	return status, nil
}

func (c *Client) validateWorkloadKinds(ctx context.Context, t Target) error {
	ns, nsErr := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(t.ApplicationID), metav1.GetOptions{})
	if nsErr != nil && !apierrors.IsNotFound(nsErr) {
		return nsErr
	}
	if nsErr == nil {
		for _, svc := range t.Spec.Services {
			if (ns.Labels["hakopod.io/workload-kind"] == "actions") != (svc.Actions != nil) {
				return fmt.Errorf("Managed Actions requires a dedicated application; create a new application instead of converting this one")
			}
		}
	}

	for name, s := range t.Spec.Services {
		if s.Actions != nil {
			d, e := c.kube.AppsV1().Deployments(Namespace(t.ApplicationID)).Get(ctx, name, metav1.GetOptions{})
			if e == nil && d != nil {
				return fmt.Errorf("%s: remove the existing service before creating a Managed Actions pool", name)
			}
			if e != nil && !apierrors.IsNotFound(e) {
				return e
			}
		}
		cron, err := c.kube.BatchV1().CronJobs(Namespace(t.ApplicationID)).Get(ctx, scheduledJobName(name), metav1.GetOptions{})
		if err == nil && cron != nil && (s.Job == nil || s.Job.Schedule == nil) {
			return fmt.Errorf("%s: remove the scheduled job before changing workload kind", name)
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if s.Job != nil && s.Job.Schedule != nil {
			prior, e := c.kube.BatchV1().Jobs(Namespace(t.ApplicationID)).Get(ctx, jobName(name), metav1.GetOptions{})
			if e == nil && prior != nil {
				return fmt.Errorf("%s: remove the deployment job before scheduling it", name)
			}
			if e != nil && !apierrors.IsNotFound(e) {
				return e
			}
		}
		if s.Job != nil {
			d, err := c.kube.AppsV1().Deployments(Namespace(t.ApplicationID)).Get(ctx, name, metav1.GetOptions{})
			if err == nil && d != nil {
				return fmt.Errorf("%s: remove the existing service in a separate deployment before changing it to a job", name)
			}
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		} else {
			j, err := c.kube.BatchV1().Jobs(Namespace(t.ApplicationID)).Get(ctx, jobName(name), metav1.GetOptions{})
			if err == nil && j != nil {
				return fmt.Errorf("%s: remove the existing job in a separate deployment before changing it to a service", name)
			}
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func (c *Client) cleanupJobs(ctx context.Context, t Target) error {
	api := c.kube.BatchV1().Jobs(Namespace(t.ApplicationID))
	jobs, err := api.List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID), Limit: 201})
	if err != nil {
		return err
	}
	if jobs.Continue != "" || len(jobs.Items) > 200 {
		return fmt.Errorf("too many owned jobs for bounded cleanup")
	}
	for _, j := range jobs.Items {
		if svc, ok := t.Spec.Services[j.Labels[serviceKey]]; ok && svc.Job != nil {
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
