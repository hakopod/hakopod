package cluster

import (
	"context"
	"fmt"
	"reflect"

	"github.com/hakopod/hakopod/internal/sandbox"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const sandboxRegistrySecret = "session-registry"

// Pull credentials belong to the kubelet. The worker receives no credential mount or service token.
// A rotated credential invalidates the old immutable copy instead of replacing a live kernel.
func (c *Client) sandboxRegistry(ctx context.Context, t Target, r sandbox.Record, s spec.Service, create bool) error {
	if s.RegistryCredential == "" {
		return nil
	}
	credential, err := c.RegistryCredential(ctx, t.Project, t.Environment, s.RegistryCredential, s.Image)
	if err != nil {
		return err
	}
	data := map[string][]byte{corev1.DockerConfigJsonKey: RegistrySecretData(*credential)[corev1.DockerConfigJsonKey]}
	api := c.kube.CoreV1().Secrets(SandboxNamespace(r.ID))
	current, err := api.Get(ctx, sandboxRegistrySecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && create {
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		current, err = api.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sandboxRegistrySecret, Namespace: SandboxNamespace(r.ID), Labels: sandboxLabels(t, r)}, Type: corev1.SecretTypeDockerConfigJson, Immutable: ptr(true), Data: data}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			current, err = api.Get(ctx, sandboxRegistrySecret, metav1.GetOptions{})
		}
	}
	if err != nil {
		return fmt.Errorf("session pull credential is unavailable")
	}
	if err = sandboxOwned(current, t, r); err != nil {
		return err
	}
	if current.Type != corev1.SecretTypeDockerConfigJson || current.Immutable == nil || !*current.Immutable || !reflect.DeepEqual(current.Data, data) {
		return fmt.Errorf("session pull credential changed. Create a new session")
	}
	return nil
}
