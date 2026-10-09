package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const environmentSnapshotLabel = "hakopod.io/environment-snapshot"

func environmentSnapshot(t Target, service string, data map[string][]byte) *corev1.Secret {
	encoded, _ := json.Marshal(struct {
		Application, Service string
		Data                 map[string][]byte
	}{t.ApplicationID, service, data})
	digest := sha256.Sum256(encoded)
	labels := labelsFor(t, service)
	labels[environmentSnapshotLabel] = "true"
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("hp-env-%x", digest[:20]), Namespace: Namespace(t.ApplicationID), Labels: labels},
		Immutable:  ptr(true), Type: corev1.SecretTypeOpaque, Data: data,
	}
}

func environmentSnapshotOwned(secret *corev1.Secret, t Target, service string) error {
	if service == "" || secret.Namespace != Namespace(t.ApplicationID) || owned(secret, t) != nil || secret.DeletionTimestamp != nil || secret.Labels[serviceKey] != service || secret.Labels[environmentSnapshotLabel] != "true" || secret.Immutable == nil || !*secret.Immutable || secret.Type != corev1.SecretTypeOpaque || secret.Name != environmentSnapshot(t, service, secret.Data).Name {
		return fmt.Errorf("service environment snapshot ownership or integrity changed")
	}
	return nil
}

// Pin the environment before starting a workload. A value change changes the
// PodTemplate, while old pods and retained ReplicaSets keep their original
// immutable Secret. Kubernetes cannot start an old revision with newer values.
func (c *Client) pinWorkloadEnvironment(ctx context.Context, t Target, service string, template *corev1.PodTemplateSpec) (bool, error) {
	return c.pinWorkloadEnvironmentValues(ctx, t, service, template, nil)
}

func (c *Client) pinResolvedWorkloadEnvironment(ctx context.Context, t Target, service string, template *corev1.PodTemplateSpec) (bool, error) {
	values, err := c.workloadEnvironmentData(ctx, t, service, spec.EffectiveService(t.Spec, t.Spec.Services[service]))
	if err != nil {
		return false, err
	}
	return c.pinWorkloadEnvironmentValues(ctx, t, service, template, values)
}

func (c *Client) pinWorkloadEnvironmentValues(ctx context.Context, t Target, service string, template *corev1.PodTemplateSpec, values map[string][]byte) (bool, error) {
	svc := spec.EffectiveService(t.Spec, t.Spec.Services[service])
	if len(svc.Secrets) == 0 && len(svc.Bindings) == 0 {
		return false, nil
	}
	if len(template.Spec.Containers) == 0 || template.Spec.Containers[0].Name != "app" {
		return false, fmt.Errorf("service application container is unavailable")
	}
	api := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID))
	current, err := api.Get(ctx, service+"-environment", metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if owned(current, t) != nil || current.Labels[serviceKey] != service || current.DeletionTimestamp != nil || current.Type != corev1.SecretTypeOpaque {
		return false, fmt.Errorf("service environment ownership changed")
	}
	if values == nil {
		values = current.Data
	}
	wanted := environmentSnapshot(t, service, values)
	keys := map[string]bool{}
	for key := range svc.Secrets {
		keys[key] = true
	}
	for key, binding := range svc.Bindings {
		keys[key] = true
		if binding.ManagedDatabase != "" && t.databaseConnections[service][key].CA != "" && spec.DatabaseClientProfile(svc, key) == spec.DatabaseClientInfisicalPostgresV1 {
			keys["DB_ROOT_CERT"] = true
		}
	}
	if len(wanted.Data) != len(keys) {
		return false, fmt.Errorf("service environment snapshot has unexpected or missing values")
	}
	// Validate every declaration and the previous snapshot before creating or
	// changing anything. An annotation alone does not establish Secret ownership.
	candidate := template.DeepCopy()
	previous := map[string]bool{}
	changed := false
	seen := map[string]bool{}
	for i := range candidate.Spec.Containers[0].Env {
		env := &candidate.Spec.Containers[0].Env[i]
		if !keys[env.Name] {
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && (env.ValueFrom.SecretKeyRef.Name == current.Name || env.ValueFrom.SecretKeyRef.Name == candidate.Annotations[environmentSnapshotLabel]) {
				return false, fmt.Errorf("service environment has an undeclared secret reference")
			}
			continue
		}
		if seen[env.Name] || env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil || env.Value != "" {
			return false, fmt.Errorf("service environment declaration changed; redeploy the accepted revision")
		}
		seen[env.Name] = true
		ref := env.ValueFrom.SecretKeyRef
		if ref.Key != env.Name || ref.Optional != nil && *ref.Optional {
			return false, fmt.Errorf("service environment reference changed; redeploy the accepted revision")
		}
		if _, exists := wanted.Data[ref.Key]; !exists {
			return false, fmt.Errorf("service environment snapshot is incomplete")
		}
		if ref.Name != current.Name {
			if ref.Name == "" || ref.Name != candidate.Annotations[environmentSnapshotLabel] {
				return false, fmt.Errorf("service environment reference belongs to another Secret")
			}
			if !previous[ref.Name] {
				old, err := api.Get(ctx, ref.Name, metav1.GetOptions{})
				if err != nil {
					return false, fmt.Errorf("previous service environment snapshot is unavailable")
				}
				if err = environmentSnapshotOwned(old, t, service); err != nil {
					return false, err
				}
				previous[ref.Name] = true
			}
		}
		changed = changed || ref.Name != wanted.Name
		ref.Name = wanted.Name
	}
	if len(seen) != len(keys) {
		return false, fmt.Errorf("service environment declaration is incomplete; redeploy the accepted revision")
	}
	for _, env := range candidate.Spec.Containers[0].EnvFrom {
		if env.SecretRef != nil {
			return false, fmt.Errorf("service environment has an undeclared bulk secret reference")
		}
	}
	old, err := api.Get(ctx, wanted.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = c.cleanupEnvironmentSnapshots(ctx, t, wanted.Name); err != nil {
			return false, err
		}
		if err = beforeStep(ctx, t); err != nil {
			return false, err
		}
		old, err = api.Create(ctx, wanted, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			old, err = api.Get(ctx, wanted.Name, metav1.GetOptions{})
		}
	}
	if err != nil {
		return false, err
	}
	if err = environmentSnapshotOwned(old, t, service); err != nil {
		return false, err
	}
	if !reflect.DeepEqual(old.Data, wanted.Data) {
		return false, fmt.Errorf("service environment snapshot contents changed")
	}
	if candidate.Annotations == nil {
		candidate.Annotations = map[string]string{}
	}
	changed = changed || candidate.Annotations[environmentSnapshotLabel] != wanted.Name
	candidate.Annotations[environmentSnapshotLabel] = wanted.Name
	*template = *candidate
	return changed, nil
}

// Read every bounded inventory before deleting anything. Running pods,
// retained ReplicaSets and jobs protect their environment through a rollout.
func (c *Client) cleanupEnvironmentSnapshots(ctx context.Context, t Target, keep string) error {
	ns := Namespace(t.ApplicationID)
	namespace, err := c.kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = owned(namespace, t); err != nil {
		return err
	}
	selector := managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + environmentSnapshotLabel + "=true"
	secrets, err := c.kube.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 129})
	if err != nil {
		return err
	}
	if secrets.Continue != "" || len(secrets.Items) > 128 {
		return fmt.Errorf("environment snapshot history exceeds 128 Secrets")
	}
	if len(secrets.Items) == 0 {
		return nil
	}
	used := map[string]bool{keep: true}
	mark := func(p corev1.PodSpec) {
		for _, secret := range secrets.Items {
			if podReferencesRetiredSecret(p, secret.Name) {
				used[secret.Name] = true
			}
		}
	}
	opts := metav1.ListOptions{Limit: 401}
	pods, err := c.kube.CoreV1().Pods(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if pods.Continue != "" || len(pods.Items) > 400 {
		return fmt.Errorf("too many pods for environment snapshot cleanup")
	}
	for _, p := range pods.Items {
		mark(p.Spec)
	}
	deps, err := c.kube.AppsV1().Deployments(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if deps.Continue != "" || len(deps.Items) > 400 {
		return fmt.Errorf("too many deployments for environment snapshot cleanup")
	}
	for _, d := range deps.Items {
		mark(d.Spec.Template.Spec)
	}
	replicas, err := c.kube.AppsV1().ReplicaSets(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if replicas.Continue != "" || len(replicas.Items) > 400 {
		return fmt.Errorf("too many replica sets for environment snapshot cleanup")
	}
	for _, r := range replicas.Items {
		mark(r.Spec.Template.Spec)
	}
	jobs, err := c.kube.BatchV1().Jobs(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if jobs.Continue != "" || len(jobs.Items) > 400 {
		return fmt.Errorf("too many jobs for environment snapshot cleanup")
	}
	for _, j := range jobs.Items {
		mark(j.Spec.Template.Spec)
	}
	schedules, err := c.kube.BatchV1().CronJobs(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if schedules.Continue != "" || len(schedules.Items) > 400 {
		return fmt.Errorf("too many schedules for environment snapshot cleanup")
	}
	for _, j := range schedules.Items {
		mark(j.Spec.JobTemplate.Spec.Template.Spec)
	}
	stateful, err := c.kube.AppsV1().StatefulSets(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if stateful.Continue != "" || len(stateful.Items) > 400 {
		return fmt.Errorf("too many stateful sets for environment snapshot cleanup")
	}
	for _, s := range stateful.Items {
		mark(s.Spec.Template.Spec)
	}
	daemons, err := c.kube.AppsV1().DaemonSets(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if daemons.Continue != "" || len(daemons.Items) > 400 {
		return fmt.Errorf("too many daemon sets for environment snapshot cleanup")
	}
	for _, d := range daemons.Items {
		mark(d.Spec.Template.Spec)
	}
	controllers, err := c.kube.CoreV1().ReplicationControllers(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if controllers.Continue != "" || len(controllers.Items) > 400 {
		return fmt.Errorf("too many replication controllers for environment snapshot cleanup")
	}
	for _, r := range controllers.Items {
		if r.Spec.Template != nil {
			mark(r.Spec.Template.Spec)
		}
	}
	accounts, err := c.kube.CoreV1().ServiceAccounts(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if accounts.Continue != "" || len(accounts.Items) > 400 {
		return fmt.Errorf("too many service accounts for environment snapshot cleanup")
	}
	for _, account := range accounts.Items {
		for _, ref := range account.Secrets {
			used[ref.Name] = true
		}
		for _, ref := range account.ImagePullSecrets {
			used[ref.Name] = true
		}
	}
	// Check all candidates before the first deletion. Exact UID and resource
	// version preconditions protect a Secret replaced after this inventory.
	for _, secret := range secrets.Items {
		if err = environmentSnapshotOwned(&secret, t, secret.Labels[serviceKey]); err != nil {
			return err
		}
		if !used[secret.Name] && (secret.UID == "" || secret.ResourceVersion == "") {
			return fmt.Errorf("environment snapshot identity is unavailable")
		}
	}
	retained := 0
	for _, secret := range secrets.Items {
		if used[secret.Name] {
			retained++
			continue
		}
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		if err = c.kube.CoreV1().Secrets(ns).Delete(ctx, secret.Name, deleteOptions(&secret)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if retained >= 128 {
		return fmt.Errorf("environment snapshot history is full; retire unused workloads before changing bindings")
	}
	return nil
}
