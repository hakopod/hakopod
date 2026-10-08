package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

const invocationLabel = "hakopod.io/invocation-id"
const invocationOwner = "hakopod.io/invocation-owner"
const invocationInputHash = "hakopod.io/invocation-input-sha256"
const invocationTemplateLabel = "hakopod.io/invocation-template"

func invocationTemplateName(service string) string {
	h := sha256.Sum256([]byte(service))
	return fmt.Sprintf("invocation-template-%x", h[:12])
}
func invocationTemplateData(t Target, s spec.Service) map[string]string {
	encoded, _ := json.Marshal(s)
	hash := sha256.Sum256(encoded)
	return map[string]string{"revision": strconv.FormatInt(t.Revision, 10), "service_sha256": fmt.Sprintf("%x", hash), "suspended": strconv.FormatBool(s.Suspended)}
}

// The marker records a deployed template, without copying environment or secrets.
func (c *Client) applyInvocationTemplate(ctx context.Context, t Target, name string, s spec.Service) error {
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	if err := c.prepareWorkloadSecrets(ctx, t, name, s); err != nil {
		return err
	}
	if err := c.prepareFiles(ctx, t, name, s); err != nil {
		return err
	}
	labels := labelsFor(t, name)
	labels[invocationTemplateLabel] = "true"
	wanted := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: invocationTemplateName(name), Namespace: Namespace(t.ApplicationID), Labels: labels}, Data: invocationTemplateData(t, s)}
	api := c.kube.CoreV1().ConfigMaps(wanted.Namespace)
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
		if current.Labels[serviceKey] != name || current.Labels[invocationTemplateLabel] != "true" {
			return fmt.Errorf("invocation template ownership is invalid")
		}
		current.Data = wanted.Data
		_, err = api.Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

func (c *Client) observeInvocationTemplate(ctx context.Context, t Target, name string, s spec.Service) (ServiceStatus, error) {
	result := ServiceStatus{Name: name, Status: "missing", Image: s.Image}
	cm, err := c.kube.CoreV1().ConfigMaps(Namespace(t.ApplicationID)).Get(ctx, invocationTemplateName(name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if err = owned(cm, t); err != nil {
		return result, err
	}
	if cm.Labels[serviceKey] != name || cm.Labels[invocationTemplateLabel] != "true" {
		return result, fmt.Errorf("invocation template ownership is invalid")
	}
	wanted := invocationTemplateData(t, s)
	for key, value := range wanted {
		if cm.Data[key] != value {
			result.Status = "pending"
			return result, nil
		}
	}
	result.Status = "configured"
	result.Message = "Invocation template configured; no job starts until an authorized request arrives"
	if s.Suspended {
		result.Status = "stopped"
		result.Message = "Invocation template paused"
	}
	return result, nil
}

// No rollout may replace network, files or credentials while an invocation is
// active. The durable queue controller cancels obsolete revisions first.
func (c *Client) validateActiveInvocations(ctx context.Context, t Target) error {
	jobs, err := c.kube.BatchV1().Jobs(Namespace(t.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + invocationLabel, Limit: 21})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if jobs.Continue != "" || len(jobs.Items) > 20 {
		return fmt.Errorf("too many invocation jobs for bounded deployment validation")
	}
	for _, job := range jobs.Items {
		if err = owned(&job, t); err != nil {
			return err
		}
		if jobState(&job) == "running" || job.DeletionTimestamp != nil {
			return fmt.Errorf("wait for active invocation jobs to finish cleanup before deploying this application")
		}
	}
	return nil
}

func (c *Client) cleanupInvocationTemplates(ctx context.Context, t Target) error {
	api := c.kube.CoreV1().ConfigMaps(Namespace(t.ApplicationID))
	list, err := api.List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + invocationTemplateLabel + "=true", Limit: 21})
	if err != nil {
		return err
	}
	if list.Continue != "" || len(list.Items) > 20 {
		return fmt.Errorf("too many invocation templates for bounded cleanup")
	}
	for _, cm := range list.Items {
		s, ok := t.Spec.Services[cm.Labels[serviceKey]]
		if ok && s.Job != nil && s.Job.Invocation != nil {
			continue
		}
		if err = owned(&cm, t); err != nil {
			return err
		}
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		if err = api.Delete(ctx, cm.Name, deleteOptions(&cm)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
