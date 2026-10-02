package api

import (
	"context"
	"errors"
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
	checks := 0
	planner := NativeManagedPlatformPlanner{NeonImages: images, ApprovedEncryptedStorageClass: "encrypted-block", ValidateNeonQualification: func(context.Context) error { checks++; return nil }}
	plan, err := planner.PlanManagedPlatform(context.Background(), store.Principal{}, store.ManagedPlatform{ID: strings.Repeat("b", 32), Project: "demo", Environment: "development", Spec: spec}, 0, "create")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Capability.Available || plan.Capability.ClusterQualified || plan.Capability.PublicQualified {
		t.Fatalf("capacity-ready planning promoted an unqualified native platform: %#v", plan.Capability)
	}
	if checks != 1 {
		t.Fatalf("Neon operator binding checks = %d, want 1", checks)
	}
	if plan.StorageClass != "encrypted-block" {
		t.Fatalf("trusted storage class was not carried into the plan: %q", plan.StorageClass)
	}
	bindingErr := errors.New("Neon binding changed")
	planner.ValidateNeonQualification = func(context.Context) error { return bindingErr }
	if _, err = planner.PlanManagedPlatform(context.Background(), store.Principal{}, store.ManagedPlatform{ID: strings.Repeat("b", 32), Project: "demo", Environment: "development", Spec: spec}, 0, "create"); !errors.Is(err, bindingErr) {
		t.Fatalf("changed Neon operator binding was not refused: %v", err)
	}
	planner.ValidateNeonQualification = func(context.Context) error { return nil }
	for _, name := range spec.Placement.NodeNames {
		planner.CatalogNodes = append(planner.CatalogNodes, managedplatform.CapacityNode{Name: name, UID: name + "-uid", Architecture: "amd64", OperatingSystem: "linux"})
	}
	for _, test := range []struct{ name, architecture, operatingSystem, reason string }{
		{"node-a", "arm64", "linux", "requires linux/amd64"},
		{"node-a", "amd64", "windows", "requires linux/amd64"},
		{"another-node", "amd64", "linux", "outside the workspace capacity grant"},
	} {
		planner.CatalogNodes[0].Name, planner.CatalogNodes[0].Architecture, planner.CatalogNodes[0].OperatingSystem = test.name, test.architecture, test.operatingSystem
		plan, err = planner.PlanManagedPlatform(context.Background(), store.Principal{}, store.ManagedPlatform{ID: strings.Repeat("b", 32), Project: "demo", Environment: "development", Spec: spec}, 0, "create")
		if err == nil || !strings.Contains(err.Error(), test.reason) || plan.Capability.Available {
			t.Fatalf("Neon accepted incompatible or ungranted placement: %+v, %v", plan.Capability, err)
		}
	}
}

func TestSupabasePlannerRechecksOperatorBindingButKeepsReleaseGateClosed(t *testing.T) {
	entry := managedplatform.CatalogEntries()[0]
	spec := entry.DefaultSpec
	spec.Name = "supabase-fixture"
	spec.Placement.NodeNames = []string{"node-a"}
	spec.Supabase.PublicURL, spec.Supabase.SiteURL = "https://data.example.test", "https://app.example.test"
	for _, key := range entry.RequiredSecretKeys {
		spec.Secrets[key] = managedplatform.SecretReference{Name: "supabase-" + key, Revision: 1}
	}
	images := map[string]string{}
	for _, name := range managedplatform.SupabaseComponentNames() {
		images[name] = "registry.example.test/supabase/" + name + "@sha256:" + strings.Repeat("a", 64)
	}
	checks := 0
	planner := NativeManagedPlatformPlanner{SupabaseImages: images, ApprovedEncryptedStorageClass: "encrypted-block", CatalogNodes: []managedplatform.CapacityNode{{Name: "node-a", UID: "node-a-uid", Architecture: "amd64", OperatingSystem: "linux"}}, ValidateSupabaseQualification: func(context.Context) error { checks++; return nil }}
	plan, err := planner.PlanManagedPlatform(context.Background(), store.Principal{}, store.ManagedPlatform{ID: strings.Repeat("b", 32), Project: "demo", Environment: "development", Spec: spec}, 0, "create")
	if err != nil {
		t.Fatal(err)
	}
	if checks != 1 {
		t.Fatalf("operator binding checks = %d, want 1", checks)
	}
	if plan.Capability.Available || plan.Capability.ClusterQualified || plan.Capability.PublicQualified {
		t.Fatalf("operator binding bypassed the closed release gate: %#v", plan.Capability)
	}
	planner.CatalogNodes[0].Architecture = "arm64"
	if _, err = planner.PlanManagedPlatform(context.Background(), store.Principal{}, store.ManagedPlatform{ID: strings.Repeat("b", 32), Project: "demo", Environment: "development", Spec: spec}, 0, "create"); err == nil || !strings.Contains(err.Error(), "requires linux/amd64") {
		t.Fatalf("selected ARM node was not refused before apply: %v", err)
	}
	planner.CatalogNodes[0].Architecture = "amd64"
	planner.CatalogNodes[0].OperatingSystem = "windows"
	if _, err = planner.PlanManagedPlatform(context.Background(), store.Principal{}, store.ManagedPlatform{ID: strings.Repeat("b", 32), Project: "demo", Environment: "development", Spec: spec}, 0, "create"); err == nil || !strings.Contains(err.Error(), "requires linux/amd64") {
		t.Fatalf("selected Windows node was not refused before apply: %v", err)
	}
	planner.CatalogNodes[0].OperatingSystem = "linux"
	bindingErr := errors.New("binding changed")
	planner.ValidateSupabaseQualification = func(context.Context) error { return bindingErr }
	if _, err = planner.PlanManagedPlatform(context.Background(), store.Principal{}, store.ManagedPlatform{ID: strings.Repeat("b", 32), Project: "demo", Environment: "development", Spec: spec}, 0, "create"); !errors.Is(err, bindingErr) {
		t.Fatalf("changed operator binding was not refused: %v", err)
	}
}
