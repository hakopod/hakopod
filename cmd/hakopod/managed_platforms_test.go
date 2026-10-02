package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/pelletier/go-toml/v2"
)

// This fixture exercises the CLI contract without representing native runtime state.
func managedPlatformCLIFixture() managedplatform.Spec {
	resources := map[string]managedplatform.Resources{}
	for _, name := range managedplatform.SupabaseComponentNames() {
		resources[name] = managedplatform.Resources{CPU: "250m", Memory: "512Mi"}
	}
	resources["database"] = managedplatform.Resources{CPU: "500m", Memory: "2Gi"}
	secrets := map[string]managedplatform.SecretReference{}
	for _, name := range managedplatform.SupabaseRequiredSecretKeys() {
		secrets[name] = managedplatform.SecretReference{Name: "supabase-" + name, Revision: 1}
	}
	return managedplatform.Spec{
		SchemaVersion: 1, Name: "customer-supabase", Kind: "supabase", Version: managedplatform.SupabaseVersion,
		Resources: resources, Secrets: secrets,
		Storage:   map[string]int64{"database": 20, "database-encryption": 1, "edge-functions": 1, "objects": 20, "studio-snippets": 1},
		Placement: managedplatform.Placement{NodeNames: []string{"fixture-worker"}},
		Supabase:  &managedplatform.SupabaseConfig{PublicURL: "https://data.example.test", SiteURL: "https://app.example.test", RedirectURLs: []string{"https://app.example.test"}, DatabaseName: "postgres", JWTExpirySeconds: 3600, RESTMaxRows: 1000, StorageFileLimitBytes: 50 << 20, PoolSize: 20, PoolMaxClients: 100},
	}
}

func TestManagedPlatformCLIRepeatedUpdatesBindRetryKeyToRevision(t *testing.T) {
	spec := managedPlatformCLIFixture()
	raw, err := toml.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	path := externalCLIFile(t, "platform.toml", string(raw))
	id := strings.Repeat("a", 32)
	revision := int64(4)
	keys := []string{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && r.URL.Path == "/api/v1/managed-platforms/"+id {
			_ = json.NewEncoder(w).Encode(store.ManagedPlatform{ID: id, Project: "loaded-project", Environment: "staging", Revision: revision, Spec: spec})
			return
		}
		var intent struct {
			ID               string               `json:"id"`
			Project          string               `json:"project"`
			Environment      string               `json:"environment"`
			ExpectedRevision int64                `json:"expected_revision"`
			Kind             string               `json:"kind"`
			ConfirmName      string               `json:"confirm_name"`
			Spec             managedplatform.Spec `json:"spec"`
		}
		if json.NewDecoder(r.Body).Decode(&intent) != nil || intent.ID != id || intent.Project != "loaded-project" || intent.Environment != "staging" || intent.ExpectedRevision != revision || intent.Kind != "update" || intent.ConfirmName != spec.Name || !reflect.DeepEqual(intent.Spec, spec) {
			t.Error("update lost the fetched scope, revision, name or complete spec")
		}
		switch r.URL.Path {
		case "/api/v1/managed-platforms/reviews":
			_, _ = w.Write([]byte(`{"blocked":false,"review":{"id":"fixture-review"}}`))
		case "/api/v1/managed-platforms/operations":
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			_, _ = w.Write([]byte(`{"id":"fixture-operation"}`))
		default:
			t.Error("unexpected platform request")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c := &client{url: server.URL, http: server.Client()}
	for _, current := range []int64{4, 4, 5} {
		revision = current
		_, err := captureExternalCLI(t, func() error {
			return managedPlatformCommand(context.Background(), c, "stale-project", "development", []string{"update", id}, path, "", "")
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"platform-update-" + id + "-r5", "platform-update-" + id + "-r5", "platform-update-" + id + "-r6"}
	if calls != 9 || !reflect.DeepEqual(keys, want) {
		t.Fatalf("retry/update keys changed incorrectly: %v", keys)
	}
}

func TestManagedPlatformCLICatalogRequiresExactScope(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/api/v1/managed-platforms/catalog" || r.URL.Query().Get("project") != "selected" || r.URL.Query().Get("environment") != "staging" || len(r.URL.Query()) != 2 {
			t.Error("catalog did not preserve the explicitly selected scope")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"project":"selected","environment":"staging","storage_class":"encrypted","nodes":[],"secret_references":[],"items":[]}`))
	}))
	defer server.Close()
	c := &client{url: server.URL, http: server.Client()}
	if _, err := captureExternalCLI(t, func() error {
		return managedPlatformCommand(context.Background(), c, "selected", "staging", []string{"catalog"}, "", "", "")
	}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		project, environment string
		args                 []string
	}{
		{"", "", []string{"catalog"}},
		{"selected", "", []string{"catalog"}},
		{"", "staging", []string{"catalog"}},
		{"selected", "staging", []string{"catalog", strings.Repeat("a", 32)}},
	} {
		if err := managedPlatformCommand(context.Background(), c, test.project, test.environment, test.args, "", "", ""); err == nil {
			t.Fatal("catalog accepted a missing scope or resource ID")
		}
	}
	if calls != 1 {
		t.Fatal("invalid catalog input reached the server")
	}
}
