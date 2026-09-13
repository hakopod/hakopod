package cluster

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Template validation reads only explicitly referenced, owned application secrets.
// Values have no HTTP read endpoint and never enter deployment specifications.
func (c *Client) ReadWorkloadSecrets(ctx context.Context, project, environment, application string, refs []string) (map[string]string, error) {
	if len(refs) > 32 {
		return nil, fmt.Errorf("too many template secret references")
	}
	values := make(map[string]string, len(refs))
	total := 0
	for _, ref := range refs {
		secret, err := c.GetPlatformSecret(ctx, workloadSecretName(project, environment, application, ref))
		if err != nil {
			return nil, fmt.Errorf("secret reference %s is unavailable for this application", ref)
		}
		if secret.Labels["hakopod.io/secret-scope"] != secretScope(project, environment, application) || secret.Labels["hakopod.io/secret-name"] != ref {
			return nil, fmt.Errorf("secret reference %s does not belong to this application", ref)
		}
		value := secret.Data["value"]
		total += len(value)
		if len(value) > 64<<10 || total > 512<<10 {
			return nil, fmt.Errorf("template secret values exceed size limits")
		}
		values[ref] = string(value)
	}
	return values, nil
}

// Creation is atomic: retrying generation cannot silently replace an existing key.
func (c *Client) CreateWorkloadSecret(ctx context.Context, project, environment, application, name, value string) error {
	if len(value) == 0 || len(value) > 64<<10 {
		return fmt.Errorf("secret value exceeds size limits")
	}
	if err := validPlatformSecretName(workloadSecretName(project, environment, application, name)); err != nil {
		return err
	}
	if err := c.platformNamespace(ctx, true); err != nil {
		return err
	}
	items, err := c.ListWorkloadSecrets(ctx, project, environment, application)
	if err != nil {
		return err
	}
	if len(items) >= 100 {
		return fmt.Errorf("at most 100 application secrets")
	}
	_, err = c.kube.CoreV1().Secrets(PlatformNamespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: workloadSecretName(project, environment, application, name), Namespace: PlatformNamespace,
			Labels:      map[string]string{managedBy: "hakopod", platformSecretLabel: "true", "hakopod.io/secret-scope": secretScope(project, environment, application), "hakopod.io/secret-name": name},
			Annotations: map[string]string{"hakopod.io/updated-at": time.Now().UTC().Format(time.RFC3339)},
		}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"value": []byte(value)},
	}, metav1.CreateOptions{})
	return err
}
