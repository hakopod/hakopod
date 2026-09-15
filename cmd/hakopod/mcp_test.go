package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestAgentReadOnlyProtocol(t *testing.T) {
	input := strings.Join([]string{`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"deploy","arguments":{"plan_id":"bad"}}}`}, "\n")
	var out bytes.Buffer
	if err := serveAgent(context.Background(), nil, config{Project: "demo", Environment: "development"}, false, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatal(out.String())
	}
	if !strings.Contains(lines[0], "Initialize first") || strings.Contains(lines[2], `"name":"deploy"`) || !strings.Contains(lines[3], "deployment tools are disabled") {
		t.Fatal(out.String())
	}
	if err := serveAgent(context.Background(), nil, config{}, false, strings.NewReader(input), &out); err == nil {
		t.Fatal("unscoped agent accepted")
	}
}

func TestAgentDetectsFrameworkWithoutRepositoryAccess(t *testing.T) {
	agent := agentServer{}
	input := map[string]any{"files": map[string]string{"package.json": `{"scripts":{"build":"astro build"},"dependencies":{"astro":"7.3.2"}}`, "package-lock.json": ""}}
	raw, _ := json.Marshal(input)
	result, err := agent.call(context.Background(), "detect_framework", raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if !bytes.Contains(encoded, []byte(`"framework":"astro"`)) || !bytes.Contains(encoded, []byte("sha256:")) {
		t.Fatal(string(encoded))
	}
	for _, file := range []string{"../../package.json", "package.json\nsecret", "sub/package.json"} {
		raw, _ = json.Marshal(map[string]any{"files": map[string]string{file: "{}"}})
		if _, err := agent.call(context.Background(), "detect_framework", raw); err == nil {
			t.Fatal("unsafe metadata path accepted", file)
		}
	}
}
func TestAgentReviewedDeploymentAndScope(t *testing.T) {
	ctx := context.Background()
	var accepted [][]byte
	var keys []string
	app := store.Application{ID: "app-one", Project: "demo", Environment: "development", Spec: spec.Application{Name: "demo", Services: map[string]spec.Service{"web": {Image: "nginx:alpine"}}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped-test-key" {
			t.Error("missing API authorization")
		}
		switch r.URL.Path {
		case "/api/v1/plan":
			json.NewEncoder(w).Encode(map[string]any{"spec": app.Spec, "expected_revision": 7, "changes": []any{}, "warnings": []any{}})
		case "/api/v1/deployments":
			var body json.RawMessage
			json.NewDecoder(r.Body).Decode(&body)
			accepted = append(accepted, body)
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			json.NewEncoder(w).Encode(store.Deployment{ID: "deployment-one", ApplicationID: app.ID})
		case "/api/v1/applications/app-one":
			json.NewEncoder(w).Encode(app)
		default:
			t.Error("unexpected path", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c, err := newClient(config{URL: server.URL, Key: "scoped-test-key"})
	if err != nil {
		t.Fatal(err)
	}
	agent := agentServer{client: c, cfg: config{Project: "demo", Environment: "development"}, allowDeploy: true, plans: map[string]agentPlan{}}
	body, _ := json.Marshal(map[string]string{"toml": "schema_version=1\nname=\"demo\"\n[services.web]\nimage=\"nginx:alpine\"\n"})
	result, err := agent.call(ctx, "plan", body)
	if err != nil {
		t.Fatal(err)
	}
	id := result.(map[string]any)["plan_id"].(string)
	args, _ := json.Marshal(map[string]string{"plan_id": id})
	for i := 0; i < 2; i++ {
		if _, err = agent.call(ctx, "deploy", args); err != nil {
			t.Fatal(err)
		}
	}
	if len(accepted) != 2 || keys[0] != keys[1] || !bytes.Equal(accepted[0], accepted[1]) || !bytes.Contains(accepted[0], []byte(`"expected_revision":7`)) {
		t.Fatal("retry changed reviewed deployment")
	}
	p := agent.plans[id]
	p.Expires = time.Now().Add(-time.Second)
	agent.plans[id] = p
	if _, err = agent.call(ctx, "deploy", args); err == nil {
		t.Fatal("expired plan accepted")
	}
	if _, err = agent.call(ctx, "application", []byte(`{"id":"app-one","shell":"sh"}`)); err == nil {
		t.Fatal("unknown arguments accepted")
	}
	app.Project = "other"
	if _, err = agent.call(ctx, "application", []byte(`{"id":"app-one"}`)); err == nil {
		t.Fatal("cross-scope application disclosed")
	}
	if _, err = agent.call(ctx, "application", []byte(`{"id":"../keys"}`)); err == nil {
		t.Fatal("path traversal accepted")
	}
}
