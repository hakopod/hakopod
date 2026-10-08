package cluster

import (
	"context"
	"fmt"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"time"
)

// MissingWorkloadSecrets reads only the requested application scope. Only a
// confirmed NotFound is missing; backend outages and foreign resources fail closed.
func (c *Client) MissingWorkloadSecrets(ctx context.Context, project, environment string, app spec.Application) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	missing := []string{}
	names := spec.LocalSecretNames(app)
	if len(names) > 100 {
		return nil, fmt.Errorf("at most 100 application secrets")
	}
	if len(names) == 0 {
		return missing, nil
	}
	if err := c.platformNamespace(ctx, false); err != nil {
		if apierrors.IsNotFound(err) {
			return names, nil
		}
		return nil, fmt.Errorf("application secret storage is unavailable")
	}
	// Read this application's bounded set once. Repeated namespace and secret
	// reads can exhaust the shared deadline even when every secret exists.
	scope := secretScope(project, environment, app.Name)
	api := c.kube.CoreV1().Secrets(PlatformNamespace)
	list, err := api.List(ctx, metav1.ListOptions{LabelSelector: "hakopod.io/secret-scope=" + scope, Limit: 101})
	if err != nil {
		return nil, fmt.Errorf("application secret storage is unavailable")
	}
	if list.Continue != "" || len(list.Items) > 100 {
		return nil, fmt.Errorf("application secret limit exceeded")
	}
	byName := make(map[string]*corev1.Secret, len(list.Items))
	for i := range list.Items {
		byName[list.Items[i].Name] = &list.Items[i]
	}
	for _, name := range names {
		key := workloadSecretName(project, environment, app.Name, name)
		secret, found := byName[key]
		var err error
		if !found {
			// An absent label match is not proof of absence. Check the exact
			// name so foreign ownership still fails closed.
			secret, err = api.Get(ctx, key, metav1.GetOptions{})
		}
		if apierrors.IsNotFound(err) {
			missing = append(missing, name)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("application secret storage is unavailable")
		}
		if secret.Labels[managedBy] != "hakopod" || secret.Labels[platformSecretLabel] != "true" || secret.Labels["hakopod.io/secret-scope"] != scope || secret.Labels["hakopod.io/secret-name"] != name {
			return nil, fmt.Errorf("application secret ownership could not be verified")
		}
		if len(secret.Data["value"]) == 0 {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

func (c *Client) ValidateWorkloadSecrets(ctx context.Context, project, environment string, app spec.Application) error {
	missing, err := c.MissingWorkloadSecrets(ctx, project, environment, app)
	if err != nil {
		return err
	}
	if len(missing) != 0 {
		return &spec.MissingSecretsError{Names: missing}
	}
	return nil
}
