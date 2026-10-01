package cluster

import (
	"encoding/json"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

func TestClickHouseHealthRejectsIncompleteAndFailedReplication(t *testing.T) {
	s := clickhouseFixture().Spec
	s.Mode = "cluster"
	s.Shards = 2
	s.Replicas = 1
	good := map[string]any{"database_engine": "Replicated", "shard": "1", "replica": "0", "unhealthy_tables": 0, "failed_replication": 0, "active_database_replicas": 1}
	encode := func(v map[string]any) string { raw, _ := json.Marshal(v); return string(raw) }
	if shard, err := parseClickHouseHealth(encode(good), s); err != nil || shard != "1" {
		t.Fatal("valid native shard rejected", err)
	}
	for key, value := range map[string]any{"database_engine": "Atomic", "shard": "2", "replica": "", "unhealthy_tables": 1, "failed_replication": 1, "active_database_replicas": 0} {
		copy := map[string]any{}
		for k, v := range good {
			copy[k] = v
		}
		copy[key] = value
		if _, err := parseClickHouseHealth(encode(copy), s); err == nil {
			t.Fatal("unsafe health accepted", key)
		}
		delete(copy, key)
		if _, err := parseClickHouseHealth(encode(copy), s); err == nil {
			t.Fatal("incomplete health accepted", key)
		}
	}
}

func TestClickHouseMetricsCountDataOncePerShard(t *testing.T) {
	members := []database.Member{{Shard: "0"}, {Shard: "0"}, {Shard: "1"}, {Shard: "1"}}
	samples := []database.EngineMetrics{}
	for i := range members {
		samples = append(samples, database.EngineMetrics{Connections: ptr(int64(2)), ActiveConnections: ptr(int64(1)), MaxConnections: ptr(int64(200)), Commands: ptr(int64(10)), UptimeSeconds: ptr(int64(100 - i)), DataBytes: ptr(int64(100))})
	}
	total, err := aggregateClickHouseEngineMetrics(members, samples)
	if err != nil || !total.Available || *total.DataBytes != 200 || *total.Connections != 8 || *total.Commands != 40 || *total.UptimeSeconds != 97 {
		t.Fatal("incorrect ClickHouse aggregate", err)
	}
	samples[1].Commands = nil
	if _, err := aggregateClickHouseEngineMetrics(members, samples); err == nil {
		t.Fatal("missing counter was represented as zero")
	}
}
