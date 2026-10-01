package store

import (
	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
	"testing"
)

func TestDatabasePoolingReservationAndBindings(t *testing.T) {
	d := database.Resource{Status: "ready", Spec: database.Spec{Engine: "postgresql", Mode: "cluster", Shards: 1, Replicas: 1, Memory: "512Mi"}, Observation: database.Observation{Status: "ready"}}
	base := DatabaseMemoryReservation(d.Spec)
	b := spec.Binding{Protocol: "postgres", Endpoint: "pooled_read_write"}
	if validateDatabaseBinding(d, b) == nil {
		t.Fatal("unconfigured pooled binding accepted")
	}
	d.Spec.Pooling = &database.Pooling{Mode: "session", Instances: 2, MaxClientConnections: 200, DefaultPoolSize: 10}
	if validateDatabaseBinding(d, b) != nil {
		t.Fatal("configured write pool binding rejected")
	}
	b.Endpoint = "pooled_read_only"
	if validateDatabaseBinding(d, b) == nil {
		t.Fatal("unconfigured replica pool binding accepted")
	}
	d.Spec.Pooling.ReadOnly = true
	if validateDatabaseBinding(d, b) != nil {
		t.Fatal("configured replica pool binding rejected")
	}
	// Both deployments can replace one instance concurrently during certificate renewal.
	if got, want := DatabaseMemoryReservation(d.Spec)-base, int64(6*306<<20); got != want {
		t.Fatal("pooler surge capacity omitted", got, want)
	}
	b.Protocol = "redis"
	if validateDatabaseBinding(d, b) == nil {
		t.Fatal("wrong protocol accepted")
	}
}
