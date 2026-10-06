package cluster

import (
	"context"
	"fmt"
	"net/url"
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
	needed, passwordsNeeded := false, false
	for _, svc := range target.Spec.Services {
		for _, binding := range svc.Bindings {
			needed = needed || binding.ManagedDatabase != "" || binding.ExternalDatabase != ""
			passwordsNeeded = passwordsNeeded || binding.ManagedDatabase != "" && binding.Password != nil
		}
	}
	if !needed {
		return nil
	}
	if c.options.DatabaseBindings == nil {
		return fmt.Errorf("managed database connections are unavailable")
	}
	// Trust renewal also uses this method outside Deploy. Resolve references
	// there before taking a connection snapshot, with the same scope as deploys.
	if passwordsNeeded && target.secretValues == nil {
		if err := c.snapshotWorkloadSecrets(ctx, target); err != nil {
			return err
		}
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
			if b.Password != nil {
				password, exists := target.secretValues[name][spec.BindingSecretKey(key)]
				if !exists || len(password) == 0 || len(password) > 4096 {
					return fmt.Errorf("managed database password snapshot is missing or exceeds 4096 bytes")
				}
				// Parser errors can contain the original URL, so do not return them.
				u, err := url.Parse(connection.URL)
				if err != nil || u.User == nil || u.User.Username() == "" {
					return fmt.Errorf("managed database connection snapshot is invalid")
				}
				u.User = url.UserPassword(u.User.Username(), string(password))
				connection.URL = u.String()
				if len(connection.URL) > 8192 {
					return fmt.Errorf("managed database connection exceeds its size limit")
				}
				connections[name][key] = connection
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
