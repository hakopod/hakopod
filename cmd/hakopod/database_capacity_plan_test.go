package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDatabaseCapacityPlanCLIUsesScopeAndOptionalDatabase(t *testing.T) {
	id := strings.Repeat("a", 32)
	file := filepath.Join(t.TempDir(), "database.toml")
	if err := os.WriteFile(file, []byte("schema_version = 1\nname = \"orders\"\nengine = \"postgresql\"\nversion = \"17\"\nmode = \"standalone\"\nreplicas = 0\nshards = 1\ncpu = \"250m\"\nmemory = \"512Mi\"\nstorage_gib = 5\n\n[tls]\nmode = \"required\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/database-capacity-plan" {
			t.Errorf("wrong planning request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["project"] != "orders" || body["environment"] != "staging" || body["database_id"] != id {
			t.Errorf("wrong planning scope: %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"advisory": true})
	}))
	defer server.Close()
	c := &client{url: server.URL, http: server.Client()}
	if err := databaseCommand(context.Background(), c, "orders", "staging", []string{"capacity-plan", id}, file, "", "", "", "", 0, databaseConnectionFlags{}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("got %d planning requests", calls)
	}
}
