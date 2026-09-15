package api

import (
	"bytes"
	"context"
	"github.com/hakopod/hakopod/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDelegatedEmailOwnerCapabilityResponse(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "node-owner")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE identities SET email='node-owner@example.test',email_verified=true WHERE id=$1", admin.ID); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db}
	h := s.Handler()
	for _, management := range []bool{false, true} {
		permissions := []string{"deployments:read", "deployments:write"}
		if management {
			permissions = append(permissions, "git:manage", "applications:manage")
		}
		_, key, err := db.CreateKey(ctx, admin, store.KeyInput{Name: "workspace", Project: "demo", Environment: "development", Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		result := gitConnectionCall(t, h, key, "GET", "/me", nil, 200)
		if result["admin"] != false || result["can_manage_git"] != management || result["can_manage_applications"] != management {
			t.Fatalf("scoped capability mismatch: admin=%v git=%v applications=%v", result["admin"], result["can_manage_git"], result["can_manage_applications"])
		}
		gitConnectionCall(t, h, key, "GET", "/keys", nil, 403)
	}
}

func TestScopedGitManagementPrivateDockerfileAndIsolation(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "scoped-git")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('other');INSERT INTO environments(project,name) VALUES('other','development')"); err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	for _, project := range []string{"demo", "other"} {
		_, key, e := db.CreateKey(ctx, admin, store.KeyInput{Name: project, Project: project, Environment: "development", Permissions: []string{"deployments:read", "deployments:write", "git:manage", "applications:manage"}, ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		keys[project] = key
	}
	calls := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer private-demo" {
			t.Error("wrong private repository credential")
		}
		switch {
		case strings.Contains(r.URL.Path, "/commits/"):
			write(w, 200, map[string]string{"sha": strings.Repeat("a", 40)})
		case strings.HasSuffix(r.URL.Path, "/contents/"):
			write(w, 200, []map[string]string{{"name": "Dockerfile", "type": "file"}, {"name": "requirements.txt", "type": "file"}, {"name": "main.py", "type": "file"}})
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	s := &Server{Store: db, Auth: AuthConfig{EncryptionKey: strings.Repeat("17", 32), PublicURL: "https://cloud.example.test"}, githubAPIURL: remote.URL, githubHTTP: remote.Client()}
	h := s.Handler()
	ids := map[string]string{}
	for _, project := range []string{"demo", "other"} {
		out := gitConnectionCall(t, h, keys[project], "POST", "/git/connections", map[string]any{"name": "My GitHub", "provider": "github", "auth_kind": "token", "token": "private-" + project}, 201)
		ids[project] = out["id"].(string)
	}
	list := gitConnectionCall(t, h, keys["demo"], "GET", "/git/connections", nil, 200)
	if len(list["items"].([]any)) != 1 || strings.Contains(string(store.JSON(list)), ids["other"]) {
		t.Fatal("cross-workspace Git listing")
	}
	for _, id := range []string{ids["other"], "github-default"} {
		gitConnectionCall(t, h, keys["demo"], "GET", "/git/connections/"+id, nil, 403)
		gitConnectionCall(t, h, keys["demo"], "DELETE", "/git/connections/"+id+"?expected_revision=1", nil, 403)
	}
	input := map[string]any{"project": "demo", "environment": "development", "name": "fastapi", "service": "api", "provider": "github", "connection_id": ids["demo"], "repository": "team/private-fastapi", "branch": "main", "mode": "dockerfile", "architecture": "amd64", "dockerfile": "Dockerfile", "port": 8000, "command": []string{"uvicorn"}, "args": []string{"main:app", "--host", "0.0.0.0", "--port", "8000"}}
	result := gitConnectionCall(t, h, keys["demo"], "POST", "/builds/detect", input, 200)
	if result["mode"] != "dockerfile" || calls != 2 {
		t.Fatal("private Dockerfile detection failed", result, calls)
	}
	created := gitConnectionCall(t, h, keys["demo"], "POST", "/builds", input, 201)
	config, e := s.readBuild(ctx, created["id"].(string))
	if e != nil {
		t.Fatal(e)
	}
	run := buildRun{Status: "completed", Conclusion: "success", ConfigRevision: config.Revision, Image: "example.invalid/private@sha256:" + strings.Repeat("b", 64)}
	next, _, e := s.prepareBuildSpec(ctx, config, run)
	if e != nil {
		t.Fatal(e)
	}
	if len(next.Services["api"].Command) != 1 || next.Services["api"].Command[0] != "uvicorn" || strings.Join(next.Services["api"].Args, " ") != "main:app --host 0.0.0.0 --port 8000" {
		t.Fatal("runtime command did not reach deployment")
	}
	empty := []string{}
	config.Command = &empty
	config.Args = &empty
	next, _, e = s.prepareBuildSpec(ctx, config, run)
	if e != nil || len(next.Services["api"].Command) != 0 || len(next.Services["api"].Args) != 0 {
		t.Fatal("image defaults not restored", e)
	}
	input["connection_id"] = ids["other"]
	gitConnectionCall(t, h, keys["demo"], "POST", "/builds/detect", input, 403)
	if calls != 2 {
		t.Fatal("foreign connection reached provider")
	}
	// App setup requires a delegated browser-session digest, not just the key.
	gitConnectionCall(t, h, keys["demo"], "POST", "/git/github/start", map[string]any{"name": "App"}, 403)
	request := httptest.NewRequest("POST", "/api/v1/git/github/start", bytes.NewReader(store.JSON(map[string]any{"name": "App", "builds": true})))
	request.Header.Set("Authorization", "Bearer "+keys["demo"])
	request.Header.Set("X-Hakopod-Git-Session", strings.Repeat("a", 64))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
}
