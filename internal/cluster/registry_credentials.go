package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const registryScopeLabel = "hakopod.io/registry-credential"
const registryRevisionLabel = "hakopod.io/registry-revision"

type RegistryCredential struct {
	Registry, Username, Password, TokenRealm, Repository string
	SourceName                                           string
	Revision                                             int64
}

func NormalizeRegistry(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "docker.io" || value == "index.docker.io" {
		value = "registry-1.docker.io"
	}
	if len(value) > 253 || strings.ContainsAny(value, "/@?#\\ \t\r\n") {
		return "", fmt.Errorf("registry must be an exact HTTPS host, optionally with a port; no URL or path")
	}
	u, err := url.Parse("https://" + value)
	if err != nil || u.Host != value || u.User != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid registry host")
	}
	if net.ParseIP(u.Hostname()) != nil || len(validation.IsDNS1123Subdomain(u.Hostname())) > 0 {
		return "", fmt.Errorf("registry must use a DNS hostname with publicly routable addresses")
	}
	return value, nil
}
func ValidateRegistryCredential(credential RegistryCredential) error {
	host, err := NormalizeRegistry(credential.Registry)
	if err != nil || host != credential.Registry {
		return fmt.Errorf("invalid canonical registry host")
	}
	if len(credential.Username) < 1 || len(credential.Username) > 256 || strings.ContainsAny(credential.Username, ":\x00\r\n") || len(credential.Password) < 1 || len(credential.Password) > 16<<10 || strings.ContainsAny(credential.Password, "\x00\r\n") {
		return fmt.Errorf("registry username/password are missing or exceed supported bounds")
	}
	if credential.TokenRealm != "" {
		u, err := url.Parse(credential.TokenRealm)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(credential.TokenRealm) > 2048 {
			return fmt.Errorf("token_realm must be an exact HTTPS URL without credentials, query or fragment")
		}
		if _, err = NormalizeRegistry(u.Host); err != nil {
			return err
		}
	}
	return nil
}
func (c RegistryCredential) trustsRealm(realm *url.URL) bool {
	if realm.Fragment != "" || realm.RawQuery != "" {
		return false
	}
	if c.TokenRealm != "" {
		return realm.String() == c.TokenRealm
	}
	return realm.Host == c.Registry || (c.Registry == "registry-1.docker.io" && realm.String() == "https://auth.docker.io/token")
}
func RegistryScope(project, environment, name string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(project+"\x00"+environment+"\x00"+name)))[:32]
}
func RegistrySecretData(c RegistryCredential) map[string][]byte {
	value := map[string]any{"auths": map[string]any{c.Registry: map[string]string{"username": c.Username, "password": c.Password, "auth": base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + c.Password))}}}
	data, _ := json.Marshal(value)
	return map[string][]byte{corev1.DockerConfigJsonKey: data, "registry": []byte(c.Registry), "token_realm": []byte(c.TokenRealm), "revision": []byte(strconv.FormatInt(c.Revision, 10))}
}
func (c *Client) RegistryCredential(ctx context.Context, project, environment, name, image string) (*RegistryCredential, error) {
	if project == "" || environment == "" || name == "" || c.options.RegistrySecretName == nil {
		return nil, fmt.Errorf("scoped registry credential store is not configured")
	}
	secretName, err := c.options.RegistrySecretName(ctx, project, environment, name)
	if err != nil {
		return nil, fmt.Errorf("registry credential not found in this scope")
	}
	secret, err := c.GetPlatformSecret(ctx, secretName)
	if err != nil {
		return nil, fmt.Errorf("registry credential storage unavailable")
	}
	if secret.Type != corev1.SecretTypeDockerConfigJson || secret.Labels[registryScopeLabel] != RegistryScope(project, environment, name) {
		return nil, fmt.Errorf("registry credential scope mismatch")
	}
	value := RegistryCredential{Registry: string(secret.Data["registry"]), TokenRealm: string(secret.Data["token_realm"]), SourceName: secretName}
	value.Revision, _ = strconv.ParseInt(string(secret.Data["revision"]), 10, 64)
	var config struct {
		Auths map[string]struct {
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"auths"`
	}
	if json.Unmarshal(secret.Data[corev1.DockerConfigJsonKey], &config) != nil || len(config.Auths) != 1 {
		return nil, fmt.Errorf("registry credential encoding invalid")
	}
	auth, ok := config.Auths[value.Registry]
	if !ok {
		return nil, fmt.Errorf("registry credential host mismatch")
	}
	value.Username = auth.Username
	value.Password = auth.Password
	if err := ValidateRegistryCredential(value); err != nil {
		return nil, err
	}
	ref, err := parseReference(image)
	if image != "" && (err != nil || ref.registry != value.Registry) {
		return nil, fmt.Errorf("registry credential does not match image host")
	}
	return &value, nil
}
func (c *Client) prepareRegistryCredential(ctx context.Context, t Target, name string, svc spec.Service, wanted *appsv1.Deployment) error {
	if svc.RegistryCredential == "" {
		return nil
	}
	credential, err := c.RegistryCredential(ctx, t.Project, t.Environment, svc.RegistryCredential, svc.Image)
	if err != nil {
		return err
	}
	scope := RegistryScope(t.Project, t.Environment, svc.RegistryCredential)
	secretName := "hp-registry-" + scope[:20]
	api := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID))
	labels := labelsFor(t, "")
	labels[registryScopeLabel] = scope
	labels[registryRevisionLabel] = strconv.FormatInt(credential.Revision, 10)
	data := RegistrySecretData(*credential)
	delete(data, "registry")
	delete(data, "token_realm")
	delete(data, "revision")
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: Namespace(t.ApplicationID), Labels: labels}, Type: corev1.SecretTypeDockerConfigJson, Data: data}
	old, err := api.Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, secret, metav1.CreateOptions{})
	} else if err == nil {
		if err = owned(old, t); err != nil {
			return err
		}
		oldRevision, _ := strconv.ParseInt(old.Labels[registryRevisionLabel], 10, 64)
		if oldRevision > credential.Revision {
			secret = old
		} else {
			secret.ResourceVersion = old.ResourceVersion
			_, err = api.Update(ctx, secret, metav1.UpdateOptions{})
		}
	}
	if err != nil {
		return err
	}
	wanted.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: secretName}}
	wanted.Labels = maps.Clone(wanted.Labels)
	wanted.Labels[registryScopeLabel] = scope
	wanted.Spec.Template.Labels[registryScopeLabel] = scope
	return nil
}

// RefreshRegistryCopies rotates existing namespace copies without creating new
// workload access. A failed or bounded partial refresh is returned to the caller.
func (c *Client) RefreshRegistryCopies(ctx context.Context, project, environment, name string, remove bool) (int, error) {
	scope := RegistryScope(project, environment, name)
	items, err := c.kube.CoreV1().Secrets("").List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + registryScopeLabel + "=" + scope, Limit: 200})
	if err != nil {
		return 0, err
	}
	if items.Continue != "" {
		return 0, fmt.Errorf("credential has more than 200 copies; refresh requires operator assistance")
	}
	var credential *RegistryCredential
	if !remove {
		credential, err = c.RegistryCredential(ctx, project, environment, name, "")
		if err != nil {
			return 0, err
		}
	}
	updated := 0
	for _, secret := range items.Items {
		if secret.Namespace == PlatformNamespace {
			if !remove && secret.Name != credential.SourceName {
				revision, _ := strconv.ParseInt(string(secret.Data["revision"]), 10, 64)
				if revision > 0 && revision < credential.Revision {
					if err = c.DeletePlatformSecret(ctx, secret.Name); err != nil {
						return updated, err
					}
				}
			}
			continue
		}
		if secret.Name != "hp-registry-"+scope[:20] || secret.Labels[ownerKey] == "" || secret.Namespace != "hp-"+secret.Labels[ownerKey] || secret.Type != corev1.SecretTypeDockerConfigJson {
			return updated, fmt.Errorf("credential copy ownership mismatch")
		}
		ns, err := c.kube.CoreV1().Namespaces().Get(ctx, secret.Namespace, metav1.GetOptions{})
		if err != nil {
			return updated, err
		}
		if ns.Labels[managedBy] != "hakopod" || ns.Labels[ownerKey] != secret.Labels[ownerKey] {
			return updated, fmt.Errorf("credential namespace ownership mismatch")
		}
		api := c.kube.CoreV1().Secrets(secret.Namespace)
		if remove {
			err = api.Delete(ctx, secret.Name, deleteOptions(&secret))
		} else {
			oldRevision, _ := strconv.ParseInt(secret.Labels[registryRevisionLabel], 10, 64)
			data := RegistrySecretData(*credential)[corev1.DockerConfigJsonKey]
			if oldRevision > credential.Revision || (oldRevision == credential.Revision && bytes.Equal(secret.Data[corev1.DockerConfigJsonKey], data)) {
				continue
			}
			secret.Data = map[string][]byte{corev1.DockerConfigJsonKey: data}
			secret.Labels[registryRevisionLabel] = strconv.FormatInt(credential.Revision, 10)
			_, err = api.Update(ctx, &secret, metav1.UpdateOptions{})
		}
		if err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// Deletion is refused while an owned workload still references the credential.
func (c *Client) RegistryInUse(ctx context.Context, project, environment, name string) (bool, error) {
	selector := managedBy + "=hakopod," + registryScopeLabel + "=" + RegistryScope(project, environment, name)
	deployments, err := c.kube.AppsV1().Deployments("").List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 1})
	if err != nil {
		return false, err
	}
	if len(deployments.Items) > 0 {
		return true, nil
	}
	pods, err := c.kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: selector, FieldSelector: "status.phase!=Succeeded,status.phase!=Failed", Limit: 1})
	return err == nil && len(pods.Items) > 0, err
}
