package cluster

import (
	"strings"
	"testing"
)

func TestRedisEngineMetricsMissingAndZeroValues(t *testing.T) {
	info := `# Development protocol fixture
connected_clients:0
maxclients:10000
used_memory_dataset:256
total_commands_processed:100
evicted_keys:0
rejected_connections:0
uptime_in_seconds:30
keyspace_hits:3
keyspace_misses:1
`
	m, err := parseRedisEngineMetrics(info)
	if err != nil || *m.Connections != 0 || *m.CacheHitRatio != 0.75 || *m.DataBytes != 256 {
		t.Fatal("valid source statistics lost", err)
	}
	for _, bad := range []string{strings.Replace(info, "connected_clients:0\n", "", 1), strings.Replace(info, "connected_clients:0", "connected_clients:-1", 1), strings.Replace(info, "keyspace_hits:3", "keyspace_hits:invalid", 1)} {
		if _, err = parseRedisEngineMetrics(bad); err == nil {
			t.Fatal("missing or invalid source statistics became a sample")
		}
	}
	zero := strings.Replace(strings.Replace(info, "keyspace_hits:3", "keyspace_hits:0", 1), "keyspace_misses:1", "keyspace_misses:0", 1)
	m, err = parseRedisEngineMetrics(zero)
	if err != nil || m.CacheHitRatio != nil {
		t.Fatal("no requests became a cache hit ratio", err)
	}
}
