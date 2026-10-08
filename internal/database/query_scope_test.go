package database

import "testing"

func TestQueryTransactionScopes(t *testing.T) {
	for _, engine := range []string{"postgresql", "mysql", "vitess", "oracle", "duckdb", "clickhouse"} {
		c := CapabilitiesForQuery(engine)
		want := "none"
		if engine == "postgresql" || engine == "mysql" {
			want = "connection"
		}
		if engine == "vitess" {
			want = "single_shard"
			if c.Supported || c.CrossShardDML == nil || *c.CrossShardDML {
				t.Fatal("Vitess exceeded qualified scope")
			}
		} else if c.CrossShardDML != nil {
			t.Fatal("unsharded engine exposed shard capability")
		}
		if c.TransactionScope != want {
			t.Fatal("transaction scope differs", engine)
		}
	}
}
