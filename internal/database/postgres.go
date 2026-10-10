package database

import (
	"fmt"
	"regexp"
	"strings"
)

// PostgresConfig selects the owner and logical database when a cluster is created.
// Passwords remain generated credentials and never belong in a database spec.
type PostgresConfig struct {
	Database string `json:"database" toml:"database"`
	Username string `json:"username" toml:"username"`
}

var postgresIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func (s Spec) validatePostgres() error {
	if s.Postgres == nil {
		return nil
	}
	if s.Engine != "postgresql" {
		return fmt.Errorf("postgres configuration belongs only to PostgreSQL databases")
	}
	for _, name := range []string{s.Postgres.Database, s.Postgres.Username} {
		if !postgresIdentifier.MatchString(name) || strings.HasPrefix(name, "pg_") || name == "postgres" || name == "template0" || name == "template1" {
			return fmt.Errorf("PostgreSQL database and username must be non-reserved names of 1–63 lowercase letters, digits or underscores, starting with a letter")
		}
	}
	return nil
}

func (s Spec) CredentialUsername() string {
	if s.Engine == "postgresql" && s.Postgres != nil {
		return s.Postgres.Username
	}
	return "app"
}

func (s Spec) LogicalDatabase() string {
	if s.Engine == "postgresql" && s.Postgres != nil {
		return s.Postgres.Database
	}
	return "app"
}
