package cluster

import (
	"bytes"
	"context"
	"fmt"
	"strconv"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Registry copies contain only Docker's authentication document. The source
// reference is resolved through the database's project and environment on every
// reconciliation; the application never receives this Secret.
func (c *Client) prepareOracleEnterpriseRegistry(ctx context.Context, d database.Resource, before func() error) (string, error) {
	if !oracleEnterprise(d.Spec) || d.Spec.Oracle.RegistryCredential == "" {
		return "", nil
	}
	credential, err := c.RegistryCredential(ctx, d.Project, d.Environment, d.Spec.Oracle.RegistryCredential, d.Spec.Oracle.Image)
	if err != nil {
		return "", err
	}
	if credential.Revision < 1 {
		return "", fmt.Errorf("Oracle registry credential has no accepted revision")
	}
	ns, err := c.oracleEnterpriseNamespace(ctx, d)
	if err != nil {
		return "", err
	}
	scope := RegistryScope(d.Project, d.Environment, d.Spec.Oracle.RegistryCredential)
	name := "hp-registry-" + scope[:20]
	meta := databaseIdentityMeta(d, ns.UID, name)
	meta.Labels[registryScopeLabel] = scope
	meta.Labels[registryRevisionLabel] = strconv.FormatInt(credential.Revision, 10)
	data := RegistrySecretData(*credential)[corev1.DockerConfigJsonKey]
	desired := &corev1.Secret{ObjectMeta: meta, Type: corev1.SecretTypeDockerConfigJson, Data: map[string][]byte{corev1.DockerConfigJsonKey: data}}
	api := c.kube.CoreV1().Secrets(ns.Name)
	current, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return "", err
		}
		_, err = api.Create(ctx, desired, metav1.CreateOptions{})
		return name, err
	}
	if err != nil {
		return "", fmt.Errorf("Oracle registry credential copy is unavailable")
	}
	if err = databaseIdentityOwned(current, d, ns.UID); err != nil || current.Type != desired.Type || current.Labels[registryScopeLabel] != scope || len(current.Data) != 1 {
		return "", fmt.Errorf("Oracle registry credential copy ownership changed")
	}
	revision, err := strconv.ParseInt(current.Labels[registryRevisionLabel], 10, 64)
	if err != nil || revision < 1 || revision > credential.Revision {
		return "", fmt.Errorf("Oracle registry credential revision changed")
	}
	if revision == credential.Revision {
		if !bytes.Equal(current.Data[corev1.DockerConfigJsonKey], data) {
			return "", fmt.Errorf("Oracle registry credential revision has different contents")
		}
		return name, nil
	}
	current.Data = desired.Data
	current.Labels[registryRevisionLabel] = desired.Labels[registryRevisionLabel]
	if err = before(); err != nil {
		return "", err
	}
	_, err = api.Update(ctx, current, metav1.UpdateOptions{})
	return name, err
}

func (c *Client) databaseRegistryCopyOwned(ctx context.Context, secret *corev1.Secret, project, environment, scope string) error {
	if secret.Labels[databaseOwner] == "" || secret.Namespace != DatabaseNamespace(secret.Labels[databaseOwner]) || secret.Name != "hp-registry-"+scope[:20] || secret.Type != corev1.SecretTypeDockerConfigJson || secret.Labels[registryScopeLabel] != scope || secret.Labels["hakopod.io/project"] != project || secret.Labels["hakopod.io/environment"] != environment {
		return fmt.Errorf("database registry credential copy scope changed")
	}
	d := database.Resource{ID: secret.Labels[databaseOwner], Project: project, Environment: environment}
	ns, err := c.oracleEnterpriseNamespace(ctx, d)
	if err != nil {
		return err
	}
	return databaseIdentityOwned(secret, d, ns.UID)
}

// An accepted database owns its credential copy before a controller pod exists.
// Refuse deletion throughout provisioning, replacement and recovery, as well as
// while members are running. Database deletion removes this copy with its namespace.
func (c *Client) databaseRegistryInUse(ctx context.Context, project, environment, scope string) (bool, error) {
	items, err := c.kube.CoreV1().Secrets("").List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + registryScopeLabel + "=" + scope + "," + databaseOwner, Limit: 65})
	if err != nil {
		return false, fmt.Errorf("database registry reference inventory is unavailable")
	}
	if items.Continue != "" || len(items.Items) > 64 {
		return false, fmt.Errorf("database registry reference inventory is unavailable or exceeds its bound")
	}
	for i := range items.Items {
		if err = c.databaseRegistryCopyOwned(ctx, &items.Items[i], project, environment, scope); err != nil {
			return false, err
		}
	}
	return len(items.Items) > 0, nil
}
