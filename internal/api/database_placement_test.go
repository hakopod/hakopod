package api

import (
	"context"
	"github.com/hakopod/hakopod/internal/store"
	"net/http/httptest"
	"testing"
)

func TestDatabasePlacementRejectsForeignScopeBeforeRuntime(t *testing.T) {
	principal := store.Principal{ID: "reader", Project: "owned", Environment: "production", Permissions: []string{"deployments:read"}, IdentityPermissions: []string{"deployments:read"}}
	for _, test := range []struct {
		query  string
		status int
	}{{"?project=other&environment=production", 403}, {"?project=owned&environment=development", 403}, {"?project=owned&environment=production&node=secret", 400}, {"?project=owned&project=other&environment=production", 400}, {"?project=owned&environment=production", 503}} {
		r := httptest.NewRequest("GET", "/api/v1/database-placement/nodes"+test.query, nil)
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal))
		w := httptest.NewRecorder()
		(&Server{}).databasePlacementNodes(w, r)
		if w.Code != test.status {
			t.Fatalf("%s got %d want %d", test.query, w.Code, test.status)
		}
	}
	principal.Application = "app"
	r := httptest.NewRequest("GET", "/api/v1/database-placement/nodes?project=owned&environment=production", nil).WithContext(context.WithValue(context.Background(), principalKey{}, principal))
	w := httptest.NewRecorder()
	(&Server{}).databasePlacementNodes(w, r)
	if w.Code != 403 {
		t.Fatal("application learned database node inventory")
	}
}
