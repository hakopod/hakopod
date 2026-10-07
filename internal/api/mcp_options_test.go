package api_test

import (
	"context"
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPMCPExecutionOptionsAndManagedRuntime(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	bootstrap, err := db.Bootstrap(ctx, "mcp-options")
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
	read := key("read-options", []string{"deployments:read"})
	full := key("full-options", []string{"deployments:read", "pods:exec", "databases:query", "databases:write-query"})
	query := key("query-options", []string{"deployments:read", "databases:query"})
	// A managed tenant runtime retains MCP. Only the shared control plane disables it.
	handler := (&api.Server{Store: db, Auth: api.AuthConfig{DeploymentMode: cluster.DeploymentManagedCloud}}).Handler()
	const endpoint = "/api/v1/mcp?project=demo&environment=development"
	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
	call := func(path, token, session, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			r.Header.Set("Mcp-Session-Id", session)
			r.Header.Set("MCP-Protocol-Version", "2025-06-18")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	expect := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("got%d want%d: %s", w.Code, status, w.Body.String())
		}
	}
	for _, flags := range []string{"&allow_write=1", "&allow_exec=TRUE", "&allow_sql=true&allow_sql=false", "&allow_sql_write=true"} {
		expect(call(endpoint+flags, full, "", initialize), 400)
	}
	for _, flags := range []string{"&allow_exec=true", "&allow_sql=true", "&allow_sql=true&allow_sql_write=true"} {
		expect(call(endpoint+flags, read, "", initialize), 403)
	}
	expect(call(endpoint+"&allow_sql=true&allow_sql_write=true", query, "", initialize), 403)
	flags := "&allow_write=true&allow_exec=true&allow_sql=true&allow_sql_write=true"
	init := call(endpoint+flags, full, "", initialize)
	expect(init, 200)
	session := init.Header().Get("Mcp-Session-Id")
	if session == "" {
		t.Fatal("managed runtime omitted MCP session")
	}
	list := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	tools := call(endpoint+flags, full, session, list)
	expect(tools, 200)
	for _, name := range []string{"pod_exec", "database_query", "api_call"} {
		if !strings.Contains(tools.Body.String(), `"name":"`+name+`"`) {
			t.Fatal(tools.Body.String())
		}
	}
	for _, changed := range []string{"&allow_write=false&allow_exec=true&allow_sql=true&allow_sql_write=true", "&allow_write=true&allow_exec=false&allow_sql=true&allow_sql_write=true", "&allow_write=true&allow_exec=true&allow_sql=false&allow_sql_write=false", "&allow_write=true&allow_exec=true&allow_sql=true&allow_sql_write=false"} {
		expect(call(endpoint+changed, full, session, list), 404)
	}
	// A valid read-only SQL connection does not imply execution or SQL writes.
	init = call(endpoint+"&allow_sql=true", query, "", initialize)
	expect(init, 200)
	tools = call(endpoint+"&allow_sql=true", query, init.Header().Get("Mcp-Session-Id"), list)
	expect(tools, 200)
	if strings.Contains(tools.Body.String(), `"name":"pod_exec"`) || !strings.Contains(tools.Body.String(), `"name":"database_query"`) {
		t.Fatal(tools.Body.String())
	}
}
