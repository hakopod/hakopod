package store

import (
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestMongoDBReservationAndRouteAuthorization(t *testing.T) {
	d := database.Resource{Status: "ready", Spec: database.Spec{Engine: "mongodb", Mode: "cluster", Shards: 1, Replicas: 2, Memory: "1Gi", StorageGiB: 10}, Observation: database.Observation{Status: "ready"}}
	for _, replicas := range []int{0, 2, 4, 6} {
		d.Spec.Mode, d.Spec.Replicas = "cluster", replicas
		if replicas == 0 {
			d.Spec.Mode = "standalone"
		}
		want := int64((1024+256+50)*(replicas+2)+128) << 20
		if got := DatabaseMemoryReservation(d.Spec); got != want {
			t.Fatal("MongoDB operational allocation omitted agent or replacement resources", got, want)
		}
		if got := DatabaseStorageReservation(d.Spec); got != int64(replicas+1)*11 {
			t.Fatal("MongoDB allocation omitted member log volumes", got)
		}
		if err := validateDatabaseBinding(d, spec.Binding{Protocol: "mongodb", Endpoint: "cluster", ClusterAware: true}); err != nil {
			t.Fatal(err)
		}
	}
	for _, binding := range []spec.Binding{{Protocol: "postgres", Endpoint: "read_write"}, {Protocol: "mongodb", Endpoint: "cluster"}, {Protocol: "mongodb", Endpoint: "read_write", ClusterAware: true}, {Protocol: "mongodb", Endpoint: "read_only", ClusterAware: true}} {
		if validateDatabaseBinding(d, binding) == nil {
			t.Fatal("MongoDB accepted an incompatible binding")
		}
	}
}
