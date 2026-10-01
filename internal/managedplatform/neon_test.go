package managedplatform

import (
	"strings"
	"testing"
)

func neonCandidateSpec() Spec {
	s := Spec{SchemaVersion: 1, Name: "analytics", Kind: "neon", Version: NeonVersion, Resources: map[string]Resources{}, Storage: map[string]int64{}, Secrets: map[string]SecretReference{}, Placement: Placement{NodeNames: []string{"node-a", "node-b", "node-c"}}, Neon: &NeonConfig{PostgresVersion: "17", ComputeReplicas: 1, Pageservers: 2, Safekeepers: 3, BranchLimit: 16, ObjectStorageURL: "https://objects.example.test", ObjectStorageBucket: "neon-objects", ObjectStorageRegion: "test-region-1", ObjectStoragePrefix: "analytics", ProxyControlPlanePatchSHA256: NeonProxyControlPlanePatchSHA256}}
	for _, key := range NeonComponents() {
		s.Resources[key] = Resources{CPU: "500m", Memory: "2Gi"}
	}
	for _, key := range NeonStorageKeys() {
		s.Storage[key] = 20
	}
	for _, key := range NeonSecretKeys() {
		s.Secrets[key] = SecretReference{Name: "neon-" + key, Revision: 1}
	}
	return s
}

func TestNeonPlanIncludesStorageControlPlaneAndQuorumButStaysUnavailable(t *testing.T) {
	s := neonCandidateSpec()
	images := map[string]string{}
	for _, key := range NeonComponents() {
		images[key] = "registry.example.test/neon/" + key + "@sha256:" + strings.Repeat("a", 64)
	}
	plan, err := PlanNeon(s, images)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Capability.Available || plan.Capability.ClusterQualified || plan.Capability.PublicQualified || len(plan.Components) != 8 {
		t.Fatal("incomplete or falsely available Neon plan")
	}
	for _, component := range plan.Components {
		if len(component.Ports) == 0 || component.Name == "safekeeper" && component.Replicas != 3 {
			t.Fatal("missing listeners or WAL quorum")
		}
	}
}

func TestNeonRejectsIncompleteStorageAndUnsafeRemoteCredentials(t *testing.T) {
	for _, mutate := range []func(*Spec){
		func(s *Spec) { s.Version = "latest" },
		func(s *Spec) { s.Neon.Safekeepers = 1 },
		func(s *Spec) { delete(s.Resources, "storage-controller") },
		func(s *Spec) { delete(s.Storage, "safekeeper") },
		func(s *Spec) { delete(s.Secrets, "object-storage") },
		func(s *Spec) { s.Neon.ObjectStorageURL = "https://user:password@objects.example.test" },
		func(s *Spec) { s.Neon.ObjectStoragePrefix = "../other-tenant" },
		func(s *Spec) { s.Placement.NodeNames = nil },
		func(s *Spec) { s.Supabase = &SupabaseConfig{} },
	} {
		s := neonCandidateSpec()
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Fatal("unsafe Neon plan accepted")
		}
	}
}
