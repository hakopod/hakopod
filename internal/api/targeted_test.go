package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestRoutesRegisterWithoutConflict(t *testing.T) {
	handler := (&api.Server{}).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/healthz", nil))
	if response.Code != 200 {
		t.Fatalf("health route returned %d", response.Code)
	}
}

func TestTargetedPlanAndSlimHistory(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "operator")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	original, err := spec.Parse([]byte("name='targeted'\n[services.api]\nimage='python:3.13-alpine'\n[services.web]\nimage='python:3.13-alpine'"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Accept(ctx, p, "demo", "development", original, 0, "original-release")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	resolved, _ := spec.Normalize(original)
	for name, svc := range resolved.Services {
		svc.Image = "docker.io/library/python@sha256:" + strings.Repeat("a", 64)
		resolved.Services[name] = svc
	}
	if _, err = claim.SetResolved(ctx, resolved); err != nil {
		t.Fatal(err)
	}
	if err = claim.Finish(ctx, "succeeded", "", map[string]any{"status": "healthy"}); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	handler := (&api.Server{Store: db}).Handler()
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(string(store.JSON(body))))
		req.Header.Set("Authorization", "Bearer "+raw)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		var out map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return rr.Code, out
	}
	next, _ := spec.Normalize(original)
	svc := next.Services["api"]
	svc.Image = "python:3.14-alpine"
	next.Services["api"] = svc
	status, plan := call("POST", "/api/v1/plan", map[string]any{"project": "demo", "environment": "development", "service": "api", "spec": next})
	if status != http.StatusOK {
		t.Fatalf("targeted plan %d: %v", status, plan)
	}
	found := false
	for _, value := range plan["changes"].([]any) {
		change := value.(map[string]any)
		if change["service"] == "api" && change["field"] == "image" {
			found = true
			if change["before"] != "python:3.13-alpine" || change["after"] != "python:3.14-alpine" {
				t.Fatalf("aliased diff %v", change)
			}
		}
	}
	if !found {
		t.Fatal("targeted image change missing from review")
	}
	planned := plan["spec"].(map[string]any)["services"].(map[string]any)
	if planned["web"].(map[string]any)["image"] != resolved.Services["web"].Image {
		t.Fatal("untouched mutable tag was not pinned to the accepted artifact")
	}
	status, app := call("GET", "/api/v1/applications/"+d.ApplicationID, nil)
	if status != 200 {
		t.Fatal(status)
	}
	history := app["deployments"].([]any)
	if len(history) != 1 {
		t.Fatal("missing history")
	}
	entry := history[0].(map[string]any)
	if _, ok := entry["spec"]; ok {
		t.Fatal("history retained full specifications")
	}
	if _, ok := entry["resolved_spec"]; ok {
		t.Fatal("history retained resolved specifications")
	}
	_, err = db.Pool.Exec(ctx, "UPDATE identities SET project='demo',environment='development' WHERE id=$1", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := call("GET", "/api/v1/keys", nil); status != 403 {
		t.Fatalf("owner scope reduction did not revoke global administration: %d", status)
	}
}
