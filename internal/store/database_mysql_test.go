package store

import (
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestMySQLReservationAndRouteAuthorization(t *testing.T) {
	d := database.Resource{Status: "ready", Spec: database.Spec{Engine: "mysql", Mode: "cluster", Shards: 1, Replicas: 2, Memory: "1Gi"}, Observation: database.Observation{Status: "ready"}}
	for _, replicas := range []int{0, 2, 4, 6} {
		d.Spec.Mode, d.Spec.Replicas = "cluster", replicas
		if replicas == 0 {
			d.Spec.Mode = "standalone"
		}
		// Server, sidecar and sandbox overhead for every member and one
		// replacement; Router surge and bounded recovery work are separate.
		wantMiB := int64((1024+256+50)*(replicas+2) + 128 + (d.Spec.RouterInstances()+1)*(128+50))
		if got := DatabaseMemoryReservation(d.Spec); got != wantMiB<<20 {
			t.Fatal("MySQL operational allocation omitted resources", got, wantMiB<<20)
		}
		if err := validateDatabaseBinding(d, spec.Binding{Protocol: "mysql", Endpoint: "read_write"}); err != nil {
			t.Fatal(err)
		}
		readOnly := validateDatabaseBinding(d, spec.Binding{Protocol: "mysql", Endpoint: "read_only"})
		if (readOnly == nil) != (replicas > 0) {
			t.Fatal("MySQL replica route authorization differs from configured members")
		}
	}
	for _, binding := range []spec.Binding{{Protocol: "postgres", Endpoint: "read_write"}, {Protocol: "mysql", Endpoint: "pooled_read_write"}, {Protocol: "mysql", Endpoint: "cluster", ClusterAware: true}, {Protocol: "mysql", Endpoint: "read_write", ClusterAware: true}} {
		if validateDatabaseBinding(d, binding) == nil {
			t.Fatal("MySQL accepted an incompatible binding")
		}
	}
}
