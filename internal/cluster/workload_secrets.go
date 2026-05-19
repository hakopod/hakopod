package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sort"
	"time"
)

type WorkloadSecret struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

func secretScope(project, environment, application string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(project+"/"+environment+"/"+application)))[:32]
}
func workloadSecretName(project, environment, application, name string) string {
	return "env-" + secretScope(project, environment, application) + "-" + name
}

func (c *Client) ListWorkloadSecrets(ctx context.Context, project, environment, application string) ([]WorkloadSecret, error) {
	result := []WorkloadSecret{}
	if err := c.platformNamespace(ctx, false); err != nil {
		if apierrors.IsNotFound(err) {
			return result, nil
		}
		return nil, err
	}
	list, err := c.kube.CoreV1().Secrets(PlatformNamespace).List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod,hakopod.io/secret-scope=" + secretScope(project, environment, application), Limit: 101})
	if err != nil {
		return nil, err
	}
	if list.Continue != "" || len(list.Items) > 100 {
		return nil, fmt.Errorf("application secret limit exceeded")
	}
	for _, s := range list.Items {
		stamp, _ := time.Parse(time.RFC3339, s.Annotations["hakopod.io/updated-at"])
		if stamp.IsZero() {
			stamp = s.CreationTimestamp.Time
		}
		result = append(result, WorkloadSecret{Name: s.Labels["hakopod.io/secret-name"], UpdatedAt: stamp})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
func (c *Client) PutWorkloadSecret(ctx context.Context, project, environment, application, name, value string) error {
	items, err := c.ListWorkloadSecrets(ctx, project, environment, application)
	if err != nil {
		return err
	}
	exists := false
	for _, i := range items {
		exists = exists || i.Name == name
	}
	if !exists && len(items) >= 100 {
		return fmt.Errorf("at most 100 application secrets")
	}
	return c.PutPlatformSecret(ctx, workloadSecretName(project, environment, application, name), corev1.SecretTypeOpaque, map[string][]byte{"value": []byte(value)}, map[string]string{"hakopod.io/secret-scope": secretScope(project, environment, application), "hakopod.io/secret-name": name})
}
func (c *Client) DeleteWorkloadSecret(ctx context.Context, project, environment, application, name string) error {
	return c.DeletePlatformSecret(ctx, workloadSecretName(project, environment, application, name))
}

func (c *Client) prepareWorkloadSecrets(ctx context.Context, t Target, name string, s spec.Service) error {
	api := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID))
	if len(s.Secrets) == 0 {
		return nil
	}
	data := map[string][]byte{}
	total := 0
	for key, reference := range s.Secrets {
		secret, err := c.GetPlatformSecret(ctx, workloadSecretName(t.Project, t.Environment, t.Spec.Name, reference.Ref))
		if err != nil {
			return fmt.Errorf("secret reference %s is unavailable for this application", reference.Ref)
		}
		data[key] = secret.Data["value"]
		total += len(key) + len(data[key])
		if total > 512<<10 {
			return fmt.Errorf("service secret values exceed 512 KiB")
		}
	}
	wanted := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-environment", Namespace: Namespace(t.ApplicationID), Labels: labelsFor(t, name)}, Type: corev1.SecretTypeOpaque, Data: data}
	current, err := api.Get(ctx, wanted.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		_, err = api.Create(ctx, wanted, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if err = owned(current, t); err != nil {
		return err
	}
	wanted.ResourceVersion = current.ResourceVersion
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	_, err = api.Update(ctx, wanted, metav1.UpdateOptions{})
	return err
}
