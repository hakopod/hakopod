package api_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	managed "github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

func TestDatabaseExplorerCatalogScopeAndCurrentGrants(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	bootstrap, err := db.Bootstrap(ctx, "explorer-catalog")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.Authenticate(ctx, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO environments(project,name) VALUES('demo','production')"); err != nil {
		t.Fatal(err)
	}
	key := func(name, environment string, permissions []string) string {
		_, raw, err := db.CreateKey(ctx, owner, store.KeyInput{Name: name, Project: "demo", Environment: environment, Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	reader := key("reader", "development", []string{"deployments:read", "databases:query"})
	writer := key("writer", "development", []string{"deployments:read", "databases:query", "databases:write-query"})
	viewer := key("viewer", "development", []string{"deployments:read"})
	foreign := key("foreign", "production", []string{"deployments:read", "databases:query"})
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	for _, engine := range []string{"postgresql", "mysql", "mongodb", "clickhouse", "oracle", "redis", "vitess", "duckdb"} {
		d := managed.Resource{ID: store.NewID(), Spec: managed.Spec{SchemaVersion: 1, Name: "fixture-" + engine, Engine: engine, TLS: &managed.TLSConfig{Mode: "required"}}, Observation: managed.Observation{Status: "ready", Revision: 1, ObservedAt: now, TLS: &managed.TLSObservation{Verified: true, PlaintextRejected: true, ExpiresAt: &expires}, Endpoints: []managed.Endpoint{{Purpose: "read_write", Host: "private-db.example.test", Port: 5432}}}}
		if engine == "mongodb" {
			d.Observation.Endpoints[0].Purpose = "cluster"
		}
		if engine == "clickhouse" {
			d.Observation.Endpoints[0].Purpose = "https"
		}
		if _, err := db.Pool.Exec(ctx, `INSERT INTO managed_databases(id,project,environment,name,revision,spec,status,credentials,observation) VALUES($1,'demo','development',$2,1,$3,'ready',$4,$5)`, d.ID, d.Spec.Name, store.JSON(d.Spec), []byte("credential-fixture"), store.JSON(d.Observation)); err != nil {
			t.Fatal(err)
		}
	}
	handler := (&api.Server{Store: db}).Handler()
	call := func(token, query string) (int, managed.ExplorerCatalog) {
		r := httptest.NewRequest("GET", "/api/v1/database-explorer/connections"+query, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		for _, secret := range []string{"credential-fixture", "private-db.example.test", "password"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("catalog leaked private data")
			}
		}
		var result managed.ExplorerCatalog
		if w.Code == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("catalog may be cached")
			}
		}
		return w.Code, result
	}
	query := "?project=demo&environment=development"
	for _, token := range []string{bootstrap, viewer, foreign} {
		if code, _ := call(token, query); code != 404 {
			t.Fatal("ungranted catalog access", code)
		}
	}
	for _, q := range []string{"", "?project=demo", "?project=demo&environment=development&environment=production", "?project=demo&environment=development&extra=true", "?project=demo&environment=development&project=%zz"} {
		if code, _ := call(reader, q); code != 400 {
			t.Fatal("scope fallback accepted", code)
		}
	}
	for _, tc := range []struct {
		token string
		write bool
	}{{reader, false}, {writer, true}} {
		code, result := call(tc.token, query)
		if code != 200 || len(result.Items) != 5 || result.Project != "demo" || result.Environment != "development" {
			t.Fatal("wrong catalog scope or engines", code, result)
		}
		for _, item := range result.Items {
			if item.State != "eligible" || item.CanWrite != tc.write {
				t.Fatal("wrong source permission", item)
			}
		}
	}
	// A new source revision must lose eligibility until its observation catches up.
	if _, err = db.Pool.Exec(ctx, "UPDATE managed_databases SET revision=2 WHERE name='fixture-postgresql'"); err != nil {
		t.Fatal(err)
	}
	_, changed := call(writer, query)
	for _, item := range changed.Items {
		if item.Engine == "postgresql" && (item.Revision != 2 || item.Reason != "observation_stale" || item.CanWrite) {
			t.Fatal("old observation remained eligible", item)
		}
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE managed_databases SET deleted_at=now() WHERE name='fixture-postgresql'"); err != nil {
		t.Fatal(err)
	}
	if code, result := call(writer, query); code != 200 || len(result.Items) != 4 {
		t.Fatal("removed source remained in catalog", code, result)
	}
	p, err := db.Authenticate(ctx, reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", p.KeyID); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(reader, query); code != 401 {
		t.Fatal("revoked key retained access", code)
	}
}
