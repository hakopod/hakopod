package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/hakopod/hakopod/internal/agent"
	"github.com/hakopod/hakopod/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPAndStdioInitializationInstructionsMatch(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		scope      agent.Scope
		options    agent.Options
		p          store.Principal
	}{
		{"project", "?project=demo&environment=development&allow_deploy=true", agent.Scope{Project: "demo", Environment: "development"}, agent.Options{AllowDeploy: true}, store.Principal{Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "deployments:write"}}},
		{"installation", "?installation=true&allow_admin=true&allow_credentials=true", agent.Scope{}, agent.Options{Installation: true, AllowAdmin: true, AllowCredentials: true}, store.Principal{Permissions: []string{"admin", "agent:admin", "agent:credentials"}}},
		{"host", "?host_only=true&allow_host_terminal=true", agent.Scope{}, agent.Options{HostOnly: true, AllowHostTerminal: true}, store.Principal{Permissions: []string{"nodes:terminal"}, HostPermissions: []store.HostPermission{{Node: "fixture-node", Permission: "nodes:terminal"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.p
			p.Admin = true
			p.ID = "fixture"
			p.KeyID = "fixture-key"
			p.Email = "fixture@example.invalid"
			p.CredentialType = "cli"
			input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
			r := httptest.NewRequest("POST", "/api/v1/mcp"+tc.path, strings.NewReader(input))
			r.Header.Set("Authorization", "Bearer fixture")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Accept", "application/json, text/event-stream")
			r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
			w := httptest.NewRecorder()
			(&Server{}).mcp(w, r, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("initialization dispatched resource operation") }))
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var httpResult, stdioResult struct {
				Result struct {
					Instructions string `json:"instructions"`
				} `json:"result"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &httpResult); err != nil {
				t.Fatal(err)
			}
			requester := func(_ context.Context, _, _ string, _ any, _ string, out any) error {
				raw, _ := json.Marshal(map[string]any{"email": p.Email, "credential_type": "cli", "permissions": p.Permissions})
				return json.Unmarshal(raw, out)
			}
			var output bytes.Buffer
			if err := agent.ServeWithStream(context.Background(), requester, tc.scope, tc.options, nil, strings.NewReader(input+"\n"), &output, "test"); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(output.Bytes(), &stdioResult); err != nil {
				t.Fatal(err)
			}
			if httpResult.Result.Instructions == "" || httpResult.Result.Instructions != stdioResult.Result.Instructions {
				t.Fatal("transport instructions differ")
			}
		})
	}
}
