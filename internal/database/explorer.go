package database

import (
	"strings"
	"time"
)

// ExplorerConnection describes a source, not a running explorer session. It never
// contains an endpoint or credentials. Those belong on the private sync channel.
type ExplorerConnection struct {
	DatabaseID string `json:"database_id"`
	Revision   int64  `json:"revision"`
	Name       string `json:"name"`
	Engine     string `json:"engine"`
	State      string `json:"state"`
	Reason     string `json:"reason"`
	CanWrite   bool   `json:"can_write"`
}

type ExplorerCatalog struct {
	SchemaVersion int                  `json:"schema_version"`
	Project       string               `json:"project"`
	Environment   string               `json:"environment"`
	Items         []ExplorerConnection `json:"items"`
}

func ExplorerSupported(engine string) bool {
	switch engine {
	case "postgresql", "mysql", "mongodb", "clickhouse", "oracle":
		return true
	default:
		return false
	}
}

// ExplorerSource evaluates the current source observation. It does not claim
// that Kelvo, network access, or the explorer service has been provisioned.
func ExplorerSource(d Resource, now time.Time, canWrite bool) ExplorerConnection {
	c := ExplorerConnection{DatabaseID: d.ID, Revision: d.Revision, Name: d.Spec.Name, Engine: d.Spec.Engine, State: "unavailable"}
	switch {
	case !ExplorerSupported(d.Spec.Engine):
		c.Reason = "engine_unsupported"
	case d.DeletedAt != nil:
		c.Reason = "database_removed"
	case d.Recovery != nil && d.Recovery.InspectedAt == nil:
		c.Reason = "recovery_inspection_required"
	case d.Status != "ready":
		c.Reason = "database_not_ready"
	case !d.Observation.Fresh(now, d.Revision):
		c.Reason = "observation_stale"
	case d.Observation.Status != "ready":
		c.Reason = "database_not_ready"
	case !d.Spec.TLSRequired() || d.Observation.TLS == nil || !d.Observation.TLS.Verified || !d.Observation.TLS.PlaintextRejected:
		c.Reason = "verified_tls_required"
	case d.Observation.TLS.ExpiresAt == nil || !now.Before(*d.Observation.TLS.ExpiresAt):
		c.Reason = "tls_expired"
	case !explorerEndpointAvailable(d):
		c.Reason = "private_endpoint_unavailable"
	default:
		c.State, c.Reason, c.CanWrite = "eligible", "source_ready", canWrite
	}
	return c
}

// Kelvo uses HTTPS for ClickHouse and the replica-set endpoint for MongoDB.
// An unrelated service endpoint is not enough to make a source eligible.
func explorerEndpointAvailable(d Resource) bool {
	purpose := "read_write"
	switch d.Spec.Engine {
	case "clickhouse":
		purpose = "https"
	case "mongodb":
		purpose = "cluster"
	}
	for _, endpoint := range d.Observation.Endpoints {
		if endpoint.Purpose == purpose && endpoint.Host != "" && len(endpoint.Host) <= 253 &&
			!strings.ContainsAny(endpoint.Host, " /\\\t\r\n\x00@?#") && endpoint.Port > 0 && endpoint.Port <= 65535 {
			return true
		}
	}
	return false
}
