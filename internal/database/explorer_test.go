package database

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func explorerFixture(now time.Time) Resource {
	expires := now.Add(time.Hour)
	return Resource{ID: strings.Repeat("a", 32), Project: "demo", Environment: "development", Revision: 3, Spec: Spec{Name: "fixture", Engine: "postgresql", TLS: &TLSConfig{Mode: "required"}}, Status: "ready", EncryptedCredentials: []byte("credential-fixture"), Observation: Observation{ObservedAt: now, Revision: 3, Status: "ready", Endpoints: []Endpoint{{Purpose: "read_write", Host: "private-db.example.test", Port: 5432}}, TLS: &TLSObservation{Verified: true, PlaintextRejected: true, ExpiresAt: &expires}}}
}

func TestExplorerSourceRequiresCurrentVerifiedDatabase(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name, reason string
		change       func(*Resource)
	}{
		{"ready", "source_ready", func(*Resource) {}},
		{"unsupported", "engine_unsupported", func(d *Resource) { d.Spec.Engine = "redis" }},
		{"compatible is not qualified", "engine_unsupported", func(d *Resource) { d.Spec.Engine = "vitess" }},
		{"removed", "database_removed", func(d *Resource) { d.DeletedAt = &now }},
		{"pending", "database_not_ready", func(d *Resource) { d.Status = "provisioning" }},
		{"stale", "observation_stale", func(d *Resource) { d.Observation.ObservedAt = now.Add(-ObservationMaxAge - time.Second) }},
		{"future", "observation_stale", func(d *Resource) { d.Observation.ObservedAt = now.Add(time.Second) }},
		{"revision", "observation_stale", func(d *Resource) { d.Observation.Revision-- }},
		{"health", "database_not_ready", func(d *Resource) { d.Observation.Status = "degraded" }},
		{"TLS absent", "verified_tls_required", func(d *Resource) { d.Observation.TLS = nil }},
		{"TLS unverified", "verified_tls_required", func(d *Resource) { d.Observation.TLS.Verified = false }},
		{"plaintext accepted", "verified_tls_required", func(d *Resource) { d.Observation.TLS.PlaintextRejected = false }},
		{"expired", "tls_expired", func(d *Resource) { d.Observation.TLS.ExpiresAt = &now }},
		{"no expiry", "tls_expired", func(d *Resource) { d.Observation.TLS.ExpiresAt = nil }},
		{"endpoint absent", "private_endpoint_unavailable", func(d *Resource) { d.Observation.Endpoints = nil }},
		{"endpoint empty", "private_endpoint_unavailable", func(d *Resource) { d.Observation.Endpoints = []Endpoint{{}} }},
		{"endpoint wrong purpose", "private_endpoint_unavailable", func(d *Resource) { d.Observation.Endpoints[0].Purpose = "metrics" }},
		{"endpoint invalid port", "private_endpoint_unavailable", func(d *Resource) { d.Observation.Endpoints[0].Port = 65536 }},
		{"endpoint URL", "private_endpoint_unavailable", func(d *Resource) { d.Observation.Endpoints[0].Host = "https://private-db.example.test" }},
		{"restore", "recovery_inspection_required", func(d *Resource) { d.Recovery = &Recovery{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := explorerFixture(now)
			tc.change(&d)
			got := ExplorerSource(d, now, true)
			if got.Reason != tc.reason || (got.State == "eligible") != (tc.reason == "source_ready") || got.CanWrite != (tc.reason == "source_ready") {
				t.Fatalf("wrong source state: %+v", got)
			}
		})
	}
}

func TestExplorerCatalogNeverCarriesCredentialsOrEndpoints(t *testing.T) {
	now := time.Now().UTC()
	for _, engine := range []string{"postgresql", "mysql", "mongodb", "clickhouse", "oracle"} {
		d := explorerFixture(now)
		d.Spec.Engine = engine
		if engine == "mongodb" {
			d.Observation.Endpoints[0].Purpose = "cluster"
		}
		if engine == "clickhouse" {
			d.Observation.Endpoints[0].Purpose = "https"
		}
		got := ExplorerSource(d, now, false)
		if !ExplorerSupported(engine) || got.State != "eligible" || got.CanWrite {
			t.Fatal("unsupported engine or invented write permission", engine)
		}
		blob, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"credential-fixture", "private-db.example.test", "5432", "password", "certificate"} {
			if strings.Contains(string(blob), secret) {
				t.Fatal("catalog exposed connection data")
			}
		}
	}
	if ExplorerSupported("sqlite") {
		t.Fatal("local SQLite is not a managed database")
	}
}
