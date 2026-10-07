package api_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	managed "github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

func TestDatabaseQueryRequiresScopedGrantAndReviewedRevision(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	bootstrap, err := db.Bootstrap(ctx, "query-revision")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.Authenticate(ctx, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	key := func(name string, permissions []string) string {
		_, raw, err := db.CreateKey(ctx, owner, store.KeyInput{Name: name, Project: "demo", Environment: "development", Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	reader := key("query-reader", []string{"databases:query"})
	writer := key("query-writer", []string{"databases:query", "databases:write-query"})
	id := store.NewID()
	spec := managed.Spec{SchemaVersion: 1, Name: "query-revision-fixture", Engine: "postgresql", Version: "17", Mode: "standalone", Replicas: 1, CPU: "100m", Memory: "256Mi", StorageGiB: 1}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO managed_databases(id,project,environment,name,revision,spec,status,credentials) VALUES($1,'demo','development',$2,3,$3,'ready',$4)`, id, spec.Name, store.JSON(spec), []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	handler := (&api.Server{Store: db}).Handler()
	for _, tc := range []struct {
		name, token, body string
		status            int
		code              string
	}{
		{"wildcard cannot query", bootstrap, `{"sql":"SELECT 1"}`, 404, "not_found"},
		{"reader cannot write", reader, `{"sql":"SELECT 1","read_only":false,"expected_revision":3}`, 404, "not_found"},
		{"write requires revision", writer, `{"sql":"SELECT 1","read_only":false}`, 409, "database_query_revision_conflict"},
		{"stale write revision", writer, `{"sql":"SELECT 1","read_only":false,"expected_revision":2}`, 409, "database_query_revision_conflict"},
		{"reviewed write reaches cluster boundary", writer, `{"sql":"SELECT 1","read_only":false,"expected_revision":3}`, 503, "database_query_unavailable"},
		{"read reaches cluster boundary", reader, `{"sql":"SELECT 1"}`, 503, "database_query_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v1/databases/"+id+"/query", strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+tc.token)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), fmt.Sprintf(`"code":"%s"`, tc.code)) {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
		})
	}
	var count int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='database.query'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected query started: count=%d err=%v", count, err)
	}
}
