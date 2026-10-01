package cluster

import (
	"os"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

func TestManagedClickHouseDistributedQueriesLive(t *testing.T) {
	c, ctx := liveClickHouseClient(t)
	d, health := newClickHouseFixture(t, ctx, c, "cluster", 2, os.Getenv("HAKOPOD_CLICKHOUSE_DISTRIBUTED_ID"))
	for _, engine := range []string{"File", "S3", "URL", "MySQL", "PostgreSQL"} {
		out, err := c.clickhouseQuery(ctx, d, health.Members[0], "app", "CHECK GRANT TABLE ENGINE ON "+engine)
		if err != nil || strings.TrimSpace(out) != "0" {
			t.Fatal("application received an unapproved table engine", engine, err)
		}
	}
	queries := []string{
		"DROP TABLE IF EXISTS app.distributed_fixture SYNC",
		"DROP TABLE IF EXISTS app.local_fixture SYNC",
		"CREATE TABLE app.local_fixture (id UInt64) ORDER BY id",
		"CREATE TABLE app.distributed_fixture AS app.local_fixture ENGINE=Distributed(managed, app, local_fixture, id % 2)",
		"INSERT INTO app.distributed_fixture SETTINGS distributed_foreground_insert=1 VALUES (10), (11)",
		"DROP TABLE IF EXISTS app.forbidden_distributed_fixture SYNC",
		"CREATE TABLE app.forbidden_distributed_fixture (name String) ENGINE=Distributed(managed, system, users)",
	}
	for _, query := range queries {
		if _, err := c.clickhouseQuery(ctx, d, health.Members[0], "app", query); err != nil {
			t.Fatal("application distributed query failed", query, err)
		}
	}
	waitClickHouseData(t, ctx, c, d, health, "SELECT id FROM app.distributed_fixture ORDER BY id FORMAT TSV", "10\n11")
	for _, member := range health.Members {
		want := "10"
		if member.Shard == "1" {
			want = "11"
		}
		part := health
		part.Members = []database.Member{member}
		waitClickHouseData(t, ctx, c, d, part, "SELECT id FROM app.local_fixture ORDER BY id FORMAT TSV", want)
		if _, err := c.clickhouseQuery(ctx, d, member, "app", "SELECT * FROM app.forbidden_distributed_fixture"); err == nil {
			t.Fatal("distributed query bypassed application privileges")
		}
	}
	t.Log("The application created Distributed tables, routed inserts to both shards, read all shards from every member and retained its privilege boundary")
}
