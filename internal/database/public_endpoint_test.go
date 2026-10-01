package database

import (
	"testing"
	"time"
)

func publicEndpointDatabase() Resource {
	now := time.Now().UTC()
	return Resource{
		ID: "0123456789abcdef0123456789abcdef", Project: "demo", Environment: "development", Revision: 4, Status: "ready",
		Spec:        Spec{SchemaVersion: 1, Name: "orders", Engine: "postgresql", Version: "18", Mode: "cluster", Replicas: 2, Shards: 1, CPU: "500m", Memory: "1Gi", StorageGiB: 10, TLS: &TLSConfig{Mode: "required"}},
		Observation: Observation{ObservedAt: now, Revision: 4, Status: "ready", TopologyFingerprint: "topology", Endpoints: []Endpoint{{Purpose: "read_write", Host: "database-rw.hdb-fixture.svc", Port: 5432}, {Purpose: "read_only", Host: "database-ro.hdb-fixture.svc", Port: 5432}}, TLS: &TLSObservation{Verified: true, PlaintextRejected: true, Fingerprint: "server-fingerprint"}},
	}
}

func TestPublicEndpointPlanBindsHealthyPostgreSQLState(t *testing.T) {
	now := time.Now().UTC()
	d := publicEndpointDatabase()
	d.Observation.ObservedAt = now
	plan, err := PlanPublicEndpoint(d, PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.9/24", "198.51.100.0/24"}, MaxConnections: 32}, PublicEndpointAllocation{ID: "allocation", Host: "orders-rw.db.example.test", Address: "192.0.2.20", Port: 15432}, "endpoint", 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.BlockedReasons) != 0 || plan.Spec.SourceCIDRs[0] != "192.0.2.0/24" || plan.DatabaseRevision != d.Revision || plan.TLSFingerprint != "server-fingerprint" || plan.TopologyFingerprint != "topology" {
		t.Fatal("review did not bind canonical input and current database evidence", plan)
	}
}

func TestPublicEndpointPlanRejectsUnsupportedOrUnsafeRoutes(t *testing.T) {
	now := time.Now().UTC()
	for _, change := range []func(*Resource, *PublicEndpointSpec){
		func(d *Resource, _ *PublicEndpointSpec) { d.Spec.Engine = "mongodb" },
		func(d *Resource, _ *PublicEndpointSpec) { d.Spec.TLS = nil },
		func(_ *Resource, s *PublicEndpointSpec) { s.Purpose = "administration" },
		func(_ *Resource, s *PublicEndpointSpec) { s.SourceCIDRs = nil },
		func(_ *Resource, s *PublicEndpointSpec) { s.SourceCIDRs = []string{"::/0"} },
		func(_ *Resource, s *PublicEndpointSpec) { s.MaxConnections = 257 },
	} {
		d := publicEndpointDatabase()
		d.Observation.ObservedAt = now
		spec := PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 32}
		change(&d, &spec)
		if _, err := PlanPublicEndpoint(d, spec, PublicEndpointAllocation{ID: "allocation", Host: "orders.db.example.test", Address: "192.0.2.20", Port: 15432}, "endpoint", 0, now); err == nil {
			t.Fatal("unsafe public endpoint review accepted")
		}
	}
}

func TestPublicEndpointPlanBlocksStaleRecoveryAndTLS(t *testing.T) {
	now := time.Now().UTC()
	d := publicEndpointDatabase()
	d.Observation.ObservedAt = now.Add(-time.Minute)
	d.Observation.TLS.Verified = false
	d.Recovery = &Recovery{JobID: "restore"}
	plan, err := PlanPublicEndpoint(d, PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"0.0.0.0/0"}, MaxConnections: 1}, PublicEndpointAllocation{ID: "allocation", Host: "orders.db.example.test", Address: "192.0.2.20", Port: 15432}, "endpoint", 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.BlockedReasons) != 3 || len(plan.Warnings) != 2 {
		t.Fatal("review did not expose every blocking fact and warning", plan)
	}
}
