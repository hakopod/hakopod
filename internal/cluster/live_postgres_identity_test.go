package cluster

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

func TestManagedPostgresNamedRecoveryLive(t *testing.T) {
	c, ctx := liveRecoveryClient(t, 20*time.Minute)
	t.Setenv("HAKOPOD_DATABASE_TLS_TEST", "1")
	source, health := newRecoveryFixture(t, ctx, c, "postgresql", "17")
	query := func(d database.Resource, o database.Observation, sql string) string {
		t.Helper()
		out := &databaseBoundedWriter{limit: 4096}
		for _, m := range o.Members {
			if m.Name == o.Primary {
				if err := c.DatabaseExec(ctx, d, m, []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", d.Spec.LogicalDatabase(), "-c", sql}, nil, out); err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(out.String())
			}
		}
		t.Fatal("fixture primary is unavailable")
		return ""
	}
	query(source, health, "CREATE TABLE identity_probe(id bigint PRIMARY KEY, value text); INSERT INTO identity_probe VALUES (1, 'captured');")
	archive := &databaseBoundedWriter{limit: 8 << 20}
	if err := c.DumpDatabase(ctx, source, health, archive); err != nil {
		t.Fatal(err)
	}
	target, targetHealth := newRecoveryFixtureConfigured(t, ctx, c, "postgresql", "17", func(s *database.Spec) {
		s.Postgres = &database.PostgresConfig{Database: "feesbook", Username: "superuserfox"}
	})
	target.Status, target.Recovery = "restoring", &database.Recovery{JobID: source.ID}
	if err := c.RestorePostgresDatabase(ctx, target, targetHealth, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	if got := query(target, targetHealth, "SELECT value FROM identity_probe WHERE id=1"); got != "captured" {
		t.Fatalf("recovered data: %q", got)
	}
	if got := query(target, targetHealth, "SELECT pg_get_userbyid(relowner) FROM pg_class WHERE relname='identity_probe'"); got != "superuserfox" {
		t.Fatalf("restored owner: %q", got)
	}
	result, err := c.QueryDatabase(ctx, target, database.QueryRequest{SQL: "SELECT current_user, current_database(), value FROM identity_probe"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || len(result.Rows[0]) != 3 || result.Rows[0][0] == nil || *result.Rows[0][0] != "superuserfox" || result.Rows[0][1] == nil || *result.Rows[0][1] != "feesbook" {
		t.Fatalf("query used the wrong identity: %+v", result)
	}
	if err := c.RestorePostgresDatabase(ctx, target, targetHealth, bytes.NewReader(archive.Bytes())); err == nil {
		t.Fatal("restore accepted a nonempty named target")
	}
	namedArchive := &databaseBoundedWriter{limit: 8 << 20}
	if err := c.DumpDatabase(ctx, target, targetHealth, namedArchive); err != nil {
		t.Fatal(err)
	}
	if len(namedArchive.Bytes()) == 0 {
		t.Fatal("named database backup is empty")
	}
	if got := query(source, health, "SELECT count(*) FROM identity_probe"); got != "1" {
		t.Fatal("migration changed the source")
	}
}
