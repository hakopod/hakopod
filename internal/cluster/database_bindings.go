package cluster

import (
	"context"
	"fmt"

	"github.com/hakopod/hakopod/internal/spec"
)

// DatabaseConnection contains only one database's credentials, never cluster
// credentials. It remains in memory until copied to an application Secret.
type DatabaseConnection struct {
	URL  string
	Port int32
}
type DatabaseBindingResolver func(context.Context, string, string, spec.Application) (map[string]map[string]DatabaseConnection, error)

// SetDatabaseBindingResolver is called once before starting the workers.
func (c *Client) SetDatabaseBindingResolver(resolve DatabaseBindingResolver) {
	c.options.DatabaseBindings = resolve
}

func (c *Client) snapshotDatabaseBindings(ctx context.Context, target *Target) error {
	needed := false
	for _, svc := range target.Spec.Services {
		for _, binding := range svc.Bindings {
			needed = needed || binding.ManagedDatabase != ""
		}
	}
	if !needed {
		return nil
	}
	if c.options.DatabaseBindings == nil {
		return fmt.Errorf("managed database connections are unavailable")
	}
	connections, err := c.options.DatabaseBindings(ctx, target.Project, target.Environment, target.Spec)
	if err != nil {
		return err
	}
	for name, svc := range target.Spec.Services {
		for key, b := range svc.Bindings {
			if b.ManagedDatabase == "" {
				continue
			}
			connection, ok := connections[name][key]
			if !ok || connection.URL == "" || len(connection.URL) > 8192 || connection.Port != 5432 && connection.Port != 6379 {
				return fmt.Errorf("managed database connection snapshot is incomplete")
			}
		}
	}
	target.databaseConnections = connections
	return nil
}
