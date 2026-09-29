package cluster

import (
	"context"
	"errors"

	"github.com/hakopod/hakopod/internal/logmask"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func managedRunnerDiagnostics(pod corev1.Pod) bool {
	return pod.Labels[gitlabActionsProviderLabel] != "" || pod.Spec.RuntimeClassName != nil && *pod.Spec.RuntimeClassName == ActionsRuntime
}

// podLogMasker resolves only secrets actually referenced by this owned pod.
// The resulting values stay within the reader and never become API metadata.
func (c *Client) podLogMasker(ctx context.Context, pod corev1.Pod, containerName string) (*logmask.Masker, error) {
	invalid := errors.New("pod log redaction secrets are unavailable")
	if pod.Labels[managedBy] != "hakopod" || pod.Labels[ownerKey] == "" || pod.Namespace != "hp-"+pod.Labels[ownerKey] {
		return nil, invalid
	}
	mask := logmask.New()
	names := map[string]bool{}
	mounts := map[string]bool{}
	found := false
	for _, container := range pod.Spec.Containers {
		if container.Name != containerName {
			continue
		}
		found = true
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
				names[env.ValueFrom.SecretKeyRef.Name] = true
			}
			if logmask.SensitiveName(env.Name) {
				mask.Add(env.Value)
			}
		}
		for _, source := range container.EnvFrom {
			if source.SecretRef != nil {
				names[source.SecretRef.Name] = true
			}
		}
		for _, mount := range container.VolumeMounts {
			mounts[mount.Name] = true
		}
	}
	if !found {
		return nil, invalid
	}
	for _, volume := range pod.Spec.Volumes {
		if !mounts[volume.Name] {
			continue
		}
		if volume.Secret != nil {
			names[volume.Secret.SecretName] = true
		}
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.Secret != nil {
					names[source.Secret.Name] = true
				}
			}
		}
	}
	if len(names) > 32 {
		return nil, invalid
	}
	total := 0
	for name := range names {
		if name == "" {
			return nil, invalid
		}
		secret, err := c.kube.CoreV1().Secrets(pod.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil || secret.Labels[managedBy] != "hakopod" || secret.Labels[ownerKey] != pod.Labels[ownerKey] {
			return nil, invalid
		}
		for _, value := range secret.Data {
			total += len(value)
			if total > 512<<10 {
				return nil, invalid
			}
			mask.Add(string(value))
		}
	}
	return mask, nil
}
