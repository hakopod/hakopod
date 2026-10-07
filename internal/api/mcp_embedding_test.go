package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScopedMCPEmbeddingDispatchAndBinding(t *testing.T) {
	scope := MCPScope{Identity: "user", KeyID: "key", Binding: "workspace-one/node-one", Project: "demo", Environment: "development", Permissions: []string{"deployments:read"}}
	revoked := false
	calls := 0
	routes := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("X-Hakopod-Workspace") != "workspace-one" {
			t.Error("canonical authentication headers lost")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-Untrusted") != "" || r.Header.Get("Mcp-Session-Id") != "" {
			t.Error("unrelated headers forwarded")
		}
		if r.URL.Path != "/api/v1/applications" {
			t.Errorf("unexpected canonical path %s", r.URL.Path)
		}
		problem(w, 403, "forbidden", "The canonical route denied this operation.")
	})
	handler := NewScopedMCPHandler(routes, "https://cloud.example.test", func(*http.Request) (MCPScope, error) {
		if revoked {
			return MCPScope{}, errors.New("revoked")
		}
		return scope, nil
	})
	call := func(session, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/mcp?project=demo&environment=development", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer fixture")
		r.Header.Set("X-Hakopod-Workspace", "workspace-one")
		r.Header.Set("X-Untrusted", "do not forward")
		r.Header.Set("Cookie", "private=cookie")
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
	init := call("", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if init.Code != 200 {
		t.Fatal(init.Code, init.Body.String())
	}
	session := init.Header().Get("Mcp-Session-Id")
	w := call(session, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"applications","arguments":{}}}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "canonical route denied") || calls != 1 {
		t.Fatal("canonical policy bypassed", w.Code, w.Body.String(), calls)
	}
	scope.Binding = "workspace-two/node-two"
	w = call(session, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	if w.Code != 404 {
		t.Fatal("same-name workspace reused session", w.Code)
	}
	scope.Binding = "workspace-one/node-one"
	revoked = true
	w = call(session, `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
	if w.Code != 403 {
		t.Fatal("revoked credential reused session", w.Code)
	}
}
