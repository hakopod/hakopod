package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
)

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
	planner := &NativeManagedPlatformPlanner{ApprovedEncryptedStorageClass: "encrypted", CatalogNodes: []managedplatform.CapacityNode{{Name: "approved-node", UID: "node-uid"}}, CatalogSecrets: map[string]map[string][]managedplatform.SecretReference{
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
	}
}
