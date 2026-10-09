package cluster

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const bindingVerifiedSnapshot = "hakopod.io/binding-verified-snapshot"

// RefreshDatabaseBindings runs under the application's durable runtime claim.
// It refreshes environment and public CA references together; images, replicas
// and completed jobs retain their accepted configuration. New invocations
// resolve their own credentials when they start.
func (c *Client) RefreshDatabaseBindings(ctx context.Context, t Target, emit func(Event), name string) error {
	svc, ok := t.Spec.Services[name]
	if !ok {
		return fmt.Errorf("database binding service is unavailable")
	}
	if len(svc.Bindings) == 0 || svc.Session != nil || svc.Actions != nil || svc.Job != nil && svc.Job.Schedule == nil {
		return nil
	}
	normalized, err := spec.Normalize(t.Spec)
	if err != nil {
		return err
	}
	t.Spec = spec.RuntimeEnvironment(normalized)
	svc = t.Spec.Services[name]
	selected := t
	selected.Spec.Services = map[string]spec.Service{name: svc}
	if err = c.snapshotWorkloadSecrets(ctx, &selected); err != nil {
		return err
	}
	if err = c.snapshotDatabaseBindings(ctx, &selected); err != nil {
		return err
	}
	t.secretValues, t.databaseConnections = selected.secretValues, selected.databaseConnections
	check := func(obj metav1.Object, revisionKey string) error {
		if owned(obj, t) != nil || obj.GetDeletionTimestamp() != nil || obj.GetLabels()[serviceKey] != name || obj.GetAnnotations()[revisionKey] != strconv.FormatInt(t.Revision, 10) {
			return fmt.Errorf("database binding refresh is waiting for the accepted deployment")
		}
		return beforeStep(ctx, t)
	}
	if svc.Job != nil {
		api := c.kube.BatchV1().CronJobs(Namespace(t.ApplicationID))
		current, err := api.Get(ctx, scheduledJobName(name), metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err = check(current, jobRevision); err != nil {
			return err
		}
		changed, err := c.refreshBindingTemplate(ctx, t, name, &current.Spec.JobTemplate.Spec.Template)
		if err != nil || !changed {
			return err
		}
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		if _, err = api.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			return err
		}
		if emit != nil {
			emit(Event{Type: "binding_refreshed", Service: name, Message: "Database binding values updated for future scheduled jobs. Existing jobs keep their original configuration."})
		}
		return nil
	}
	api := c.kube.AppsV1().Deployments(Namespace(t.ApplicationID))
	current, err := api.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = check(current, "hakopod.io/revision"); err != nil {
		return err
	}
	changed, err := c.refreshBindingTemplate(ctx, t, name, &current.Spec.Template)
	if err != nil {
		return err
	}
	if changed {
		delete(current.Annotations, bindingVerifiedSnapshot)
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		if _, err = api.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			return err
		}
		if emit != nil {
			emit(Event{Type: "binding_rollout", Service: name, Message: "Database binding settings changed. Replacing affected pods with the new values and public CA."})
		}
		return nil
	}
	// Check the rollout on subsequent maintenance passes. Do not hold the
	// observation loop during startup, or mistake its short deadline for a
	// failed deployment. A cancelled pass leaves verification pending.
	snapshot := current.Spec.Template.Annotations[environmentSnapshotLabel]
	if current.Annotations[bindingVerifiedSnapshot] == snapshot || !readyDeployment(current) {
		return nil
	}
	ready, err := c.readyPods(ctx, t, name, current)
	if err != nil || !ready {
		return err
	}
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	current.Annotations[bindingVerifiedSnapshot] = snapshot
	if _, err = api.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		return err
	}
	if emit != nil {
		if current.Spec.Replicas != nil && *current.Spec.Replicas == 0 {
			emit(Event{Type: "binding_refreshed", Service: name, Message: "Database binding values updated for the next start. This service has no running replicas."})
		} else {
			emit(Event{Type: "binding_ready", Service: name, Message: "All desired pods are ready with the current database binding snapshot."})
		}
	}
	return nil
}

// Both references change on the local candidate. A failure leaves the caller's
// template unchanged, and the caller publishes the pair in one update.
func (c *Client) refreshBindingTemplate(ctx context.Context, t Target, name string, template *corev1.PodTemplateSpec) (bool, error) {
	candidate := template.DeepCopy()
	environmentChanged, err := c.pinResolvedWorkloadEnvironment(ctx, t, name, candidate)
	if err != nil {
		return false, err
	}
	trustChanged, err := c.renewPodDatabaseTrust(ctx, t, name, &candidate.Spec)
	if err != nil {
		return false, err
	}
	*template = *candidate
	return environmentChanged || trustChanged, nil
}
