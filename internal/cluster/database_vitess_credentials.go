package cluster

import (
	"context"
	"crypto/subtle"
	"fmt"
	"reflect"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Older Vitess credentials had no owner reference. Bind only the immutable
// Secret whose scope and password match the durable operation; never adopt a
// Secret owned by another resource or replace its data.
func (c *Client) reconcileVitessCredentialOwnership(ctx context.Context, d database.Resource, namespace *corev1.Namespace, secret *corev1.Secret, password []byte, before func() error) error {
	if d.Spec.Engine != "vitess" || namespace == nil || namespace.Name != DatabaseNamespace(d.ID) || namespace.UID == "" || namespace.DeletionTimestamp != nil || namespace.Labels[databaseOwner] != d.ID || namespace.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Vitess credential namespace identity changed")
	}
	if secret == nil || secret.Name != "database-credentials" || secret.Namespace != namespace.Name || secret.UID == "" || secret.ResourceVersion == "" || secret.DeletionTimestamp != nil || secret.Type != corev1.SecretTypeBasicAuth || secret.Immutable == nil || !*secret.Immutable || len(secret.Data) != 2 || string(secret.Data["username"]) != "app" || len(password) < 32 || len(password) > 128 || subtle.ConstantTimeCompare(secret.Data["password"], password) != 1 {
		return fmt.Errorf("Vitess credential identity changed")
	}
	for key, value := range databaseLabels(d) {
		if secret.Labels[key] != value {
			return fmt.Errorf("Vitess credential scope changed")
		}
	}
	owners := databaseIdentityMeta(d, namespace.UID, secret.Name).OwnerReferences
	if len(secret.OwnerReferences) > 0 {
		if !reflect.DeepEqual(secret.OwnerReferences, owners) {
			return fmt.Errorf("Vitess credential namespace ownership changed")
		}
	}
	if err := before(); err != nil {
		return err
	}
	current, err := c.kube.CoreV1().Namespaces().Get(ctx, namespace.Name, metav1.GetOptions{})
	if err != nil || current.UID != namespace.UID || current.DeletionTimestamp != nil || current.Labels[databaseOwner] != d.ID || current.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Vitess credential namespace changed before binding")
	}
	if len(secret.OwnerReferences) > 0 {
		return nil
	}
	updated := secret.DeepCopy()
	updated.OwnerReferences = owners
	// Kubernetes checks resourceVersion and UID on this metadata-only update.
	bound, err := c.kube.CoreV1().Secrets(namespace.Name).Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("Vitess credential ownership could not be reconciled")
	}
	if bound.UID != secret.UID || !reflect.DeepEqual(bound.OwnerReferences, owners) || !reflect.DeepEqual(bound.Data, secret.Data) || bound.Immutable == nil || !*bound.Immutable {
		return fmt.Errorf("Vitess credential identity changed during binding")
	}
	return nil
}
