package api_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestCIDeploymentProvenanceIsAtomicScopedAndDigestBound(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	token, err := db.Bootstrap(ctx, "ci-provenance")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	old := "ghcr.io/example/backend@sha256:" + strings.Repeat("a", 64)
	image := "ghcr.io/example/backend@sha256:" + strings.Repeat("b", 64)
	names := []string{"api", "celery-beat", "celery-worker", "setup"}
	app := spec.Application{Name: "ci-app", Services: map[string]spec.Service{}}
	for _, name := range append(names, "other") {
		app.Services[name] = spec.Service{Image: old}
	}
	app, err = spec.Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := db.Accept(ctx, p, "demo", "development", app, 0, "initial-ci-app", app)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&api.Server{Store: db}).Handler()
	call := func(path string, body any, key string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/v1/"+path, strings.NewReader(string(store.JSON(body))))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: got %d want %d: %s", path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	provenance := map[string]store.SourceBuild{}
	for _, name := range names {
		svc := app.Services[name]
		svc.Image = image
		app.Services[name] = svc
		provenance[name] = store.SourceBuild{Image: image, CommitSHA: strings.Repeat("c", 40), Provider: "github", Repository: "example/backend", RunURL: "https://github.com/example/backend/actions/runs/123"}
	}
	body := map[string]any{"project": "demo", "environment": "development", "spec": app, "services": names, "expected_revision": 1, "provenance": provenance}
	var plan map[string]any
	json.Unmarshal(call("plan", body, "", 200), &plan)
	if len(plan["provenance"].(map[string]any)) != 4 {
		t.Fatal(plan)
	}
	var accepted store.Deployment
	json.Unmarshal(call("deployments", body, "ci-provenance-release", 202), &accepted)
	if accepted.Revision != 2 || len(accepted.Provenance) != 4 {
		t.Fatal(accepted)
	}
	_, err = db.Pool.Exec(ctx, "UPDATE deployments SET resolved_spec=spec WHERE id=$1", accepted.ID)
	if err != nil {
		t.Fatal(err)
	}
	var retry store.Deployment
	json.Unmarshal(call("deployments", body, "ci-provenance-release", 202), &retry)
	if retry.ID != accepted.ID {
		t.Fatal("retry created another release")
	}
	changed := provenance["api"]
	changed.CommitSHA = strings.Repeat("d", 40)
	provenance["api"] = changed
	call("deployments", body, "ci-provenance-release", 409)
	changed.CommitSHA = strings.Repeat("c", 40)
	provenance["api"] = changed
	// A claim against an unselected service is rejected before acceptance.
	provenance["other"] = provenance["api"]
	call("plan", body, "", 400)
	delete(provenance, "other")
	changed.Image = old
	provenance["api"] = changed
	call("plan", body, "", 400)
	changed.Image = image
	changed.CommitSHA = "short"
	provenance["api"] = changed
	call("deployments", body, "invalid-sha-release", 400)
	changed.CommitSHA = strings.Repeat("c", 40)
	provenance["api"] = changed
	changed.RunURL = "https://user:password@example.com/run"
	provenance["api"] = changed
	call("plan", body, "", 400)
	changed.RunURL = "https://github.com/example/backend/actions/runs/123"
	provenance["api"] = changed
	// Resolve the accepted image without pretending this test ran a cluster rollout.
	_, err = db.Pool.Exec(ctx, "UPDATE deployments SET resolved_spec=spec WHERE id=$1", accepted.ID)
	if err != nil {
		t.Fatal(err)
	}
	read := func() map[string]any {
		r := httptest.NewRequest("GET", "/api/v1/applications/"+initial.ApplicationID+"/provenance", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out["services"].(map[string]any)
	}
	for name, value := range read() {
		v := value.(map[string]any)
		if name == "other" {
			if v["source_status"] != "unknown" {
				t.Fatal(v)
			}
			continue
		}
		if v["commit_sha"] != strings.Repeat("c", 40) || v["source_status"] != "reported_build" {
			t.Fatal(v)
		}
		b := v["builds"].([]any)[0].(map[string]any)
		if b["source"] != "ci_reported" || b["deployment_id"] != accepted.ID || b["reported_by"] != p.ID {
			t.Fatal(b)
		}
	}
	// Application restrictions apply to the new metadata just like the spec.
	_, restricted, err := db.CreateKey(ctx, p, store.KeyInput{Name: "other-app-reader", Project: "demo", Environment: "development", Application: "different-app", Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/applications/"+initial.ApplicationID+"/provenance", nil)
	req.Header.Set("Authorization", "Bearer "+restricted)
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, req)
	if denied.Code != 403 {
		t.Fatalf("application-scoped key read provenance: %d", denied.Code)
	}
	// Later releases without new claims retain exact digest history.
	later, err := db.Accept(ctx, p, "demo", "development", app, 2, "later-no-provenance", app)
	if err != nil {
		t.Fatal(err)
	}
	if read()["api"].(map[string]any)["commit_sha"] != strings.Repeat("c", 40) {
		t.Fatal("lost historical mapping")
	}
	// Different source claims for the same digest cannot silently overwrite history.
	changed.CommitSHA = strings.Repeat("e", 40)
	ambiguous := map[string]store.SourceBuild{"api": changed}
	latest, err := db.AcceptWithProvenance(ctx, p, "demo", "development", app, later.Revision, "ambiguous-ci-provenance", ambiguous)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool.Exec(ctx, "UPDATE deployments SET resolved_spec=spec WHERE id=$1", latest.ID)
	if err != nil {
		t.Fatal(err)
	}
	v := read()["api"].(map[string]any)
	if v["source_status"] != "ambiguous" || v["commit_sha"] != nil {
		t.Fatal(v)
	}
}
