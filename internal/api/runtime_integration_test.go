package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestRuntimeActionsPinDigestsAndReplayAcrossRevisions(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "runtime-operator")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	original, err := spec.Parse([]byte("name='runtime'\n[services.api]\nimage='python:3.13-alpine'\n[services.web]\nimage='python:3.13-alpine'"))
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := spec.Normalize(original)
	for name, svc := range resolved.Services {
		svc.Image = "docker.io/library/python@sha256:" + strings.Repeat("a", 64)
		resolved.Services[name] = svc
	}
	initial, err := db.Accept(ctx, principal, "demo", "development", original, 0, "runtime-initial", resolved)
	if err != nil {
		t.Fatal(err)
	}
	finish := func() {
		t.Helper()
		claim, err := db.Claim(ctx)
		if err != nil || claim == nil {
			t.Fatal("missing queued action", err)
		}
		defer claim.Release()
		if err = claim.Finish(ctx, "succeeded", "", map[string]any{"status": "healthy"}); err != nil {
			t.Fatal(err)
		}
	}
	finish()
	handler := (&api.Server{Store: db}).Handler()
	call := func(path, idem string, body any) (int, store.Deployment) {
		t.Helper()
		request := httptest.NewRequest("POST", "/api/v1/applications/"+initial.ApplicationID+"/services/api/"+path, strings.NewReader(string(store.JSON(body))))
		request.Header.Set("Authorization", "Bearer "+raw)
		request.Header.Set("Idempotency-Key", idem)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var deployment store.Deployment
		_ = json.Unmarshal(response.Body.Bytes(), &deployment)
		if response.Code >= 500 {
			t.Fatalf("unexpected API failure: %s", response.Body.String())
		}
		return response.Code, deployment
	}
	status, restart := call("restart", "runtime-restart", map[string]any{"expected_revision": 1})
	if status != 202 || restart.Revision != 2 || restart.Spec.Services["api"].RestartNonce == "" || restart.ResolvedSpec == nil {
		t.Fatalf("restart rejected or omitted nonce: %d %+v", status, restart)
	}
	if restart.ResolvedSpec.Services["api"].Image != resolved.Services["api"].Image || restart.ResolvedSpec.Services["web"].Image != resolved.Services["web"].Image {
		t.Fatal("restart changed immutable artifacts")
	}
	finish()
	status, scale := call("scale", "runtime-scale", map[string]any{"expected_revision": 2, "replicas": 3})
	if status != 202 || scale.Revision != 3 || scale.Spec.Services["api"].Replicas != 3 || scale.ResolvedSpec.Services["api"].Image != resolved.Services["api"].Image {
		t.Fatalf("scale failed: %d %+v", status, scale)
	}

	finish()
	status, stopped := call("stop", "runtime-stop", map[string]any{"expected_revision": 3})
	if status != 202 || !stopped.Spec.Services["api"].Suspended || stopped.Spec.Services["api"].Replicas != 3 || stopped.ResolvedSpec.Services["api"].Image != resolved.Services["api"].Image {
		t.Fatalf("stop lost configuration: %d %+v", status, stopped)
	}
	finish()
	status, resumed := call("resume", "runtime-resume", map[string]any{"expected_revision": 4})
	if status != 202 || resumed.Spec.Services["api"].Suspended || resumed.Spec.Services["api"].Replicas != 3 {
		t.Fatalf("resume lost replica count: %d %+v", status, resumed)
	}
	if status, _ := call("stop", "runtime-stale-stop", map[string]any{"expected_revision": 3}); status != 409 {
		t.Fatalf("stale stop accepted: %d", status)
	}
	status, replay := call("restart", "runtime-restart", map[string]any{"expected_revision": 1})
	if status != 202 || replay.ID != restart.ID {
		t.Fatalf("restart replay after advancing revision changed: %d %s", status, replay.ID)
	}
	if status, _ = call("scale", "runtime-scale", map[string]any{"expected_revision": 2, "replicas": 4}); status != 409 {
		t.Fatalf("changed action reused idempotency key: %d", status)
	}
	if status, _ = call("scale", "runtime-invalid", map[string]any{"expected_revision": 2, "replicas": 21}); status != 400 {
		t.Fatalf("out-of-bound scale accepted: %d", status)
	}
}

func TestRuntimeResourceScopeCASHistoryAndExpiry(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "runtime-operator")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	restricted := p
	restricted.Application = "one-application"
	if _, err = db.PutRuntimeResource(ctx, restricted, "registry", "demo", "development", "private", 0, map[string]string{"secret_name": "test"}); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("application-scoped principal changed environment-wide registry", err)
	}
	if _, err = db.PutRuntimeResource(ctx, p, "registry", "demo", "development", "private", 0, map[string]string{"registry": "ghcr.io", "secret_name": "secret-one"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, secret := range []string{"secret-two", "secret-three"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := db.PutRuntimeResource(ctx, p, "registry", "demo", "development", "private", 1, map[string]string{"registry": "ghcr.io", "secret_name": name})
			results <- err
		}(secret)
	}
	wg.Wait()
	close(results)
	won, lost := 0, 0
	for err := range results {
		if err == nil {
			won++
		} else if errors.Is(err, store.ErrConflict) {
			lost++
		} else {
			t.Fatal(err)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatal("resource revision CAS did not serialize", won, lost)
	}
	resource, err := db.RuntimeResource(ctx, "registry", "demo", "development", "private")
	if err != nil || resource.Revision != 2 {
		t.Fatal(err)
	}
	var history int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM runtime_resource_history WHERE kind='registry' AND name='private'").Scan(&history); err != nil || history != 2 {
		t.Fatal("configuration history missing", history, err)
	}
	if _, err = db.PutRuntimeResource(ctx, p, "enrollment", "", "", "abc123", 0, map[string]any{"expires_at": time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err = db.PruneExpiredEnrollments(ctx, p); err != nil {
		t.Fatal(err)
	}
	items, err := db.RuntimeResources(ctx, "enrollment", "", "")
	if err != nil || len(items) != 0 {
		t.Fatal("expired enrollment retained", err)
	}
	if err = db.DeleteRuntimeResource(ctx, restricted, "registry", "demo", "development", "private", 2); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("restricted registry deletion accepted")
	}
}
