package managedplatform

import (
	"strings"
	"testing"
)

func platformImages(names []string) map[string]string {
	images := make(map[string]string, len(names))
	for _, name := range names {
		images[name] = "registry.example.test/platform/" + name + "@sha256:" + strings.Repeat("a", 64)
	}
	return images
}

func TestSupabaseCapacityCountsEachRenderedPVCOnce(t *testing.T) {
	spec := supabaseCandidateSpec()
	plan, err := PlanSupabase(spec, platformImages(SupabaseComponentNames()))
	if err != nil {
		t.Fatal(err)
	}
	plan.StorageClass = "encrypted-block"
	reservations, err := CapacityReservations(spec, plan)
	if err != nil {
		t.Fatal(err)
	}
	total := ReservationTotal(reservations)
	storage, workloads := 0, 0
	for _, reservation := range reservations {
		if strings.HasPrefix(reservation.Key, "storage/") {
			storage++
		} else {
			workloads++
		}
		if reservation.NodeName != "worker-a" {
			t.Fatalf("Supabase reservation escaped its only node: %#v", reservation)
		}
	}
	if storage != len(SupabaseRequiredStorageKeys()) {
		t.Fatalf("created %d storage reservations", storage)
	}
	if total.CPUMilli != 3000 || total.MemoryBytes != 7<<30+int64(workloads)*PodMemoryOverheadBytes || total.StorageGiB != 43 {
		t.Fatalf("unexpected Supabase reservation: %#v", total)
	}
	plan.SchedulingPool, plan.SchedulingRuntimeClass = "databases", "runsc"
	sandboxed, err := CapacityReservations(spec, plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := ReservationTotal(sandboxed).CPUMilli; got != total.CPUMilli+int64(workloads)*SandboxCPUOverheadMilli {
		t.Fatalf("sandbox CPU overhead was not reserved per pod: %dm", got)
	}
}

func TestNeonCapacityMatchesSidecarsPVCsAndRendererPlacement(t *testing.T) {
	spec := neonCandidateSpec()
	plan, err := PlanNeon(spec, platformImages(NeonComponents()))
	if err != nil {
		t.Fatal(err)
	}
	plan.StorageClass = "encrypted-block"
	reservations, err := CapacityReservations(spec, plan)
	if err != nil {
		t.Fatal(err)
	}
	total := ReservationTotal(reservations)
	byNode := map[string]Capacity{}
	workloads := 0
	for _, reservation := range reservations {
		byNode[reservation.NodeName] = byNode[reservation.NodeName].Add(reservation.Capacity)
		if strings.HasPrefix(reservation.Key, "workload/") {
			workloads++
		}
	}
	if total.CPUMilli != 5500 || total.MemoryBytes != 22<<30+int64(workloads)*PodMemoryOverheadBytes || total.StorageGiB != 140 {
		t.Fatalf("unexpected Neon reservation: %#v", total)
	}
	plan.SchedulingPool, plan.SchedulingRuntimeClass = "databases", "runsc"
	sandboxed, err := CapacityReservations(spec, plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := ReservationTotal(sandboxed).CPUMilli; got != total.CPUMilli+int64(workloads)*SandboxCPUOverheadMilli {
		t.Fatalf("sandbox CPU overhead was not reserved per rendered pod: %dm", got)
	}
	if byNode["node-a"].CPUMilli != 4000 || byNode["node-b"].CPUMilli != 1000 || byNode["node-c"].CPUMilli != 500 {
		t.Fatalf("unexpected Neon placement: %#v", byNode)
	}
	compute := Capacity{}
	for _, reservation := range reservations {
		if reservation.Key == "workload/compute/0" {
			compute = reservation.Capacity
		}
	}
	if compute.CPUMilli != 1000 || compute.MemoryBytes != 4<<30+PodMemoryOverheadBytes {
		t.Fatalf("compute sidecar was not reserved: %#v", compute)
	}
}

func TestCapacityPolicyRejectsStorageOrNodeDrift(t *testing.T) {
	spec := supabaseCandidateSpec()
	plan, err := PlanSupabase(spec, platformImages(SupabaseComponentNames()))
	if err != nil {
		t.Fatal(err)
	}
	plan.StorageClass = "encrypted-block"
	policy := CapacityPolicy{Enabled: true, Pool: "fixture", Capacity: Capacity{CPUMilli: 4000, MemoryBytes: 8 << 30, StorageGiB: 64}, Nodes: []CapacityNode{{Name: "worker-a", UID: "uid-a", Architecture: "amd64", OperatingSystem: "linux"}}, StorageClass: "encrypted-block"}
	if err = policy.Allows(spec, plan); err != nil {
		t.Fatal(err)
	}
	unscoped := policy
	unscoped.Pool = ""
	if err = unscoped.Validate(); err == nil {
		t.Fatal("unscoped capacity policy accepted")
	}
	plan.StorageClass = "other"
	if err = policy.Allows(spec, plan); err == nil {
		t.Fatal("unapproved StorageClass accepted")
	}
	plan.StorageClass = "encrypted-block"
	spec.Placement.NodeNames = []string{"worker-b"}
	if err = policy.Allows(spec, plan); err == nil {
		t.Fatal("unapproved node accepted")
	}
}

func TestCapacityPolicyFingerprintBindsSchedulingPool(t *testing.T) {
	policy := CapacityPolicy{Enabled: true, Pool: "workspace", SchedulingPool: "databases", SchedulingRuntimeClass: "runsc", Capacity: Capacity{CPUMilli: 1000, MemoryBytes: 1 << 30, StorageGiB: 10}, Nodes: []CapacityNode{{Name: "worker", UID: "uid-worker", Architecture: "amd64", OperatingSystem: "linux"}}, StorageClass: "encrypted"}
	withPool, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	policy.SchedulingPool = ""
	policy.SchedulingRuntimeClass = ""
	withoutPool, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if withPool == withoutPool {
		t.Fatal("scheduling pool was not bound into the capacity fingerprint")
	}
}
