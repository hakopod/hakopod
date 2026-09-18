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
	original, err := spec.Parse([]byte("name='targeted'\n[services.api]\nimage='python:3.13-alpine'\n[services.worker]\nimage='python:3.13-alpine'\n[services.web]\nimage='python:3.13-alpine'"))
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
		req.Header.Set("Idempotency-Key", "selected-services-release")
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
	group, _ := spec.Normalize(next)
	worker := group.Services["worker"]
	worker.Image = "python:3.14-alpine"
	group.Services["worker"] = worker
	for _, route := range []string{"/api/v1/plan", "/api/v1/deployments"} {
		for _, tc := range []struct {
			name     string
			services []string
			service  string
		}{
			{"empty", []string{}, ""},
			{"duplicate", []string{"api", "api"}, ""},
			{"blank", []string{" "}, ""},
			{"missing", []string{"api", "missing"}, ""},
			{"both selectors", []string{"api", "worker"}, "api"},
			{"too many", make([]string, 21), ""},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				status, result := call("POST", route, map[string]any{"project": "demo", "environment": "development", "spec": group, "services": tc.services, "service": tc.service, "expected_revision": d.Revision})
				if status != 400 || result["error"].(map[string]any)["code"] != "invalid_service" {
					t.Fatalf("invalid selection accepted: %d %v", status, result)
				}
			})
		}
	}
	for _, useTOML := range []bool{false, true} {
		body := map[string]any{"project": "demo", "environment": "development", "services": []string{"worker", "api"}}
		if useTOML {
			body["toml"] = "name='targeted'\n[services.api]\nimage='python:3.14-alpine'\n[services.worker]\nimage='python:3.14-alpine'"
		} else {
			body["spec"] = group
		}
		status, result := call("POST", "/api/v1/plan", body)
		if status != 200 {
			t.Fatalf("group plan: %d %v", status, result)
		}
		services := result["spec"].(map[string]any)["services"].(map[string]any)
		if services["web"].(map[string]any)["image"] != resolved.Services["web"].Image ||
			services["api"].(map[string]any)["image"] != "python:3.14-alpine" ||
			services["worker"].(map[string]any)["image"] != "python:3.14-alpine" {
			t.Fatalf("incorrect group merge: %v", services)
		}
	}
	// Shared changes on the second target must be rejected as well.
	for _, field := range []string{"env", "networks", "volumes"} {
		changed, _ := spec.Normalize(group)
		svc := changed.Services["worker"]
		switch field {
		case "env":
			changed.Env = map[string]string{"SHARED": "changed"}
		case "networks":
			changed.Networks["private"] = spec.Network{Internal: true}
			svc.Networks = []string{"private"}
		case "volumes":
			changed.Volumes = map[string]spec.NamedVolume{"data": {SizeGiB: 1}}
			svc.Mounts = []spec.Mount{{Volume: "data", MountPath: "/data"}}
		}
		changed.Services["worker"] = svc
		status, result := call("POST", "/api/v1/plan", map[string]any{"project": "demo", "environment": "development", "spec": changed, "services": []string{"api", "worker"}})
		if status != 400 || result["error"].(map[string]any)["code"] != "shared_configuration" {
			t.Fatalf("shared %s allowed: %d %v", field, status, result)
		}
	}
	for _, field := range []string{"networks", "volumes"} {
		t.Run("shared "+field, func(t *testing.T) {
			changed, err := spec.Normalize(next)
			if err != nil {
				t.Fatal(err)
			}
			if field == "networks" {
				changed.Networks["default"] = spec.Network{Internal: true}
			} else {
				changed.Volumes = map[string]spec.NamedVolume{"data": {SizeGiB: 1}}
				svc := changed.Services["api"]
				svc.Mounts = []spec.Mount{{Volume: "data", MountPath: "/data"}}
				changed.Services["api"] = svc
			}
			body := map[string]any{"project": "demo", "environment": "development", "service": "api", "spec": changed}
			status, result := call("POST", "/api/v1/plan", body)
			problem, _ := result["error"].(map[string]any)
			if status != http.StatusBadRequest || problem["code"] != "shared_configuration" {
				t.Fatalf("targeted plan silently changed shared configuration: %d %v", status, result)
			}
			delete(body, "service")
			status, result = call("POST", "/api/v1/plan", body)
			if status != http.StatusOK {
				t.Fatalf("application-wide plan rejected shared configuration: %d %v", status, result)
			}
			found := false
			for _, value := range result["changes"].([]any) {
				change := value.(map[string]any)
				if change["service"] == "" && change["field"] == field {
					found = true
				}
			}
			if !found {
				t.Fatal("shared configuration missing from application-wide review")
			}
		})
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
	body := map[string]any{"project": "demo", "environment": "development", "spec": group, "services": []string{"api", "worker"}, "expected_revision": d.Revision}
	status, accepted := call("POST", "/api/v1/deployments", body)
	if status != 202 {
		t.Fatalf("group deployment: %d %v", status, accepted)
	}
	deployment, err := db.Deployment(ctx, accepted["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Revision != d.Revision+1 || deployment.Spec.Services["web"].Image != resolved.Services["web"].Image ||
		deployment.Spec.Services["api"].Image != "python:3.14-alpine" || deployment.Spec.Services["worker"].Image != "python:3.14-alpine" {
		t.Fatalf("group was not accepted as one immutable revision: %+v", deployment)
	}
	_, err = db.Pool.Exec(ctx, "UPDATE identities SET project='demo',environment='development' WHERE id=$1", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := call("GET", "/api/v1/keys", nil); status != 403 {
		t.Fatalf("owner scope reduction did not revoke global administration: %d", status)
	}
}
