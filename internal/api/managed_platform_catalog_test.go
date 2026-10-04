package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
)

func TestReleasedPlatformCatalogStillRequiresCurrentOperatorApproval(t *testing.T) {
	for _, name := range []string{"Neon", "Supabase"} {
		t.Run(name, func(t *testing.T) {
			initial := managedplatform.Capability{Available: true, ClusterQualified: true, PublicQualified: true, Reason: "release qualified"}
			for _, check := range []func(context.Context) error{nil, func(context.Context) error { return errors.New("approval withdrawn") }} {
				got := scopedPlatformCapability(context.Background(), name, true, initial, check)
				if got.Available || got.ClusterQualified || got.PublicQualified {
					t.Fatalf("release qualification bypassed missing or withdrawn operator approval: %+v", got)
				}
			}
			approved := func(ctx context.Context) error {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 5*time.Second {
					t.Fatal("operator approval read has no five-second bound")
				}
				return nil
			}
			got := scopedPlatformCapability(context.Background(), name, true, initial, approved)
			if !got.Available || !got.ClusterQualified || got.PublicQualified {
				t.Fatalf("approved private capability differs: %+v", got)
			}
			got = scopedPlatformCapability(context.Background(), name, false, initial, approved)
			if got.Available || got.ClusterQualified || got.PublicQualified {
				t.Fatal("operator approval bypassed the release gate")
			}
		})
	}
}

func TestManagedPlatformCatalogRejectsInvalidOrForeignScope(t *testing.T) {
	principal := store.Principal{ID: "reader", Project: "owned", Environment: "production", Permissions: []string{"deployments:read"}, IdentityPermissions: []string{"deployments:read"}}
	for _, test := range []struct {
		query  string
		status int
	}{
		{"?project=foreign&environment=production", 403},
		{"?project=owned&environment=development", 403},
		{"?project=owned&environment=production&secret=hidden", 400},
		{"?project=owned&project=foreign&environment=production", 400},
		{"?project=owned", 400},
		{"?project=owned&environment=production", 503},
	} {
		request := httptest.NewRequest("GET", "/api/v1/managed-platforms/catalog"+test.query, nil)
		request = request.WithContext(context.WithValue(request.Context(), principalKey{}, principal))
		response := httptest.NewRecorder()
		(&Server{}).managedPlatformCatalog(response, request)
		if response.Code != test.status {
			t.Fatalf("%s: got %d want %d", test.query, response.Code, test.status)
		}
	}
	principal.Application = "application"
	request := httptest.NewRequest("GET", "/api/v1/managed-platforms/catalog?project=owned&environment=production", nil).WithContext(context.WithValue(context.Background(), principalKey{}, principal))
	response := httptest.NewRecorder()
	(&Server{}).managedPlatformCatalog(response, request)
	if response.Code != 403 {
		t.Fatal("application learned platform configuration")
	}
}

func TestManagedPlatformCatalogExposesOnlyRequestedReferencesAndCopiesNodes(t *testing.T) {
	checks := 0
	planner := &NativeManagedPlatformPlanner{ApprovedEncryptedStorageClass: "encrypted", ValidateSupabaseQualification: func(context.Context) error { checks++; return errors.New("changed binding") }, CatalogNodes: []managedplatform.CapacityNode{{Name: "approved-node", UID: "node-uid", Architecture: "amd64", OperatingSystem: "linux"}}, CatalogSecrets: map[string]map[string][]managedplatform.SecretReference{
		"owned":   {"production": {{Name: "visible", Revision: 2}}, "development": {{Name: "hidden-environment", Revision: 1}}},
		"foreign": {"production": {{Name: "hidden-project", Revision: 1}}},
	}}
	principal := store.Principal{Admin: true, Permissions: []string{"admin"}, Project: "owned", Environment: "production"}
	catalog, err := planner.ManagedPlatformCatalog(context.Background(), principal, "owned", "production")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hidden") || len(catalog.SecretReferences) != 1 || catalog.SecretReferences[0].Name != "visible" {
		t.Fatal("catalog leaked another scope")
	}
	catalog.Nodes[0].Name = "modified"
	catalog.SecretReferences[0].Name = "modified"
	if planner.CatalogNodes[0].Name != "approved-node" || planner.CatalogSecrets["owned"]["production"][0].Name != "visible" {
		t.Fatal("catalog mutated trusted configuration")
	}
	if _, err := planner.ManagedPlatformCatalog(context.Background(), principal, "foreign", "production"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("catalog planner accepted foreign scope", err)
	}
	for _, item := range catalog.Items {
		if item.Capability.Available || item.Capability.ClusterQualified || item.Capability.PublicQualified {
			t.Fatal("catalog enabled unqualified runtime")
		}
		if item.Kind == "supabase" && item.Capability.Reason != "Supabase operator qualification does not match this cluster" {
			t.Fatalf("catalog hid the current Supabase binding state: %q", item.Capability.Reason)
		}
	}
	if checks != 1 {
		t.Fatalf("Supabase catalog binding checks = %d, want 1", checks)
	}
}

func TestManagedPlatformCatalogUsesCurrentWorkspaceGrant(t *testing.T) {
	policy := managedplatform.CapacityPolicy{Enabled: true, Pool: "workspace", StorageClass: "encrypted", Capacity: managedplatform.Capacity{CPUMilli: 1000, MemoryBytes: 1 << 30, StorageGiB: 10}, Nodes: []managedplatform.CapacityNode{{Name: "owned-node", UID: "owned-uid", Architecture: "amd64", OperatingSystem: "linux"}}}
	revoked := false
	planner := &NativeManagedPlatformPlanner{ApprovedEncryptedStorageClass: "encrypted", CatalogNodes: []managedplatform.CapacityNode{{Name: "foreign-node"}}, CatalogCapacity: func(_ context.Context, project, environment string) (managedplatform.CapacityPolicy, error) {
		if revoked || project != "owned" || environment != "production" {
			return managedplatform.CapacityPolicy{}, store.ErrForbidden
		}
		return policy, nil
	}}
	principal := store.Principal{Admin: true, Permissions: []string{"admin"}, Project: "owned", Environment: "production"}
	catalog, err := planner.ManagedPlatformCatalog(context.Background(), principal, "owned", "production")
	if err != nil || len(catalog.Nodes) != 1 || catalog.Nodes[0].Name != "owned-node" {
		t.Fatal("catalog exposed nodes outside grant", catalog.Nodes, err)
	}
	revoked = true
	if _, err = planner.ManagedPlatformCatalog(context.Background(), principal, "owned", "production"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("revoked grant remained visible", err)
	}
}
