package cluster

import (
	"context"
	"fmt"

	"github.com/hakopod/hakopod/internal/spec"
)

// SetExternalSecretResolver is startup-only wiring, before workers are started.
func (c *Client) SetExternalSecretResolver(resolve func(context.Context, string, string, spec.Application) (map[string]map[string][]byte, error)) {
	c.options.ExternalSecrets = resolve
}

// Read every source before any deployment write. A failed provider or missing
// native reference cannot replace one service's last-good secret while another
// service's source is unavailable. Kubernetes updates themselves remain separate
// API operations, as with every multi-resource deployment.
func (c *Client) snapshotWorkloadSecrets(ctx context.Context, target *Target) error {
	hasExternal := false
	for _, service := range target.Spec.Services {
		for _, ref := range spec.SecretReferences(service) {
			hasExternal = hasExternal || ref.Provider != ""
		}
	}
	values := map[string]map[string][]byte{}
	if hasExternal {
		if c.options.ExternalSecrets == nil {
			return fmt.Errorf("external secret providers are unavailable")
		}
		var err error
		values, err = c.options.ExternalSecrets(ctx, target.Project, target.Environment, target.Spec)
		if err != nil {
			return fmt.Errorf("external secret snapshot is unavailable; existing workload secrets remain unchanged")
		}
		if values == nil {
			return fmt.Errorf("external secret snapshot is incomplete")
		}
	}
	for _, name := range spec.Names(target.Spec) {
		service := target.Spec.Services[name]
		if values[name] == nil {
			values[name] = map[string][]byte{}
		}
		serviceBytes := 0
		for key, ref := range spec.SecretReferences(service) {
			if ref.Provider == "" {
				secret, err := c.GetPlatformSecret(ctx, workloadSecretName(target.Project, target.Environment, target.Spec.Name, ref.Ref))
				if err != nil {
					return fmt.Errorf("secret reference %s is unavailable for this application", ref.Ref)
				}
				value, ok := secret.Data["value"]
				if !ok {
					return fmt.Errorf("native secret value is unavailable")
				}
				values[name][key] = value
			}
			value, exists := values[name][key]
			if !exists || len(value) > 64<<10 {
				return fmt.Errorf("secret snapshot is incomplete or exceeds value bounds")
			}
			serviceBytes += len(key) + len(value)
		}
		if serviceBytes > 512<<10 {
			return fmt.Errorf("secret snapshot exceeds size bounds")
		}
	}
	target.secretValues = values
	return nil
}
