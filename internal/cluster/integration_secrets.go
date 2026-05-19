package cluster

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const PlatformNamespace = "hakopod-system"
const platformSecretLabel = "hakopod.io/platform-secret"

func (c *Client) platformNamespace(ctx context.Context, create bool) error {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, PlatformNamespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && create {
		ns, err = c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: PlatformNamespace, Labels: map[string]string{managedBy: "hakopod", "pod-security.kubernetes.io/enforce": "restricted"}}}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			ns, err = c.kube.CoreV1().Namespaces().Get(ctx, PlatformNamespace, metav1.GetOptions{})
		}
	}
	if err != nil {
		return err
	}
	if ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("platform namespace is not owned by Hakopod")
	}
	return nil
}

func validPlatformSecretName(name string) error {
	if len(name) > 100 || len(validation.IsDNS1123Subdomain(name)) > 0 {
		return fmt.Errorf("invalid platform secret name")
	}
	return nil
}

// Platform credentials stay in Kubernetes Secrets, covered by K3s encryption at
// rest. These methods have no public read endpoint and never include data in errors.
func (c *Client) PutPlatformSecret(ctx context.Context, name string, kind corev1.SecretType, data map[string][]byte, labels map[string]string) error {
	if err := validPlatformSecretName(name); err != nil {
		return err
	}
	if len(data) == 0 || len(data) > 32 || len(labels) > 16 {
		return fmt.Errorf("platform secret exceeds field bounds")
	}
	size := 0
	for key, value := range data {
		if len(validation.IsConfigMapKey(key)) > 0 {
			return fmt.Errorf("invalid platform secret field")
		}
		size += len(key) + len(value)
	}
	if size > 512<<10 {
		return fmt.Errorf("platform secret exceeds 512 KiB")
	}
	ownedLabels := map[string]string{managedBy: "hakopod", platformSecretLabel: "true"}
	for key, value := range labels {
		if key == managedBy || key == platformSecretLabel || len(validation.IsQualifiedName(key)) > 0 || len(validation.IsValidLabelValue(value)) > 0 {
			return fmt.Errorf("invalid platform secret label")
		}
		ownedLabels[key] = value
	}
	if err := c.platformNamespace(ctx, true); err != nil {
		return err
	}
	existing, err := c.kube.CoreV1().Secrets(PlatformNamespace).Get(ctx, name, metav1.GetOptions{})
	next := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: PlatformNamespace, Labels: ownedLabels}, Type: kind, Data: data}
	next.Annotations = map[string]string{"hakopod.io/updated-at": time.Now().UTC().Format(time.RFC3339)}
	if apierrors.IsNotFound(err) {
		_, err = c.kube.CoreV1().Secrets(PlatformNamespace).Create(ctx, next, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if existing.Labels[managedBy] != "hakopod" || existing.Labels[platformSecretLabel] != "true" {
		return fmt.Errorf("platform secret is not owned by Hakopod")
	}
	next.ResourceVersion = existing.ResourceVersion
	_, err = c.kube.CoreV1().Secrets(PlatformNamespace).Update(ctx, next, metav1.UpdateOptions{})
	return err
}

func (c *Client) GetPlatformSecret(ctx context.Context, name string) (*corev1.Secret, error) {
	if err := validPlatformSecretName(name); err != nil {
		return nil, err
	}
	if err := c.platformNamespace(ctx, false); err != nil {
		return nil, err
	}
	secret, err := c.kube.CoreV1().Secrets(PlatformNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if secret.Labels[managedBy] != "hakopod" || secret.Labels[platformSecretLabel] != "true" {
		return nil, fmt.Errorf("platform secret is not owned by Hakopod")
	}
	return secret, nil
}

func (c *Client) DeletePlatformSecret(ctx context.Context, name string) error {
	secret, err := c.GetPlatformSecret(ctx, name)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return c.kube.CoreV1().Secrets(PlatformNamespace).Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &secret.UID, ResourceVersion: &secret.ResourceVersion}})
}
