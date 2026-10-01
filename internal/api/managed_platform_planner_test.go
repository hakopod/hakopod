package api

import (
	"context"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
)

func TestNativeManagedPlatformPlannerDoesNotPromoteCapacityToQualification(t *testing.T) {
	spec := managedplatform.Spec{SchemaVersion: 1, Name: "neon-fixture", Kind: "neon", Version: managedplatform.NeonVersion, Resources: map[string]managedplatform.Resources{}, Storage: map[string]int64{}, Secrets: map[string]managedplatform.SecretReference{}, Placement: managedplatform.Placement{NodeNames: []string{"node-a", "node-b", "node-c"}}, Neon: &managedplatform.NeonConfig{PostgresVersion: "17", ComputeReplicas: 1, Pageservers: 2, Safekeepers: 3, BranchLimit: 8, ObjectStorageURL: "https://objects.example.test", ObjectStorageBucket: "neon-fixture", ObjectStorageRegion: "us-east-1", ObjectStoragePrefix: "fixture", ProxyControlPlanePatchSHA256: managedplatform.NeonProxyControlPlanePatchSHA256}}
	images := map[string]string{}
	for _, name := range managedplatform.NeonComponents() {
		spec.Resources[name] = managedplatform.Resources{CPU: "500m", Memory: "2Gi"}
		images[name] = "registry.example.test/neon/" + name + "@sha256:" + strings.Repeat("a", 64)
	}
	for _, name := range managedplatform.NeonStorageKeys() {
		spec.Storage[name] = 20
	}
	for _, name := range managedplatform.NeonSecretKeys() {
		spec.Secrets[name] = managedplatform.SecretReference{Name: "neon-" + name, Revision: 1}
	}
	planner := NativeManagedPlatformPlanner{NeonImages: images, ApprovedEncryptedStorageClass: "encrypted-block"}
	plan, err := planner.PlanManagedPlatform(context.Background(), store.Principal{}, store.ManagedPlatform{ID: strings.Repeat("b", 32), Project: "demo", Environment: "development", Spec: spec}, 0, "create")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Capability.Available || plan.Capability.ClusterQualified || plan.Capability.PublicQualified {
		t.Fatalf("capacity-ready planning promoted an unqualified native platform: %#v", plan.Capability)
	}
	if plan.StorageClass != "encrypted-block" {
		t.Fatalf("trusted storage class was not carried into the plan: %q", plan.StorageClass)
	}
}
