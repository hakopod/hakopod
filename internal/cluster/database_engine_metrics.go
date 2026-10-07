package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

const postgresEngineMetricsQuery = `SELECT json_build_object(
 'connections', (SELECT count(*) FROM pg_stat_activity WHERE datname='app'),
 'active_connections', (SELECT count(*) FROM pg_stat_activity WHERE datname='app' AND state='active'),
 'max_connections', current_setting('max_connections')::bigint,
 'data_bytes', pg_database_size('app'),
 'transactions', xact_commit+xact_rollback,
 'cache_hit_ratio', CASE WHEN blks_hit+blks_read=0 THEN NULL ELSE blks_hit::float8/(blks_hit+blks_read) END,
 'replication_lag_bytes', (SELECT max(pg_wal_lsn_diff(pg_current_wal_lsn(),replay_lsn))::bigint FROM pg_stat_replication),
 'uptime_seconds', extract(epoch FROM clock_timestamp()-pg_postmaster_start_time())::bigint
) FROM pg_stat_database WHERE datname='app'`

// Telemetry is optional: failed collection records a gap, never a healthy zero
// and never changes the outcome of the separate database health checks.
func (c *Client) observeDatabaseEngineMetrics(ctx context.Context, d database.Resource, o *database.Observation) {
	o.EngineMetrics = &database.EngineMetrics{Reason: "Database statistics are unavailable."}
	step, stop := context.WithTimeout(ctx, 4*time.Second)
	defer stop()
	if d.Spec.Engine == "duckdb" {
		if metric, err := c.myduckEngineMetrics(step, d, *o); err == nil {
			o.EngineMetrics = &metric
		}
		return
	}
	if d.Spec.Engine == "oracle" {
		if metric, err := c.oracleEngineMetrics(step, d, *o); err == nil {
			o.EngineMetrics = &metric
		}
		return
	}
	if d.Spec.Engine == "clickhouse" {
		if metric, err := c.clickhouseEngineMetrics(step, d, *o); err == nil {
			o.EngineMetrics = &metric
		}
		return
	}
	if d.Spec.Engine == "mongodb" {
		if metric, err := c.mongodbEngineMetrics(step, d, *o); err == nil {
			o.EngineMetrics = &metric
		}
		return
	}
	if d.Spec.Engine == "mysql" {
		m, err := mysqlObservedPrimary(*o)
		if err != nil {
			return
		}
		out := &databaseBoundedWriter{limit: 8192}
		query := `SELECT JSON_OBJECT(
 'connections', (SELECT COUNT(*) FROM information_schema.processlist WHERE USER='app'),
 'active_connections', (SELECT COUNT(*) FROM information_schema.processlist WHERE USER='app' AND COMMAND<>'Sleep'),
 'max_connections', 100,
 'data_bytes', (SELECT COALESCE(SUM(DATA_LENGTH+INDEX_LENGTH),0) FROM information_schema.tables WHERE TABLE_SCHEMA='app'),
 'commands', (SELECT CAST(VARIABLE_VALUE AS UNSIGNED) FROM performance_schema.global_status WHERE VARIABLE_NAME='Queries'),
 'uptime_seconds', (SELECT CAST(VARIABLE_VALUE AS UNSIGNED) FROM performance_schema.global_status WHERE VARIABLE_NAME='Uptime'))`
		if err = c.DatabaseExec(step, d, m, mysqlLocalCommand(query), nil, out); err != nil {
			return
		}
		var metric database.EngineMetrics
		if json.Unmarshal(out.Bytes(), &metric) != nil || !validDatabaseEngineMetrics(metric) {
			return
		}
		now := time.Now().UTC()
		metric.Available, metric.SampledAt = true, &now
		o.EngineMetrics = &metric
		return
	}
	if d.Spec.Engine == "postgresql" {
		for _, m := range o.Members {
			if m.Name != o.Primary {
				continue
			}
			output := &databaseBoundedWriter{limit: 8192}
			if err := c.DatabaseExec(step, d, m, []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-c", postgresEngineMetricsQuery}, nil, output); err != nil {
				return
			}
			var metric database.EngineMetrics
			if json.Unmarshal(output.Bytes(), &metric) != nil || !validDatabaseEngineMetrics(metric) {
				return
			}
			now := time.Now().UTC()
			metric.Available = true
			metric.SampledAt = &now
			o.EngineMetrics = &metric
			return
		}
	}
	if d.Spec.Engine == "redis" {
		samples := []database.EngineMetrics{}
		for _, m := range o.Members {
			if m.Role != "primary" {
				continue
			}
			info, err := c.redisCommand(step, d, m, "INFO")
			if err != nil {
				return
			}
			metric, err := parseRedisEngineMetrics(info)
			if err != nil {
				return
			}
			samples = append(samples, metric)
		}
		if len(samples) == 0 {
			return
		}
		total := samples[0]
		for _, m := range samples[1:] {
			*total.Connections += *m.Connections
			*total.MaxConnections += *m.MaxConnections
			*total.DataBytes += *m.DataBytes
			*total.Commands += *m.Commands
			*total.EvictedKeys += *m.EvictedKeys
			*total.RejectedConnections += *m.RejectedConnections
			// Counter ratios cannot be averaged without each shard's denominators.
			total.CacheHitRatio = nil
			if *m.UptimeSeconds < *total.UptimeSeconds {
				*total.UptimeSeconds = *m.UptimeSeconds
			}
		}
		now := time.Now().UTC()
		total.Available = true
		total.SampledAt = &now
		o.EngineMetrics = &total
	}
}

func validDatabaseEngineMetrics(m database.EngineMetrics) bool {
	for _, value := range []*int64{m.Connections, m.ActiveConnections, m.MaxConnections, m.DataBytes, m.Transactions, m.Commands, m.ReplicationLagBytes, m.EvictedKeys, m.RejectedConnections, m.UptimeSeconds} {
		if value != nil && *value < 0 {
			return false
		}
	}
	return m.Connections != nil && m.DataBytes != nil && m.MaxConnections != nil && (m.CacheHitRatio == nil || !math.IsNaN(*m.CacheHitRatio) && !math.IsInf(*m.CacheHitRatio, 0) && *m.CacheHitRatio >= 0 && *m.CacheHitRatio <= 1)
}

func parseRedisEngineMetrics(info string) (database.EngineMetrics, error) {
	m := database.EngineMetrics{}
	if len(info) > 128<<10 {
		return m, fmt.Errorf("Redis statistics exceed the limit")
	}
	values := map[string]string{}
	for _, line := range strings.Split(info, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok {
			values[key] = value
		}
	}
	for _, field := range []struct {
		key   string
		value **int64
	}{{"connected_clients", &m.Connections}, {"maxclients", &m.MaxConnections}, {"used_memory_dataset", &m.DataBytes}, {"total_commands_processed", &m.Commands}, {"evicted_keys", &m.EvictedKeys}, {"rejected_connections", &m.RejectedConnections}, {"uptime_in_seconds", &m.UptimeSeconds}} {
		n, err := strconv.ParseInt(values[field.key], 10, 64)
		if err != nil || n < 0 {
			return m, fmt.Errorf("Redis statistics are incomplete")
		}
		*field.value = &n
	}
	hits, err := strconv.ParseUint(values["keyspace_hits"], 10, 64)
	if err != nil {
		return m, fmt.Errorf("Redis cache statistics are incomplete")
	}
	misses, err := strconv.ParseUint(values["keyspace_misses"], 10, 64)
	if err != nil {
		return m, fmt.Errorf("Redis cache statistics are incomplete")
	}
	if hits > 0 || misses > 0 {
		ratio := float64(hits) / (float64(hits) + float64(misses))
		m.CacheHitRatio = &ratio
	}
	return m, nil
}
