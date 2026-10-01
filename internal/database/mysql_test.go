package database

import "testing"

func TestMySQLRequiresTLSQuorumAndCapacity(t *testing.T) {
	s := Spec{SchemaVersion: 1, Name: "mysql", Engine: "mysql", Version: "8.4", Mode: "cluster", Shards: 1, Replicas: 2, CPU: "500m", Memory: "1Gi", StorageGiB: 1, TLS: &TLSConfig{Mode: "required"}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, replicas := range []int{2, 4, 6} {
		s.Replicas = replicas
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, change := range []func(*Spec){func(s *Spec) { s.TLS = nil }, func(s *Spec) { s.Replicas = 1 }, func(s *Spec) { s.Replicas = 3 }, func(s *Spec) { s.Shards = 2 }, func(s *Spec) { s.Version = "8.0" }, func(s *Spec) { s.Memory = "512Mi" }, func(s *Spec) { s.CPU = "100m" }, func(s *Spec) { s.Pooling = &Pooling{} }} {
		next := s
		change(&next)
		if next.Validate() == nil {
			t.Fatalf("invalid MySQL spec accepted: %+v", next)
		}
	}
	s.Mode, s.Replicas = "standalone", 0
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.RouterInstances() != 1 {
		t.Fatal("standalone router reservation is wrong")
	}
}
