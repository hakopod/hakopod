package cluster

import (
	"context"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
)

// DatabaseConnection contains only one database's credentials, never cluster
// credentials. It remains in memory until copied to an application Secret.
type DatabaseConnection struct {
	// ExternalIPs come only from a fresh provider TLS/query verification. They
	// are not accepted from application TOML or public deployment requests.
	ExternalIPs []string
	URL         string
	Port        int32
	// Public CA material only; no database or cluster private key is copied.
	CA string
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
			needed = needed || binding.ManagedDatabase != "" || binding.ExternalDatabase != ""
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
			if b.ExternalDatabase != "" {
				connection, ok := connections[name][key]
				if !ok {
					return fmt.Errorf("external database connection snapshot is incomplete")
				}
				if err := validateExternalDatabaseSnapshot(*target, b, connection); err != nil {
					return err
				}
				continue
			}
			if b.ManagedDatabase == "" {
				continue
			}
			connection, ok := connections[name][key]
			if !ok || connection.URL == "" || len(connection.URL) > 8192 || connection.Port != 5432 && connection.Port != 6379 && connection.Port != 3306 && connection.Port != 6446 && connection.Port != 6447 && connection.Port != 27017 && connection.Port != 9440 && connection.Port != 2484 {
				return fmt.Errorf("managed database connection snapshot is incomplete")
			}
			if connection.CA != "" {
				if _, _, err := database.ParsePublicTrust([]byte(connection.CA), time.Now()); err != nil {
					return err
				}
			}
		}
	}
	target.databaseConnections = connections
	return nil
}
