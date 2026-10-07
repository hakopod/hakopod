package spec

import (
	"fmt"
	"strconv"
	"strings"
)

// ValidateManagedOptions checks client settings without reading credentials or
// modifying the managed database. Database policy is checked when binding it.
func (b Binding) ValidateManagedOptions() error {
	if len(b.Username) > 64 || strings.ContainsAny(b.Username, "\x00\r\n") || len(b.Database) > 64 || strings.ContainsAny(b.Database, "/\\?#\x00\r\n") {
		return fmt.Errorf("invalid database or username")
	}
	if b.Password != nil && !b.Password.Valid() {
		return fmt.Errorf("password must be a scoped secret reference")
	}
	defaultUser := "app"
	if b.Protocol == "redis" {
		defaultUser = "default"
	}
	if b.Protocol == "oracle" {
		defaultUser = "APP"
	}
	if b.Endpoint == "mysql" {
		defaultUser = "root"
	}
	if b.Endpoint == "postgresql" {
		defaultUser = "postgres"
	}
	if b.Username != "" && b.Username != defaultUser && b.Password == nil {
		return fmt.Errorf("a custom username requires a scoped password reference")
	}
	if b.Protocol == "postgres" && (b.Endpoint == "pooled_read_write" || b.Endpoint == "pooled_read_only") && (b.Username != "" && b.Username != "app" || b.Database != "" && b.Database != "app") {
		return fmt.Errorf("pooled endpoints support only the app login and database; choose a direct endpoint for a custom login or database")
	}
	if b.Protocol == "redis" && b.Database != "" {
		index, err := strconv.Atoi(b.Database)
		if err != nil || index < 0 || index > 15 || strconv.Itoa(index) != b.Database {
			return fmt.Errorf("Redis database must be an index from 0 to 15")
		}
		if b.Endpoint == "cluster" && index != 0 {
			return fmt.Errorf("Redis cluster supports only database 0")
		}
	}
	switch b.SSLMode {
	case "":
		return nil
	case "disable":
		if b.Protocol == "postgres" || b.Protocol == "redis" {
			return nil
		}
	case "require", "verify-ca":
		if b.Protocol == "postgres" {
			return nil
		}
	case "verify-full":
		if b.Protocol != "mysql" {
			return nil
		}
	}
	if b.Protocol == "mysql" {
		return fmt.Errorf("MySQL TLS must be configured in the client driver; omit ssl_mode")
	}
	return fmt.Errorf("ssl_mode is not supported for this database protocol")
}
