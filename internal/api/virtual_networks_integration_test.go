package api_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestVirtualNetworkAPIReviewScopeAndConnections(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	admin, err := db.Bootstrap(ctx, "network-operator")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	_, scoped, err := db.CreateKey(ctx, p, store.KeyInput{Name: "network-client", Project: "demo", Environment: "development", Application: "orders", Permissions: []string{"deployments:read", "deployments:write"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	handler := (&api.Server{Store: db}).Handler()
	call := func(method, path, token string, body any, expected int) []byte {
		t.Helper()
		request := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(string(store.JSON(body))))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != expected {
			t.Fatalf("%s %s: want %d, got %d %s", method, path, expected, response.Code, response.Body.String())
		}
		return response.Body.Bytes()
	}
	base := map[string]any{"project": "demo", "environment": "development", "toml": "schema_version=1\nname='commerce'\ndescription='Shared data services'\n[segments.data]\napplications=['orders']"}
	plan := call("POST", "/virtual-networks/plan", admin, base, 200)
	var review struct {
		Spec             spec.VirtualNetwork `json:"spec"`
		TOML             string              `json:"toml"`
		ExpectedID       string              `json:"expected_id"`
		ExpectedRevision int64               `json:"expected_revision"`
	}
	if json.Unmarshal(plan, &review) != nil || review.Spec.Name != "commerce" || review.TOML == "" || review.ExpectedID != "" || review.ExpectedRevision != 0 {
		t.Fatal("network plan is not canonical")
	}
	base["expected_revision"] = 0
	call("POST", "/virtual-networks", scoped, base, 403)
	created := call("POST", "/virtual-networks", admin, base, 201)
	var saved struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(created, &saved) != nil || len(saved.ID) != 32 {
		t.Fatal("created network is missing its runtime identity")
	}
	call("POST", "/virtual-networks", admin, base, 409)
	query := "?project=demo&environment=development"
	call("GET", "/virtual-networks"+query, scoped, nil, 403)
	call("GET", "/virtual-networks/commerce?project=demo&environment=production", admin, nil, 404)
	app, err := spec.Parse([]byte("name='orders'\n[networks.data]\nvirtual_network='commerce'\nsegment='data'\ninternal=true\n[services.api]\nimage='python:3.13-alpine'\nport=8080\nnetworks=['data']\n[services.api.env]\nMODE='not-public-in-list'"))
	if err != nil {
		t.Fatal(err)
	}
	call("POST", "/plan", scoped, map[string]any{"project": "demo", "environment": "development", "spec": app}, 200)
	if _, err := db.Accept(ctx, p, "demo", "development", app, 0, "network-api-connection"); err != nil {
		t.Fatal(err)
	}
	connections := call("GET", "/virtual-networks/commerce"+query, admin, nil, 200)
	if !strings.Contains(string(connections), ".svc.cluster.local") || !strings.Contains(string(connections), "\"service\":\"api\"") || strings.Contains(string(connections), "not-public-in-list") {
		t.Fatal("connection inventory is incomplete or leaked configuration")
	}
	candidates := call("GET", "/virtual-networks/commerce/candidates"+query, admin, nil, 200)
	if !strings.Contains(string(candidates), "\"services\":[\"api\"]") || strings.Contains(string(candidates), "not-public-in-list") {
		t.Fatal("candidate list is incomplete or contains environment values")
	}
	base["expected_revision"] = 1
	base["expected_id"] = saved.ID
	base["toml"] = "name='commerce'\n[segments.data]\napplications=[]"
	call("POST", "/virtual-networks/plan", admin, base, 409)
	call("PUT", "/virtual-networks/commerce", admin, base, 409)
	call("DELETE", "/virtual-networks/commerce", admin, map[string]any{"project": "demo", "environment": "development", "expected_id": saved.ID, "expected_revision": 1, "confirmation": "wrong"}, 400)
	call("DELETE", "/virtual-networks/commerce", admin, map[string]any{"project": "demo", "environment": "development", "expected_id": saved.ID, "expected_revision": 1, "confirmation": "commerce"}, 409)
	base["toml"] = "name='commerce'\n[segments.data]\napplications=['orders','catalog']"
	call("PUT", "/virtual-networks/commerce", admin, base, 200)
	call("PUT", "/virtual-networks/commerce", admin, base, 409)
}

func TestScopedMachineNetworkManagement(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	admin, err := db.Bootstrap(ctx, "network-owner")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.Authenticate(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	in := store.KeyInput{Name: "cloud-node", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "deployments:write"}, ExpiresAt: time.Now().Add(time.Hour)}
	_, oldKey, err := db.CreateKey(ctx, owner, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Permissions = append(in.Permissions, "networks:write")
	_, networkKey, err := db.CreateKey(ctx, owner, in)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, networkKey)
	if err != nil || principal.IsAdmin() || principal.CanManageProject("demo") {
		t.Fatal("network key gained administrator access", err)
	}
	in.Application = "orders"
	if _, _, err := db.CreateKey(ctx, owner, in); err == nil {
		t.Fatal("application-scoped network key accepted")
	}
	handler := (&api.Server{Store: db}).Handler()
	call := func(method, path, token string, body any, expected int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(string(store.JSON(body))))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != expected {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, expected, w.Body.String())
		}
		return w.Body.Bytes()
	}
	body := map[string]any{"project": "demo", "environment": "development", "expected_revision": 0, "toml": "name='shared'\n[segments.data]\napplications=['orders']"}
	call("POST", "/virtual-networks/plan", oldKey, body, 403)
	call("POST", "/virtual-networks/plan", networkKey, body, 200)
	created := call("POST", "/virtual-networks", networkKey, body, 201)
	var saved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created, &saved); err != nil {
		t.Fatal(err)
	}
	listing := call("GET", "/virtual-networks?project=demo&environment=development", networkKey, nil, 200)
	if !strings.Contains(string(listing), `"can_manage":true`) {
		t.Fatal("missing scoped management capability")
	}
	call("GET", "/virtual-networks?project=demo&environment=production", networkKey, nil, 403)
	body["environment"] = "production"
	call("POST", "/virtual-networks", networkKey, body, 403)
	body["environment"] = "development"
	body["project"] = "another"
	call("POST", "/virtual-networks", networkKey, body, 403)
	body["project"] = "demo"
	body["expected_revision"] = 1
	body["expected_id"] = saved.ID
	call("PUT", "/virtual-networks/shared", networkKey, body, 200)
	call("DELETE", "/virtual-networks/shared", networkKey, map[string]any{"project": "demo", "environment": "development", "expected_id": saved.ID, "expected_revision": 2, "confirmation": "shared"}, 200)
}

func TestVirtualNetworkAPIRejectsRecreatedReview(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	token, err := db.Bootstrap(ctx, "network-review-operator")
	if err != nil {
		t.Fatal(err)
	}
	handler := (&api.Server{Store: db}).Handler()
	call := func(method, path string, body any, expected int) []byte {
		t.Helper()
		request := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(string(store.JSON(body))))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != expected {
			t.Fatalf("%s %s: want %d, got %d %s", method, path, expected, response.Code, response.Body.String())
		}
		return response.Body.Bytes()
	}
	configuration := func(description string) string {
		return "name='commerce'\ndescription='" + description + "'\n[segments.data]\napplications=[]"
	}
	create := func(description string) string {
		t.Helper()
		raw := call("POST", "/virtual-networks", map[string]any{"project": "demo", "environment": "development", "expected_revision": 0, "toml": configuration(description)}, 201)
		var result struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &result) != nil || len(result.ID) != 32 {
			t.Fatal("created network is missing its ID")
		}
		return result.ID
	}
	remove := func(id string, revision int) map[string]any {
		return map[string]any{"project": "demo", "environment": "development", "expected_id": id, "expected_revision": revision, "confirmation": "commerce"}
	}
	originalID := create("Original")
	draft := map[string]any{"project": "demo", "environment": "development", "expected_id": originalID, "expected_revision": 1, "toml": configuration("Reviewed edit")}
	plan := call("POST", "/virtual-networks/plan", draft, 200)
	var review struct {
		ExpectedID       string `json:"expected_id"`
		ExpectedRevision int64  `json:"expected_revision"`
	}
	if json.Unmarshal(plan, &review) != nil || review.ExpectedID != originalID || review.ExpectedRevision != 1 {
		t.Fatal("review did not preserve the network identity and revision")
	}
	call("DELETE", "/virtual-networks/commerce", remove(originalID, 1), 200)
	call("PUT", "/virtual-networks/commerce", draft, 409)
	call("POST", "/virtual-networks/plan", draft, 409)
	recreatedID := create("Recreated")
	if recreatedID == originalID {
		t.Fatal("recreated network reused its prior identity")
	}
	call("PUT", "/virtual-networks/commerce", draft, 409)
	call("DELETE", "/virtual-networks/commerce", remove(originalID, 1), 409)
	call("POST", "/virtual-networks/plan", draft, 409)
	raw := call("GET", "/virtual-networks/commerce?project=demo&environment=development", nil, 200)
	var detail struct {
		Network struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		} `json:"network"`
		Spec spec.VirtualNetwork `json:"spec"`
	}
	if json.Unmarshal(raw, &detail) != nil || detail.Network.ID != recreatedID || detail.Network.Revision != 1 || detail.Spec.Description != "Recreated" {
		t.Fatal("stale requests changed the recreated network")
	}
	delete(draft, "expected_id")
	call("PUT", "/virtual-networks/commerce", draft, 400)
	call("POST", "/virtual-networks/plan", draft, 400)
	call("DELETE", "/virtual-networks/commerce", remove("", 1), 400)
	draft["expected_id"] = recreatedID
	plan = call("POST", "/virtual-networks/plan", draft, 200)
	if json.Unmarshal(plan, &review) != nil || review.ExpectedID != recreatedID {
		t.Fatal("new review did not select the recreated network")
	}
	call("PUT", "/virtual-networks/commerce", draft, 200)
	call("PUT", "/virtual-networks/commerce", draft, 409)
	call("POST", "/virtual-networks/plan", draft, 409)
	call("DELETE", "/virtual-networks/commerce", remove(recreatedID, 1), 409)
	call("DELETE", "/virtual-networks/commerce", remove(recreatedID, 2), 200)
	reserved := map[string]any{"project": "demo", "environment": "development", "expected_revision": 0, "toml": "name='new'\n[segments.data]\napplications=[]"}
	call("POST", "/virtual-networks/plan", reserved, 400)
	call("POST", "/virtual-networks", reserved, 400)
}
