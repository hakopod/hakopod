package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/invocation"
	"github.com/hakopod/hakopod/internal/spec"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var invocationIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func invocationJobName(id string) string    { return "invocation-" + id }
func invocationSecretName(id string) string { return "invocation-input-" + id }
func invocationTarget(r invocation.Record) (Target, spec.Service, error) {
	if !invocationIDPattern.MatchString(r.ID) || r.ApplicationID == "" || r.Revision < 1 || len(r.OwnerHash) != 64 || len(r.InputHash) != 64 {
		return Target{}, spec.Service{}, fmt.Errorf("invocation runtime metadata is invalid")
	}
	app, err := spec.Normalize(r.Source)
	if err != nil {
		return Target{}, spec.Service{}, fmt.Errorf("invocation source is invalid")
	}
	app = spec.RuntimeEnvironment(app)
	s, ok := app.Services[r.Service]
	if !ok || s.Job == nil || s.Job.Invocation == nil || !strings.Contains(s.Image, "@sha256:") {
		return Target{}, spec.Service{}, fmt.Errorf("invocation requires an immutable predefined job")
	}
	return Target{Project: r.Project, Environment: r.Environment, ApplicationID: r.ApplicationID, OperationID: r.ID, Revision: r.Revision, Spec: app}, s, nil
}
func (c *Client) invocationNamespace(ctx context.Context, t Target, r invocation.Record) (string, error) {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(r.ApplicationID), metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if err = owned(ns, t); err != nil {
		return "", err
	}
	if ns.DeletionTimestamp != nil || r.NamespaceUID != "" && string(ns.UID) != r.NamespaceUID {
		return "", fmt.Errorf("invocation application namespace changed")
	}
	return string(ns.UID), nil
}
func validateInvocationJob(job *batchv1.Job, t Target, r invocation.Record) error {
	if err := owned(job, t); err != nil {
		return err
	}
	if job.Name != invocationJobName(r.ID) || job.Labels[serviceKey] != r.Service || job.Labels[invocationLabel] != r.ID || job.Labels[invocationOwner] != r.OwnerHash[:32] || job.Annotations[jobRevision] != strconv.FormatInt(r.Revision, 10) || job.Annotations[invocationInputHash] != r.InputHash {
		return fmt.Errorf("invocation job identity does not match its receipt")
	}
	if r.RuntimeUID != "" && string(job.UID) != r.RuntimeUID {
		return fmt.Errorf("invocation job UID changed")
	}
	return nil
}

// StartInvocation reconciles one deterministic Job. The caller must hold the
// durable service lease and revalidate authority through before each side effect.
// Once RuntimeUID is recorded, a missing Job is never recreated.
func (c *Client) StartInvocation(ctx context.Context, r invocation.Record, input []byte, before func(context.Context) error) (invocation.RuntimeState, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	t, s, err := invocationTarget(r)
	if err != nil {
		return invocation.RuntimeState{}, err
	}
	t.BeforeStep = before
	if err = beforeStep(ctx, t); err != nil {
		return invocation.RuntimeState{}, err
	}
	nsUID, err := c.invocationNamespace(ctx, t, r)
	if err != nil {
		return invocation.RuntimeState{}, err
	}
	result := invocation.RuntimeState{NamespaceUID: nsUID}
	api := c.kube.BatchV1().Jobs(Namespace(r.ApplicationID))
	current, err := api.Get(ctx, invocationJobName(r.ID), metav1.GetOptions{})
	if err == nil {
		if err = validateInvocationJob(current, t, r); err != nil {
			return result, err
		}
		return c.invocationState(ctx, t, r, current, nsUID)
	}
	if !apierrors.IsNotFound(err) {
		return result, err
	}
	if r.RuntimeUID != "" {
		return result, fmt.Errorf("recorded invocation job is missing; automatic recreation is disabled")
	}
	if !invocation.AllowsIdentity(s.Job.Invocation, r.IdentityID) {
		return result, fmt.Errorf("invocation identity is not permitted by this template")
	}
	if r.CancelRequested || s.Suspended {
		return result, fmt.Errorf("invocation execution is cancelled or paused")
	}
	marker, err := c.observeInvocationTemplate(ctx, t, r.Service, s)
	if err != nil {
		return result, err
	}
	if marker.Status != "configured" {
		return result, fmt.Errorf("invocation template revision is not active")
	}
	var inputs map[string]json.RawMessage
	if len(input) > invocation.MaxInputBytes || json.Unmarshal(input, &inputs) != nil {
		return result, fmt.Errorf("invocation input is invalid")
	}
	normalized, err := invocation.NormalizeInputs(s.Job.Invocation, inputs)
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256(normalized)
	if fmt.Sprintf("%x", sum) != r.InputHash || !reflect.DeepEqual(normalized, input) {
		return result, fmt.Errorf("invocation input digest does not match its receipt")
	}
	if err = c.resolveVirtualNetworks(ctx, &t); err != nil {
		return result, err
	}
	// Resolve only this service's secrets while evaluating policy against the full application.
	t.policy, err = c.workloadPolicy(ctx, t)
	if err != nil {
		return result, err
	}
	full := t.Spec
	t.Spec.Services = map[string]spec.Service{r.Service: s}
	if err = c.snapshotWorkloadSecrets(ctx, &t); err != nil {
		return result, err
	}
	if err = c.snapshotDatabaseBindings(ctx, &t); err != nil {
		return result, err
	}
	t.Spec = full
	t.privateEgress, err = c.resolvePrivateEgress(t)
	if err != nil {
		return result, err
	}
	t.containerDaemon, err = c.resolveContainerDaemons(t)
	if err != nil {
		return result, err
	}
	if err = c.prepareWorkloadSecrets(ctx, t, r.Service, s); err != nil {
		return result, err
	}
	if err = c.prepareFiles(ctx, t, r.Service, s); err != nil {
		return result, err
	}
	d := deployment(t, r.Service, s, c.options.RolloutTimeout, c.options.ReadinessProbeImage)
	if _, err := c.pinResolvedWorkloadEnvironment(ctx, t, r.Service, &d.Spec.Template); err != nil {
		return result, err
	}
	if err = c.prepareAWSIdentity(ctx, t, r.Service, s, d); err != nil {
		return result, err
	}
	if err = c.prepareRuntimeProfile(ctx, t, r.Service, s, d); err != nil {
		return result, err
	}
	if err = c.prepareRegistryCredential(ctx, t, r.Service, s, d); err != nil {
		return result, err
	}
	labels := labelsFor(t, r.Service)
	labels[invocationLabel] = r.ID
	labels[invocationOwner] = r.OwnerHash[:32]
	annotations := map[string]string{jobRevision: strconv.FormatInt(r.Revision, 10), invocationInputHash: r.InputHash}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: invocationSecretName(r.ID), Namespace: Namespace(r.ApplicationID), Labels: labels, Annotations: annotations}, Type: corev1.SecretTypeOpaque, Immutable: ptr(true), Data: map[string][]byte{"input.json": input}}
	secrets := c.kube.CoreV1().Secrets(secret.Namespace)
	existing, e := secrets.Get(ctx, secret.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(e) {
		if err = beforeStep(ctx, t); err != nil {
			return result, err
		}
		_, e = secrets.Create(ctx, secret, metav1.CreateOptions{})
	} else if e == nil {
		if e = owned(existing, t); e == nil && (existing.Immutable == nil || !*existing.Immutable || !reflect.DeepEqual(existing.Data, secret.Data) || existing.Labels[invocationLabel] != r.ID || existing.Annotations[invocationInputHash] != r.InputHash) {
			e = fmt.Errorf("invocation input secret integrity check failed")
		}
	}
	if e != nil {
		return result, e
	}
	pod := &d.Spec.Template.Spec
	pod.RestartPolicy = corev1.RestartPolicyNever
	pod.Volumes = append(pod.Volumes, corev1.Volume{Name: "invocation-input", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secret.Name, DefaultMode: ptr(int32(0444)), Items: []corev1.KeyToPath{{Key: "input.json", Path: "input.json"}}}}})
	pod.Containers[0].VolumeMounts = append(pod.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "invocation-input", MountPath: "/run/hakopod/invocation", ReadOnly: true})
	d.Spec.Template.Labels[invocationLabel] = r.ID
	d.Spec.Template.Labels[invocationOwner] = r.OwnerHash[:32]
	wanted := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: invocationJobName(r.ID), Namespace: Namespace(r.ApplicationID), Labels: labels, Annotations: annotations}, Spec: batchv1.JobSpec{Template: d.Spec.Template, BackoffLimit: ptr(int32(0)), ActiveDeadlineSeconds: ptr(s.Job.TimeoutSeconds), Completions: ptr(int32(1)), Parallelism: ptr(int32(1))}}
	if err = beforeStep(ctx, t); err != nil {
		return result, err
	}
	// No TTL: the controller persists a terminal receipt before deleting this Job.
	current, err = api.Create(ctx, wanted, metav1.CreateOptions{})
	if err != nil {
		return result, err
	} // An ambiguous response is recovered by deterministic name on the next lease.
	return c.invocationState(ctx, t, r, current, nsUID)
}

func (c *Client) ObserveInvocation(ctx context.Context, r invocation.Record) (invocation.RuntimeState, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	t, _, err := invocationTarget(r)
	if err != nil {
		return invocation.RuntimeState{}, err
	}
	nsUID, err := c.invocationNamespace(ctx, t, r)
	if err != nil {
		return invocation.RuntimeState{}, err
	}
	result := invocation.RuntimeState{NamespaceUID: nsUID}
	job, err := c.kube.BatchV1().Jobs(Namespace(r.ApplicationID)).Get(ctx, invocationJobName(r.ID), metav1.GetOptions{})
	if err != nil {
		return result, err
	}
	if err = validateInvocationJob(job, t, r); err != nil {
		return result, err
	}
	return c.invocationState(ctx, t, r, job, nsUID)
}
func (c *Client) invocationPods(ctx context.Context, t Target, r invocation.Record, uid types.UID) ([]corev1.Pod, error) {
	list, err := c.kube.CoreV1().Pods(Namespace(r.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: invocationLabel + "=" + r.ID, Limit: 5})
	if err != nil {
		return nil, err
	}
	if list.Continue != "" || len(list.Items) > 4 {
		return nil, fmt.Errorf("invocation pod count exceeds its bound")
	}
	for _, pod := range list.Items {
		if err = owned(&pod, t); err != nil {
			return nil, err
		}
		controlled := false
		for _, owner := range pod.OwnerReferences {
			if owner.Kind == "Job" && owner.UID == uid && owner.Name == invocationJobName(r.ID) && owner.Controller != nil && *owner.Controller {
				controlled = true
			}
		}
		if !controlled {
			return nil, fmt.Errorf("invocation pod ownership does not match its Job")
		}
	}
	return list.Items, nil
}
func (c *Client) invocationState(ctx context.Context, t Target, r invocation.Record, job *batchv1.Job, nsUID string) (invocation.RuntimeState, error) {
	result := invocation.RuntimeState{NamespaceUID: nsUID, RuntimeUID: string(job.UID), Status: invocation.Running, Message: "Job is running"}
	switch jobState(job) {
	case "completed":
		result.Status = invocation.Succeeded
		result.Message = "Job completed"
	case "failed":
		result.Status = invocation.Failed
		result.Message = "Job failed or exceeded its deadline"
	}
	pods, err := c.invocationPods(ctx, t, r, job.UID)
	if err != nil {
		return result, err
	}
	for _, pod := range pods {
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == "app" && status.State.Terminated != nil {
				code := status.State.Terminated.ExitCode
				result.ExitCode = &code
			}
		}
	}
	// Runtime messages use fixed text. Application output is available only through
	// the separately authorized logs endpoint, encrypted in durable storage.
	if result.Status != invocation.Running {
		for _, pod := range pods {
			remaining := invocation.MaxLogBytes - len(result.Log)
			if remaining <= 0 {
				result.LogTruncated = true
				break
			}
			stream, e := c.kube.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: "app", LimitBytes: ptr(int64(remaining + 1))}).Stream(ctx)
			if e != nil {
				return result, fmt.Errorf("invocation terminal logs are not available")
			}
			data, e := io.ReadAll(io.LimitReader(stream, int64(remaining+1)))
			stream.Close()
			if e != nil {
				return result, fmt.Errorf("invocation terminal logs could not be read")
			}
			if len(data) > remaining {
				data = data[:remaining]
				result.LogTruncated = true
			}
			result.Log = append(result.Log, data...)
		}
	}
	return result, nil
}

// CleanupInvocation deletes only the recorded Job and input Secret. A true
// result confirms that all owned pods have disappeared. It does not set status.
func (c *Client) CleanupInvocation(ctx context.Context, r invocation.Record) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	t, _, err := invocationTarget(r)
	if err != nil {
		return false, err
	}
	_, err = c.invocationNamespace(ctx, t, r)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	api := c.kube.BatchV1().Jobs(Namespace(r.ApplicationID))
	job, err := api.Get(ctx, invocationJobName(r.ID), metav1.GetOptions{})
	uid := types.UID(r.RuntimeUID)
	if err == nil {
		if err = validateInvocationJob(job, t, r); err != nil {
			return false, err
		}
		uid = job.UID
		opts := deleteOptions(job)
		opts.PropagationPolicy = ptr(metav1.DeletePropagationForeground)
		if err = api.Delete(ctx, job.Name, opts); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
	} else if !apierrors.IsNotFound(err) {
		return false, err
	}
	pods, err := c.invocationPods(ctx, t, r, uid)
	if err != nil {
		return false, err
	}
	if len(pods) > 0 {
		return false, nil
	}
	if _, err = api.Get(ctx, invocationJobName(r.ID), metav1.GetOptions{}); err == nil {
		return false, nil
	} else if !apierrors.IsNotFound(err) {
		return false, err
	}
	secrets := c.kube.CoreV1().Secrets(Namespace(r.ApplicationID))
	secret, err := secrets.Get(ctx, invocationSecretName(r.ID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if err = owned(secret, t); err != nil {
		return false, err
	}
	if secret.Labels[invocationLabel] != r.ID || secret.Labels[serviceKey] != r.Service || secret.Annotations[invocationInputHash] != r.InputHash {
		return false, fmt.Errorf("invocation input secret ownership is invalid")
	}
	if err = secrets.Delete(ctx, secret.Name, deleteOptions(secret)); err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	return true, nil
}
