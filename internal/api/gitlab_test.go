package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestGitLabSourcePinningAndProviderInboxIsolation(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "gitlab-source")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := db.Authenticate(ctx, raw)
	app, _ := spec.Normalize(spec.Application{Name: "gitlab-source", Services: map[string]spec.Service{"web": {Image: "python:3.13-alpine", Port: 8080}}})
	first, err := db.Accept(ctx, p, "demo", "development", app, 0, "gitlab-source-initial")
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("b", 40)
	content := "name='gitlab-source'\n[services.web]\nimage='python:3.13-alpine'\nport=8080\n"
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Error("missing GitLab auth header")
		}
		if !strings.Contains(r.URL.EscapedPath(), "group%2Fsubgroup%2Frepo") {
			t.Error("nested project path not escaped", r.URL.EscapedPath())
		}
		if strings.Contains(r.URL.Path, "/commits/") {
			write(w, 200, map[string]string{"id": sha})
			return
		}
		if r.URL.Query().Get("ref") != sha {
			t.Error("file fetch did not pin commit")
		}
		write(w, 200, map[string]any{"encoding": "base64", "size": len(content), "commit_id": sha, "content": base64.StdEncoding.EncodeToString([]byte(content))})
	}))
	defer remote.Close()
	secret := strings.Repeat("s", 32)
	server := &Server{Store: db, gitlabAPIURL: remote.URL, gitlabHTTP: remote.Client(), gitlabTestCredentials: func(context.Context) (map[string][]byte, error) {
		return map[string][]byte{"token": []byte("test-token"), "webhook-secret": []byte(secret)}, nil
	}}
	handler := server.Handler()
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(store.JSON(body)))
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/applications/" + first.ApplicationID + "/source"
	w := request("PUT", path, map[string]any{"provider": "gitlab", "repository": "group/subgroup/repo", "branch": "main", "path": "hakopod.toml", "auto_deploy": true, "expected_source_revision": 0})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("POST", path+"/plan", map[string]any{})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var plan map[string]any
	json.Unmarshal(w.Body.Bytes(), &plan)
	if plan["commit_sha"] != sha {
		t.Fatal("plan lost GitLab commit", plan)
	}
	// A GitHub delivery with the same repository cannot trigger this binding.
	if err := server.enqueueSources(ctx, "github-delivery-123", sha, "group/subgroup/repo", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	var count int
	db.Pool.QueryRow(ctx, "SELECT count(*) FROM source_jobs").Scan(&count)
	if count != 0 {
		t.Fatal("cross-provider source trigger")
	}
	webhook := func(token, delivery string) *httptest.ResponseRecorder {
		payload := map[string]any{"object_kind": "push", "ref": "refs/heads/main", "after": sha, "project": map[string]string{"path_with_namespace": "group/subgroup/repo"}}
		r := httptest.NewRequest("POST", "/api/v1/webhooks/gitlab", bytes.NewReader(store.JSON(payload)))
		r.Header.Set("X-Gitlab-Token", token)
		r.Header.Set("X-Gitlab-Event", "Push Hook")
		r.Header.Set("X-Gitlab-Event-UUID", delivery)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := webhook("invalid", "test-delivery-123"); w.Code != 401 {
		t.Fatal("invalid webhook accepted", w.Code)
	}
	for i := 0; i < 2; i++ {
		if w := webhook(secret, "test-delivery-123"); w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	db.Pool.QueryRow(ctx, "SELECT count(*) FROM source_jobs").Scan(&count)
	if count != 1 {
		t.Fatal("webhook duplicate was not suppressed", count)
	}
	server.runSource(ctx)
	var status string
	db.Pool.QueryRow(ctx, "SELECT status FROM source_jobs LIMIT 1").Scan(&status)
	if status != "accepted" {
		t.Fatal("durable GitLab job failed", status)
	}
	// Replaying the worker does not manufacture another deployment.
	server.runSource(ctx)
	db.Pool.QueryRow(ctx, "SELECT count(*) FROM deployments WHERE application_id=$1", first.ApplicationID).Scan(&count)
	if count != 2 {
		t.Fatal("unexpected deployment count", count)
	}
}
func TestSourceProviderValidation(t *testing.T) {
	for _, tc := range []struct {
		provider, repo string
		valid          bool
	}{{"gitlab", "group/subgroup/repo", true}, {"github", "group/subgroup/repo", false}, {"gitlab", "group/../repo", false}, {"gitlab", "group//repo", false}, {"unknown", "owner/repo", false}, {"", "owner/repo", true}} {
		if validSourceRepository(tc.provider, tc.repo) != tc.valid {
			t.Errorf("%+v", tc)
		}
	}
}
