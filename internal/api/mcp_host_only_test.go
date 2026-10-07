package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/store"
)

func TestHTTPMCPHostOnlyIsolation(t *testing.T) {
	const endpoint = "/api/v1/mcp?host_only=true&allow_host_terminal=true"
	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
	base := store.Principal{ID: "human", KeyID: "key", Email: "fixture@example.invalid", CredentialType: "cli", Permissions: []string{"nodes:terminal"}, HostPermissions: []store.HostPermission{{Node: "owned-node", Permission: "nodes:terminal"}}}
	server := &Server{}
	dispatched := 0
	routes := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dispatched++
		problem(w, 403, "forbidden", "The canonical route denied this operation.")
	})
	call := func(p store.Principal, path, session, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer fixture")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			r.Header.Set("Mcp-Session-Id", session)
			r.Header.Set("MCP-Protocol-Version", "2025-06-18")
		}
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
		w := httptest.NewRecorder()
		server.mcp(w, r, routes)
		return w
	}
	for _, suffix := range []string{"&project=demo", "&environment=development", "&application=app", "&installation=true", "&allow_admin=true", "&allow_credentials=true", "&allow_write=true", "&allow_deploy=true", "&allow_exec=true", "&allow_sql=true", "&allow_sql_write=true", "&allow_terminal=true", "&host_only=true"} {
		w := call(base, endpoint+suffix, "", initialize)
		if w.Code != 400 {
			t.Errorf("conflicting option %s: status %d", suffix, w.Code)
		}
	}
	if w := call(base, "/api/v1/mcp?host_only=true", "", initialize); w.Code != 400 {
		t.Fatal("host-only session accepted without terminal opt-in")
	}
	for _, test := range []struct {
		name string
		edit func(*store.Principal)
	}{
		{"missing grant", func(p *store.Principal) { p.Permissions = []string{"admin"} }},
		{"MFA required", func(p *store.Principal) { p.MFARequired = true }},
		{"project restriction", func(p *store.Principal) { p.Project = "demo" }},
		{"environment restriction", func(p *store.Principal) { p.Environment = "development" }},
		{"application restriction", func(p *store.Principal) { p.Application = "app" }},
		{"identity restriction", func(p *store.Principal) { p.IdentityProject = "demo" }},
		{"browser credential", func(p *store.Principal) { p.CredentialType = "browser" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := base
			test.edit(&p)
			if w := call(p, endpoint, "", initialize); w.Code != 403 {
				t.Fatalf("restricted credential accepted: %d", w.Code)
			}
		})
	}
	for _, credential := range []string{"cli", "machine"} {
		p := base
		p.CredentialType = credential
		w := call(p, endpoint, "", initialize)
		if w.Code != 200 || w.Header().Get("Mcp-Session-Id") == "" {
			t.Fatalf("explicit %s host credential rejected: %d %s", credential, w.Code, w.Body.String())
		}
		session := w.Header().Get("Mcp-Session-Id")
		w = call(p, endpoint, session, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		var list struct {
			Result struct {
				Tools []struct{ Name string } `json:"tools"`
			} `json:"result"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Result.Tools) == 0 {
			t.Fatal("host tools unavailable")
		}
		for _, tool := range list.Result.Tools {
			if !strings.HasPrefix(tool.Name, "host_terminal_") {
				t.Fatalf("host-only session exposed %s", tool.Name)
			}
		}
		for _, name := range []string{"applications", "api_operations", "api_read", "api_apply", "execute_command", "query_database"} {
			body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`
			w = call(p, endpoint, session, body)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"isError":true`) {
				t.Fatalf("host-only session accepted %s", name)
			}
		}
		p.Permissions = nil
		if w = call(p, endpoint, session, `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`); w.Code != 403 {
			t.Fatal("session retained removed host credential grant")
		}
	}
	if dispatched != 0 {
		t.Fatal("host-only session dispatched a non-host operation")
	}
}
