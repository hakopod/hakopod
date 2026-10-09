package cluster

import (
	"context"
	"fmt"
	"github.com/hakopod/hakopod/internal/spec"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sync"
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
	// Verify this shared namespace once. Each worker still reads only an exact
	// secret name and checks both platform ownership and application ownership.
	if err := c.platformNamespace(ctx, false); err != nil {
		return nil, fmt.Errorf("application secret storage is unavailable")
	}
	const workers = 4
	jobs := make(chan int, len(names))
	for index := range names {
		jobs <- index
	}
	close(jobs)
	absent := make([]bool, len(names))
	var firstErr error
	var once sync.Once
	var group sync.WaitGroup
	fail := func(err error) {
		once.Do(func() {
			firstErr = err
			cancel()
		})
	}
	for worker := 0; worker < min(workers, len(names)); worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					fail(fmt.Errorf("application secret storage is unavailable"))
					return
				}
				name := names[index]
				secret, err := c.getPlatformSecretInVerifiedNamespace(ctx, workloadSecretName(project, environment, app.Name, name))
				if apierrors.IsNotFound(err) {
					absent[index] = true
					continue
				}
				if err != nil {
					fail(fmt.Errorf("application secret storage is unavailable"))
					return
				}
				if secret.Labels["hakopod.io/secret-scope"] != secretScope(project, environment, app.Name) || secret.Labels["hakopod.io/secret-name"] != name {
					fail(fmt.Errorf("application secret ownership could not be verified"))
					return
				}
				absent[index] = len(secret.Data["value"]) == 0
			}
		}()
	}
	group.Wait()
	if firstErr != nil || ctx.Err() != nil {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, fmt.Errorf("application secret storage is unavailable")
	}
	for index, name := range names {
		if absent[index] {
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
