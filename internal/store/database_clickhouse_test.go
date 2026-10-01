package store

import (
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestClickHouseReservationAndBindingAuthorization(t *testing.T) {
	d := database.Resource{Status: "ready", Spec: database.Spec{Engine: "clickhouse", Mode: "cluster", Shards: 2, Replicas: 1, Memory: "2Gi", StorageGiB: 10}, Observation: database.Observation{Status: "ready"}}
	if got := DatabaseMemoryReservation(d.Spec); got != int64((2048+50)*5+128+306*4)<<20 {
		t.Fatal("ClickHouse reservation omitted Keeper or replacement capacity", got)
	}
	if got := DatabaseStorageReservation(d.Spec); got != 83 {
		t.Fatal("ClickHouse reservation omitted staging or Keeper storage", got)
	}
	if err := validateDatabaseBinding(d, spec.Binding{Protocol: "clickhouse", Endpoint: "cluster", ClusterAware: true}); err != nil {
		t.Fatal(err)
	}
	for _, binding := range []spec.Binding{{Protocol: "mysql", Endpoint: "read_write"}, {Protocol: "clickhouse", Endpoint: "cluster"}, {Protocol: "clickhouse", Endpoint: "read_only", ClusterAware: true}} {
		if validateDatabaseBinding(d, binding) == nil {
			t.Fatal("incompatible ClickHouse binding accepted")
		}
	}
}
