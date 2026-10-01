package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDatabaseNodesCLIUsesExplicitScope(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/api/v1/database-placement/nodes" || r.URL.Query().Get("project") != "orders" || r.URL.Query().Get("environment") != "staging" || len(r.URL.Query()) != 2 {
			t.Errorf("wrong placement request: %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"limit":48}`))
	}))
	defer server.Close()
	c := &client{url: server.URL, http: server.Client()}
	if err := databaseCommand(context.Background(), c, "orders", "staging", []string{"nodes"}, "", "", "", "", "", 0, databaseConnectionFlags{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		project, environment string
		args                 []string
	}{
		{"", "staging", []string{"nodes"}},
		{"orders", "", []string{"nodes"}},
		{"orders", "staging", []string{"nodes", "unexpected-id"}},
	} {
		if err := databaseCommand(context.Background(), c, tc.project, tc.environment, tc.args, "", "", "", "", "", 0, databaseConnectionFlags{}); err == nil {
			t.Error("invalid discovery scope reached API")
		}
	}
	if calls != 1 {
		t.Fatalf("got %d requests", calls)
	}
}
