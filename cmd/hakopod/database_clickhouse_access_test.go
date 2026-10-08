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

	"github.com/hakopod/hakopod/internal/database"
)

func TestDatabaseCLIPreservesClickHouseAccessProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.toml")
	if err := os.WriteFile(path, []byte(`schema_version = 1
name = "tenant-analytics"
engine = "clickhouse"
version = "26.3"
mode = "standalone"
replicas = 0
shards = 1
cpu = "500m"
memory = "2Gi"
storage_gib = 2
[tls]
mode = "required"
[clickhouse]
access_profile = "tenant_admin"
`), 0600); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	for _, action := range []string{"create", "resize-plan", "resize"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body struct {
					Spec             database.Spec `json:"spec"`
					ReviewID         string        `json:"review_id"`
					ExpectedRevision int64         `json:"expected_revision"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				expected := "/api/v1/databases"
				if action != "create" {
					expected += "/" + id + "/" + action
				}
				if r.Method != "POST" || r.URL.Path != expected || !body.Spec.ClickHouseTenantAdmin() {
					t.Error("CLI did not preserve the reviewed ClickHouse access profile")
				}
				if action == "resize" && (body.ReviewID != "profile-review" || body.ExpectedRevision != 3) {
					t.Error("CLI did not preserve the review or revision")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"operation-fixture"}`))
			}))
			defer server.Close()
			args := []string{action}
			if action != "create" {
				args = append(args, id)
			}
			c := &client{url: server.URL, http: server.Client()}
			if err := databaseCommand(context.Background(), c, "demo", "development", args, path, "profile-fixture", "profile-review", "", "", 3, databaseConnectionFlags{}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("CLI did not submit exactly one request")
			}
		})
	}
}
