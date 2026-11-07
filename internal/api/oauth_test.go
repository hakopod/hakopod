package api

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestMCPResourceSelectsProjectAndPermissions(t *testing.T) {
	s := &Server{Auth: AuthConfig{PublicURL: "https://hako.example.com/"}}
	project, environment, permissions, err := s.mcpResource("https://hako.example.com/api/v1/mcp?project=demo&environment=production")
	if err != nil || project != "demo" || environment != "production" || !slices.Equal(permissions, []string{"deployments:read", "logs:read"}) {
		t.Fatal(project, environment, permissions, err)
	}
	_, _, permissions, err = s.mcpResource("https://hako.example.com/api/v1/mcp?project=demo&environment=production&allow_deploy=true")
	if err != nil || !slices.Contains(permissions, "deployments:write") {
		t.Fatal(permissions, err)
	}
	for _, refused := range []string{
		"https://other.example.com/api/v1/mcp?project=demo&environment=production",
		"http://hako.example.com/api/v1/mcp?project=demo&environment=production",
		"https://hako.example.com/api/v1/other?project=demo&environment=production",
		"https://hako.example.com/api/v1/mcp",
		"https://hako.example.com/api/v1/mcp?project=demo&environment=production&allow_exec=true",
		"https://hako.example.com/api/v1/mcp?project=demo&environment=production&installation=true",
		"https://hako.example.com/api/v1/mcp?project=demo&environment=production&foo=1",
		"https://hako.example.com/api/v1/mcp?project=demo&project=x&environment=production",
		"https://hako.example.com/api/v1/mcp?project=demo&environment=production#x",
	} {
		if _, _, _, err := s.mcpResource(refused); err == nil {
			t.Error("accepted", refused)
		}
	}
	if _, _, _, err := s.mcpResource("https://hako.example.com/api/v1/mcp?project=demo&environment=production&allow_sql=true"); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Error("refusal does not point to API keys", err)
	}
}

func TestOAuthDiscovery(t *testing.T) {
	handler := (&Server{Auth: AuthConfig{PublicURL: "https://hako.example.com"}}).Handler()
	get := func(path string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		var body map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatal(path, w.Code, w.Body.String())
		}
		return body
	}
	server := get("/.well-known/oauth-authorization-server")
	if server["issuer"] != "https://hako.example.com" || server["token_endpoint"] != "https://hako.example.com/api/v1/oauth/token" || server["registration_endpoint"] != "https://hako.example.com/api/v1/oauth/register" || server["authorization_endpoint"] != "https://hako.example.com/login/authorize" {
		t.Fatal(server)
	}
	if methods, _ := server["code_challenge_methods_supported"].([]any); len(methods) != 1 || methods[0] != "S256" {
		t.Fatal(server)
	}
	resource := get("/.well-known/oauth-protected-resource/api/v1/mcp?project=demo&environment=production")
	if resource["resource"] != "https://hako.example.com/api/v1/mcp?environment=production&project=demo" {
		t.Fatal(resource)
	}
	if servers, _ := resource["authorization_servers"].([]any); len(servers) != 1 || servers[0] != "https://hako.example.com" {
		t.Fatal(resource)
	}

	// An unauthenticated MCP request points the client at the metadata.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/mcp?project=demo&environment=production", nil))
	if w.Code != 401 || w.Header().Get("WWW-Authenticate") != `Bearer resource_metadata="https://hako.example.com/.well-known/oauth-protected-resource/api/v1/mcp?environment=production&project=demo"` {
		t.Fatal(w.Code, w.Header())
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/projects", nil))
	if w.Code != 401 || w.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("challenge outside MCP", w.Code, w.Header())
	}

	// Without a public URL there is no issuer to advertise.
	handler = (&Server{}).Handler()
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
