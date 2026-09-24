package cluster

import (
	"context"
	"fmt"
	"github.com/hakopod/hakopod/internal/spec"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	for _, name := range names {
		secret, err := c.GetPlatformSecret(ctx, workloadSecretName(project, environment, app.Name, name))
		if apierrors.IsNotFound(err) {
			missing = append(missing, name)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("application secret storage is unavailable")
		}
		if secret.Labels["hakopod.io/secret-scope"] != secretScope(project, environment, app.Name) || secret.Labels["hakopod.io/secret-name"] != name {
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
