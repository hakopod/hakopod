package database

import "time"

// EngineMetrics contains numeric database statistics only. Query text, client
// addresses, usernames and keys are never collected into monitoring history.
type EngineMetrics struct {
	Available           bool       `json:"available"`
	Reason              string     `json:"reason,omitempty"`
	SampledAt           *time.Time `json:"sampled_at,omitempty"`
	Connections         *int64     `json:"connections,omitempty"`
	ActiveConnections   *int64     `json:"active_connections,omitempty"`
	MaxConnections      *int64     `json:"max_connections,omitempty"`
	DataBytes           *int64     `json:"data_bytes,omitempty"`
	Transactions        *int64     `json:"transactions,omitempty"`
	Commands            *int64     `json:"commands,omitempty"`
	CacheHitRatio       *float64   `json:"cache_hit_ratio,omitempty"`
	ReplicationLagBytes *int64     `json:"replication_lag_bytes,omitempty"`
	EvictedKeys         *int64     `json:"evicted_keys,omitempty"`
	RejectedConnections *int64     `json:"rejected_connections,omitempty"`
	UptimeSeconds       *int64     `json:"uptime_seconds,omitempty"`
}

type MetricPoint struct {
	ObservedAt time.Time      `json:"observed_at"`
	Revision   int64          `json:"revision"`
	Status     string         `json:"status"`
	Resources  *Metrics       `json:"resources,omitempty"`
	Engine     *EngineMetrics `json:"engine,omitempty"`
}

type MetricHistory struct {
	Items             []MetricPoint `json:"items"`
	From              time.Time     `json:"from"`
	Until             time.Time     `json:"until"`
	ResolutionSeconds int           `json:"resolution_seconds"`
	RetentionHours    int           `json:"retention_hours"`
}
