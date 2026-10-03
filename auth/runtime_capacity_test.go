package auth

import (
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

func TestManagedPlatformCapacityValidationForwardsTrustedSchedulingPool(t *testing.T) {
	policy := managedplatform.CapacityPolicy{SchedulingPool: "databases", SchedulingRuntimeClass: "runsc", Nodes: []managedplatform.CapacityNode{{Name: "worker", UID: "uid-worker", Architecture: "amd64", OperatingSystem: "linux"}}}
	reservations := managedPlatformNodeReservations(policy, managedplatform.CapacityPoolOwnership{})
	if len(reservations) != 1 || reservations["worker"].SchedulingPool != "databases" || reservations["worker"].SchedulingRuntimeClass != "runsc" {
		t.Fatalf("trusted scheduling pool was not forwarded to runtime validation: %#v", reservations)
	}
}
