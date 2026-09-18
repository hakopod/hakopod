package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestHTTPMCPAuthenticationScopeAndReviewedDeployment(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	bootstrap, err := db.Bootstrap(ctx, "mcp")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.Authenticate(ctx, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	newKey := func(name string, write bool) (store.Key, string) {
		permissions := []string{"deployments:read"}
		if write {
			permissions = append(permissions, "deployments:write")
		}
		key, raw, err := db.CreateKey(ctx, owner, store.KeyInput{Name: name, Project: "demo", Environment: "development", Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return key, raw
	}
	key, raw := newKey("mcp-read", false)
	_, writer := newKey("mcp-write", true)
	_, other := newKey("mcp-other", false)
	server := &api.Server{Store: db, Auth: api.AuthConfig{PublicURL: "https://dashboard.example.com"}}
	handler := server.Handler()
	const endpoint = "/api/v1/mcp?project=demo&environment=development"
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	call := func(method, path, token, session, body string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			r.Header.Set("Mcp-Session-Id", session)
			r.Header.Set("MCP-Protocol-Version", "2025-06-18")
		}
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	expect := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("got %d want %d: %s", w.Code, status, w.Body.String())
		}
	}
	expect(call("POST", endpoint, "", "", initialize, nil), 401)
	expect(call("POST", endpoint, bootstrap, "", initialize, nil), 403)
	expect(call("POST", endpoint, raw, "", initialize, map[string]string{"Origin": "https://evil.example"}), 403)
	expect(call("POST", "/api/v1/mcp?project=other&environment=development", raw, "", initialize, nil), 403)
	expect(call("POST", endpoint+"&allow_deploy=true", raw, "", initialize, nil), 403)
	expect(call("POST", endpoint, raw, "", initialize, map[string]string{"Accept": "application/json"}), 406)
	expect(call("POST", endpoint, raw, "", initialize, map[string]string{"Content-Type": "text/plain"}), 415)
	expect(call("POST", endpoint, raw, "", "["+initialize+"]", nil), 400)
	expect(call("POST", endpoint, raw, "", strings.Repeat(" ", 513<<10)+initialize, nil), 413)
	expect(call("GET", endpoint, raw, "", "", nil), 405)
	init := call("POST", endpoint, raw, "", initialize, nil)
	expect(init, 200)
	session := init.Header().Get("Mcp-Session-Id")
	if session == "" {
		t.Fatal("missing session")
	}
	expect(call("POST", endpoint, raw, session, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil), 202)
	list := call("POST", endpoint, raw, session, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	expect(list, 200)
	if strings.Contains(list.Body.String(), `"name":"deploy"`) || !strings.Contains(list.Body.String(), `"name":"service_runtime"`) {
		t.Fatal(list.Body.String())
	}
	expect(call("POST", endpoint, other, session, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil), 404)
	expect(call("POST", endpoint, raw, session, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, map[string]string{"MCP-Protocol-Version": "1900-01-01"}), 400)
	denied := call("POST", endpoint, raw, session, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"deploy","arguments":{"plan_id":"bad"}}}`, nil)
	if !strings.Contains(denied.Body.String(), `"isError":true`) {
		t.Fatal(denied.Body.String())
	}
	// Ten sessions per key; the eleventh is refused, and DELETE releases capacity.
	// The original initialized session already occupies one slot.
	for i := 0; i < 8; i++ {
		expect(call("POST", endpoint, raw, "", initialize, nil), 200)
	}
	second := call("POST", endpoint, raw, "", initialize, nil)
	expect(second, 200)
	expect(call("POST", endpoint, raw, "", initialize, nil), 429)
	expect(call("DELETE", endpoint, raw, second.Header().Get("Mcp-Session-Id"), "", nil), 204)
	expect(call("POST", endpoint, raw, second.Header().Get("Mcp-Session-Id"), `{"jsonrpc":"2.0","id":2,"method":"ping"}`, nil), 404)
	expect(call("POST", endpoint, raw, "", initialize, nil), 200)
	expect(call("POST", endpoint, raw, "", initialize, nil), 429)
	if _, err := db.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", key.ID); err != nil {
		t.Fatal(err)
	}
	expect(call("POST", endpoint, raw, session, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil), 401)

	writeURL := endpoint + "&allow_deploy=true"
	init = call("POST", writeURL, writer, "", initialize, nil)
	expect(init, 200)
	session = init.Header().Get("Mcp-Session-Id")
	tool := func(name string, args any) *httptest.ResponseRecorder {
		payload := store.JSON(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		w := call("POST", writeURL, writer, session, string(payload), nil)
		expect(w, 200)
		return w
	}
	plan := tool("plan", map[string]string{"toml": "name='mcp-app'\n[services.api]\nimage='nginx:alpine'"})
	var planned struct {
		Result struct {
			StructuredContent struct {
				Data struct {
					PlanID string `json:"plan_id"`
				} `json:"data"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(plan.Body.Bytes(), &planned); err != nil {
		t.Fatal(err)
	}
	if planned.Result.StructuredContent.Data.PlanID == "" {
		t.Fatal(plan.Body.String())
	}
	result := tool("deploy", map[string]string{"plan_id": planned.Result.StructuredContent.Data.PlanID})
	if strings.Contains(result.Body.String(), `"isError":true`) {
		t.Fatal(result.Body.String())
	}
	repeat := tool("deploy", map[string]string{"plan_id": planned.Result.StructuredContent.Data.PlanID})
	if result.Body.String() != repeat.Body.String() {
		t.Fatal("reviewed retry changed accepted release")
	}
	// Explicit Cloud embedding never registers this endpoint.
	cloud := (&api.Server{Store: db, Auth: api.AuthConfig{DeploymentMode: cluster.DeploymentManagedCloud}}).Handler()
	r := httptest.NewRequest("POST", endpoint, strings.NewReader(initialize))
	r.Header.Set("Authorization", "Bearer "+writer)
	w := httptest.NewRecorder()
	cloud.ServeHTTP(w, r)
	expect(w, 404)
}

func TestApplicationBuildProvenanceUsesExactAcceptedDigest(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "provenance")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse([]byte("name='source-app'\n[services.api]\nimage='ghcr.io/example/app:latest'\n[services.worker]\nimage='ghcr.io/example/app:latest'\n[services.external]\nimage='nginx:alpine'"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Accept(ctx, owner, "demo", "development", app, 0, "provenance-original")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := spec.Normalize(app)
	digest := "ghcr.io/example/app@sha256:" + strings.Repeat("a", 64)
	for _, name := range []string{"api", "worker"} {
		svc := resolved.Services[name]
		svc.Image = digest
		resolved.Services[name] = svc
	}
	if _, err = claim.SetResolved(ctx, resolved); err != nil {
		t.Fatal(err)
	}
	if err = claim.Finish(ctx, "succeeded", "", nil); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	cfg := map[string]any{"project": "demo", "environment": "development", "name": app.Name, "application_id": d.ApplicationID, "service": "api", "reuse_services": []string{"worker"}, "provider": "github", "repository": "example/repository", "branch": "main", "env": map[string]string{"PASSWORD": "SENSITIVE_FIXTURE_NEVER_EXPOSE"}}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO build_configs(id,project,environment,name,service,application_id,config,grant_id,created_by) VALUES('mcp-build','demo','development',$1,'api',$2,$3,$4,$5)`, app.Name, d.ApplicationID, store.JSON(cfg), owner.KeyID, owner.ID); err != nil {
		t.Fatal(err)
	}
	add := func(id, commit, image, status string, config map[string]any) {
		_, err := db.Pool.Exec(ctx, `INSERT INTO build_runs(id,build_id,identity_id,key_id,idempotency_key,request_hash,config,config_revision,commit_sha,status,conclusion,image)
   VALUES($1,'mcp-build',$2,$3,$1,'x',$4,1,$5,$6,'success',$7)`, id, owner.ID, owner.KeyID, store.JSON(config), commit, status, image)
		if err != nil {
			t.Fatal(err)
		}
	}
	sha := strings.Repeat("b", 40)
	add("matched", sha, digest, "completed", cfg)
	add("wrong-image", strings.Repeat("c", 40), "ghcr.io/example/app@sha256:"+strings.Repeat("c", 64), "completed", cfg)
	add("failed", strings.Repeat("d", 40), digest, "failed", cfg)
	other := map[string]any{}
	for k, v := range cfg {
		other[k] = v
	}
	other["application_id"] = "other-app"
	add("other-app", strings.Repeat("e", 40), digest, "completed", other)
	handler := (&api.Server{Store: db}).Handler()
	read := func() map[string]any {
		r := httptest.NewRequest("GET", "/api/v1/applications/"+d.ApplicationID+"/provenance", nil)
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "SENSITIVE_FIXTURE") {
			t.Fatal("build environment disclosed")
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out["services"].(map[string]any)
	}
	services := read()
	for _, name := range []string{"api", "worker"} {
		svc := services[name].(map[string]any)
		if svc["commit_sha"] != sha || len(svc["builds"].([]any)) != 1 {
			t.Fatal(svc)
		}
	}
	if services["external"].(map[string]any)["source_status"] != "unknown" {
		t.Fatal(services)
	}
	add("ambiguous", strings.Repeat("f", 40), digest, "completed", cfg)
	svc := read()["api"].(map[string]any)
	if svc["source_status"] != "ambiguous" || svc["commit_sha"] != nil {
		t.Fatal(svc)
	}
	// Even repeated identical SHAs cannot be claimed authoritative when older
	// matching rows have been excluded by the response bound.
	for i := 0; i < 101; i++ {
		add(fmt.Sprintf("bounded-%03d", i), sha, digest, "completed", cfg)
	}
	svc = read()["api"].(map[string]any)
	if svc["source_status"] != "incomplete" || svc["commit_sha"] != nil || len(svc["builds"].([]any)) != 100 {
		t.Fatal(svc)
	}
}

// Opt-in interoperability test; the SDK is a test tool, not a server dependency.
func TestHTTPMCPOfficialSDK(t *testing.T) {
	sdk := os.Getenv("HAKOPOD_TEST_MCP_SDK")
	if sdk == "" {
		t.Skip("set HAKOPOD_TEST_MCP_SDK to the installed @modelcontextprotocol/sdk directory")
	}
	db, _ := database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bootstrap, err := db.Bootstrap(ctx, "mcp-sdk")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.Authenticate(ctx, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	_, raw, err := db.CreateKey(ctx, owner, store.KeyInput{Name: "sdk", Project: "demo", Environment: "development", Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&api.Server{Store: db}).Handler())
	defer server.Close()
	script, err := filepath.Abs("testdata/mcp-client.mjs")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Env = append(os.Environ(), "MCP_TEST_URL="+server.URL+"/api/v1/mcp?project=demo&environment=development", "MCP_TEST_KEY="+raw, "MCP_TEST_SDK="+sdk)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("SDK interoperability: %v: %s", err, output)
	}
	t.Log(string(output))
}
