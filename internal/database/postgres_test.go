package database

import (
	"strings"
	"testing"
	"time"
)

func TestPostgresIdentityValidation(t *testing.T) {
	s := Spec{SchemaVersion: 1, Name: "feesbook", Engine: "postgresql", Version: "17", Mode: "standalone", Shards: 1, CPU: "250m", Memory: "512Mi", StorageGiB: 2}
	if s.CredentialUsername() != "app" || s.LogicalDatabase() != "app" {
		t.Fatal("existing database defaults changed")
	}
	s.Postgres = &PostgresConfig{Database: "feesbook", Username: "superuserfox"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "postgres", "template0", "template1", "pg_read_server_files", "bad-name", "UPPER", "x';DROP DATABASE x;--", strings.Repeat("a", 64)} {
		for _, field := range []string{"database", "username"} {
			bad := s
			identity := *s.Postgres
			bad.Postgres = &identity
			if field == "database" {
				identity.Database = value
			} else {
				identity.Username = value
			}
			if bad.Validate() == nil {
				t.Fatalf("accepted invalid %s %q", field, value)
			}
		}
	}
	wrongEngine := s
	wrongEngine.Engine = "mysql"
	if wrongEngine.Validate() == nil {
		t.Fatal("accepted PostgreSQL identity for MySQL")
	}
}

func TestPostgresIdentityCannotChangeDuringResize(t *testing.T) {
	s := Spec{SchemaVersion: 1, Name: "feesbook", Engine: "postgresql", Version: "17", Mode: "standalone", Shards: 1, CPU: "250m", Memory: "512Mi", StorageGiB: 2, Postgres: &PostgresConfig{Database: "feesbook", Username: "superuserfox"}}
	for _, identity := range []*PostgresConfig{nil, {Database: "other", Username: "superuserfox"}, {Database: "feesbook", Username: "other"}} {
		next := s
		next.Postgres = identity
		_, err := PlanResize(Resource{Spec: s}, next, nil, time.Now())
		if err == nil || !strings.Contains(err.Error(), "database name and login are immutable") {
			t.Fatalf("identity change not rejected: %v", err)
		}
	}
}
