package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var errRetiredConsumerTerminating = errors.New("retired credential consumer is terminating")
var errRetiredSecretReferenced = errors.New("a workload still references a retired service secret")
var errRetiredAccountSecretReferenced = errors.New("a service account still references a retired service secret")

// Retire generated credentials only after their service and all consumers stop.
// Platform secret references, certificates and current services remain intact.
func (c *Client) cleanupRetiredSecrets(ctx context.Context, t Target) error {
	ns := Namespace(t.ApplicationID)
	namespace, err := c.kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = owned(namespace, t); err != nil {
		return err
	}
	selector := managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + serviceKey + "," + fileObjectLabel + "!=true"
	if current := spec.Names(t.Spec); len(current) > 0 {
		selector += "," + serviceKey + " notin (" + strings.Join(current, ",") + ")"
	}
	list, err := c.kube.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 401})
	if err != nil {
		return fmt.Errorf("retired service secret inventory is unavailable")
	}
	if list.Continue != "" || len(list.Items) > 400 {
		return fmt.Errorf("retired service secret inventory exceeds 400 objects")
	}
	candidates := map[string]corev1.Secret{}
	services := map[string]bool{}
	for _, secret := range list.Items {
		name := secret.Labels[serviceKey]
		if _, current := t.Spec.Services[name]; current || name == "" || secret.Name != name+"-environment" {
			continue
		}
		if err = owned(&secret, t); err != nil {
			return err
		}
		if secret.Type != corev1.SecretTypeOpaque || secret.UID == "" || secret.ResourceVersion == "" {
			return fmt.Errorf("retired service secret identity could not be verified")
		}
		candidates[secret.Name] = secret
		services[name] = true
	}
	if len(candidates) == 0 {
		return nil
	}
	for {
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		err = c.checkRetiredSecretConsumers(ctx, ns, services, candidates)
		if !errors.Is(err, errRetiredConsumerTerminating) {
			if err != nil {
				return err
			}
			break
		}
		if err = sleepContext(ctx, 500*time.Millisecond); err != nil {
			return fmt.Errorf("retired service credential cleanup did not finish: %w", err)
		}
	}
	for _, secret := range candidates {
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		if err = c.kube.CoreV1().Secrets(ns).Delete(ctx, secret.Name, deleteOptions(&secret)); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("retired service secret changed or could not be removed")
		}
	}
	return nil
}

// Inspect every built-in workload, including foreign and terminating objects.
// An empty replica count does not make a controller safe to ignore.
func (c *Client) checkRetiredSecretConsumers(ctx context.Context, ns string, services map[string]bool, secrets map[string]corev1.Secret) error {
	check := func(meta metav1.Object, templateLabels map[string]string, p corev1.PodSpec) error {
		if services[meta.GetName()] || services[meta.GetLabels()[serviceKey]] || services[templateLabels[serviceKey]] {
			if meta.GetDeletionTimestamp() != nil {
				return errRetiredConsumerTerminating
			}
			return fmt.Errorf("a retired service still has a workload; wait for removal before deleting its credentials")
		}
		for name := range secrets {
			if podReferencesRetiredSecret(p, name) {
				if meta.GetDeletionTimestamp() != nil {
					return errRetiredConsumerTerminating
				}
				return errRetiredSecretReferenced
			}
		}
		return nil
	}
	bound := func(next string, count, max int) error {
		if next != "" || count > max {
			return fmt.Errorf("workload inventory exceeds the retired secret cleanup limit")
		}
		return nil
	}
	pods, err := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: 401})
	if err != nil {
		return err
	}
	if err = bound(pods.Continue, len(pods.Items), 400); err != nil {
		return err
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if err = check(p, nil, p.Spec); err != nil {
			return err
		}
	}
	opts := metav1.ListOptions{Limit: 201}
	deployments, err := c.kube.AppsV1().Deployments(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if err = bound(deployments.Continue, len(deployments.Items), 200); err != nil {
		return err
	}
	for i := range deployments.Items {
		p := &deployments.Items[i]
		if err = check(p, p.Spec.Template.Labels, p.Spec.Template.Spec); err != nil {
			return err
		}
	}
	stateful, err := c.kube.AppsV1().StatefulSets(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if err = bound(stateful.Continue, len(stateful.Items), 200); err != nil {
		return err
	}
	for i := range stateful.Items {
		p := &stateful.Items[i]
		if err = check(p, p.Spec.Template.Labels, p.Spec.Template.Spec); err != nil {
			return err
		}
	}
	replicas, err := c.kube.AppsV1().ReplicaSets(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if err = bound(replicas.Continue, len(replicas.Items), 200); err != nil {
		return err
	}
	for i := range replicas.Items {
		p := &replicas.Items[i]
		if err = check(p, p.Spec.Template.Labels, p.Spec.Template.Spec); err != nil {
			return err
		}
	}
	daemons, err := c.kube.AppsV1().DaemonSets(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if err = bound(daemons.Continue, len(daemons.Items), 200); err != nil {
		return err
	}
	for i := range daemons.Items {
		p := &daemons.Items[i]
		if err = check(p, p.Spec.Template.Labels, p.Spec.Template.Spec); err != nil {
			return err
		}
	}
	jobs, err := c.kube.BatchV1().Jobs(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if err = bound(jobs.Continue, len(jobs.Items), 200); err != nil {
		return err
	}
	for i := range jobs.Items {
		p := &jobs.Items[i]
		if err = check(p, p.Spec.Template.Labels, p.Spec.Template.Spec); err != nil {
			return err
		}
	}
	cron, err := c.kube.BatchV1().CronJobs(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if err = bound(cron.Continue, len(cron.Items), 200); err != nil {
		return err
	}
	for i := range cron.Items {
		p := &cron.Items[i]
		if err = check(p, p.Spec.JobTemplate.Spec.Template.Labels, p.Spec.JobTemplate.Spec.Template.Spec); err != nil {
			return err
		}
	}
	controllers, err := c.kube.CoreV1().ReplicationControllers(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if err = bound(controllers.Continue, len(controllers.Items), 200); err != nil {
		return err
	}
	for i := range controllers.Items {
		p := &controllers.Items[i]
		if p.Spec.Template != nil {
			if err = check(p, p.Spec.Template.Labels, p.Spec.Template.Spec); err != nil {
				return err
			}
		} else if err = check(p, nil, corev1.PodSpec{}); err != nil {
			return err
		}
	}
	accounts, err := c.kube.CoreV1().ServiceAccounts(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if err = bound(accounts.Continue, len(accounts.Items), 200); err != nil {
		return err
	}
	for _, a := range accounts.Items {
		for _, ref := range a.Secrets {
			if _, used := secrets[ref.Name]; used {
				return errRetiredAccountSecretReferenced
			}
		}
		for _, ref := range a.ImagePullSecrets {
			if _, used := secrets[ref.Name]; used {
				return errRetiredAccountSecretReferenced
			}
		}
	}
	return nil
}
