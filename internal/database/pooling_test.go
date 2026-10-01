package database

import "testing"

func TestPoolingPreservesRouteAndCapacityConstraints(t *testing.T) {
	s := Spec{SchemaVersion: 1, Name: "pooling-fixture", Engine: "postgresql", Version: "17", Mode: "cluster", Shards: 1, Replicas: 1, CPU: "250m", Memory: "512Mi", StorageGiB: 1, TLS: &TLSConfig{Mode: "required"}, Pooling: &Pooling{Mode: "transaction", Instances: 2, MaxClientConnections: 200, DefaultPoolSize: 10, ReadOnly: true}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.PoolerInstances() != 4 || s.PlacementDomains() != 2 {
		t.Fatal("pooler allocation changed")
	}
	for _, mutate := range []func(*Spec){func(s *Spec) { s.Engine = "redis" }, func(s *Spec) { s.TLS = nil }, func(s *Spec) { s.Pooling.Mode = "statement" }, func(s *Spec) { s.Pooling.Instances = 4 }, func(s *Spec) { s.Pooling.DefaultPoolSize = 21 }, func(s *Spec) { s.Pooling.MaxClientConnections = 2001 }, func(s *Spec) { s.Mode = "standalone"; s.Replicas = 0 }} {
		bad := s
		p := *s.Pooling
		bad.Pooling = &p
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatal("invalid pooling accepted", bad)
		}
	}
}
