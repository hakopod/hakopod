package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/sandbox"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type sandboxRuntimeFixture struct {
	starts, calls, cleanups int
	cleanupReady            bool
}

func (f *sandboxRuntimeFixture) StartSession(ctx context.Context, r sandbox.Record, before func(context.Context) error) (sandbox.RuntimeState, error) {
	if err := before(ctx); err != nil {
		return sandbox.RuntimeState{}, err
	}
	f.starts++
	return sandbox.RuntimeState{NamespaceUID: "ns-fixture", PodUID: "pod-fixture", ContainerID: "container-fixture", Ready: true}, nil
}
func (f *sandboxRuntimeFixture) ObserveSession(context.Context, sandbox.Record) (sandbox.RuntimeState, error) {
	return sandbox.RuntimeState{NamespaceUID: "ns-fixture", PodUID: "pod-fixture", ContainerID: "container-fixture", Ready: true}, nil
}
func (f *sandboxRuntimeFixture) CallSession(ctx context.Context, r sandbox.Record, in io.Reader, out io.Writer) error {
	f.calls++
	_, err := io.Copy(out, in)
	return err
}
func (f *sandboxRuntimeFixture) CleanupSession(context.Context, sandbox.Record) (bool, error) {
	f.cleanups++
	return f.cleanupReady, nil
}
func sessionAPIFixture(t *testing.T) (*Server, store.Principal, store.Deployment, sandbox.CreateRequest, *sandboxRuntimeFixture) {
	t.Helper()
	db := actionsDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "session-api")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{Name: "session-api", Services: map[string]spec.Service{"worker": {Image: "example/worker@sha256:" + strings.Repeat("a", 64), Command: []string{"worker"}, RunAsUser: 1000, RunAsGroup: 1000, FSGroup: 1000, ReadOnlyRootFilesystem: true, RuntimeProfile: "sandbox", Session: &spec.SandboxSession{AllowedIdentities: []string{admin.ID}, HelperCommand: []string{"helper"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	db.ValidateDeployment = func(context.Context, store.Application, spec.Application) error { return nil }
	d, err := db.Accept(ctx, admin, "demo", "development", app, 0, "initial-session", app)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if err = claim.Finish(ctx, "succeeded", "", nil); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	_, key, err := db.CreateKey(ctx, admin, store.KeyInput{Name: "session-client", Project: "demo", Environment: "development", Application: app.Name, Permissions: []string{"sessions:create", "sessions:read", "sessions:call", "sessions:delete"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	fake := &sandboxRuntimeFixture{}
	return &Server{Store: db, sessionTestRuntime: fake, sessionCalls: make(chan struct{}, 2)}, p, d, sandbox.CreateRequest{ExpectedImage: app.Services["worker"].Image, ExpectedRevision: 1, RuntimeKey: "runtime-one"}, fake
}
func TestSessionHTTPAndCleanupNeverExposeAnotherOwner(t *testing.T) {
	s, p, d, in, fake := sessionAPIFixture(t)
	ctx := context.Background()
	mux := http.NewServeMux()
	s.registerSessionRoutes(mux)
	base := "/api/v1/applications/" + d.ApplicationID + "/services/worker/sessions"
	call := func(method, path, owner, idem, generation string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("X-Hakopod-Owner-Scope", owner)
		r.Header.Set("Idempotency-Key", idem)
		r.Header.Set("X-Hakopod-Session-Generation", generation)
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	body, _ := json.Marshal(in)
	w := call("POST", base, "tenant-a", "create-once", "", body)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created sandbox.Record
	if json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal("invalid receipt")
	}
	if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "namespace_uid") || strings.Contains(w.Body.String(), "owner_hash") {
		t.Fatal("receipt exposed private state")
	}
	lease, err := s.Store.ClaimSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.reconcileSession(ctx, lease, fake)
	payload := []byte("{\"status\":\"ok\"}\n")
	target := base + "/" + created.ID
	w = call("POST", target+"/call", "tenant-b", "call-1", created.Generation, payload)
	if w.Code != 404 || fake.calls != 0 {
		t.Fatal("cross-owner code reached runtime", w.Code)
	}
	w = call("POST", target+"/call", "tenant-a", "call-1", "stale-generation", payload)
	if w.Code != 409 || fake.calls != 0 {
		t.Fatal("stale generation reached runtime", w.Code)
	}
	w = call("POST", target+"/call", "tenant-a", "call-1", created.Generation, payload)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), payload) || fake.calls != 1 {
		t.Fatal("bounded streaming failed", w.Code, w.Body.String())
	}
	w = call("POST", target+"/call", "tenant-a", "call-1", created.Generation, payload)
	if w.Code != 409 || fake.calls != 1 {
		t.Fatal("code was replayed", w.Code)
	}
	if w = call("DELETE", target, "tenant-b", "", "", nil); w.Code != 404 {
		t.Fatal("cross-owner cancellation allowed", w.Code)
	}
	if w = call("DELETE", target, "tenant-a", "", "", nil); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err = s.Store.Pool.Exec(ctx, `UPDATE sandbox_sessions SET lease_until=now()-interval '1 second' WHERE id=$1`, created.ID); err != nil {
			t.Fatal(err)
		}
		lease, err = s.Store.ClaimSession(ctx)
		if err != nil {
			t.Fatal(err)
		}
		fake.cleanupReady = attempt == 1
		s.reconcileSession(ctx, lease, fake)
		w = call("GET", target, "tenant-a", "", "", nil)
		var state sandbox.Record
		_ = json.Unmarshal(w.Body.Bytes(), &state)
		if attempt == 0 && (state.Status != sandbox.Closing || !state.CleanupPending || state.ClosedAt != nil) {
			t.Fatal("API completed before cleanup", state)
		}
		if attempt == 1 && (state.Status != sandbox.Closed || state.CleanupPending || state.ClosedAt == nil) {
			t.Fatal("cleanup receipt not persisted", state)
		}
	}
}
func TestSessionControllerClosesLostBrokerSessions(t *testing.T) {
	s, p, d, in, fake := sessionAPIFixture(t)
	ctx := context.Background()
	owner, _ := sandbox.HashKey("tenant-a")
	r, err := s.Store.CreateSession(ctx, p, d.ApplicationID, "worker", owner, "create-orphan", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.Pool.Exec(ctx, `UPDATE sandbox_sessions SET idle_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := s.Store.ClaimSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.reconcileSession(ctx, lease, fake)
	state, err := s.Store.ReadSession(ctx, p, d.ApplicationID, "worker", owner, r.ID, "sessions:read")
	if err != nil || state.Status != sandbox.Closing || fake.starts != 0 {
		t.Fatal("lost broker session started code", state, err)
	}
	if _, err = s.Store.Pool.Exec(ctx, `UPDATE sandbox_sessions SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	lease, err = s.Store.ClaimSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fake.cleanupReady = true
	s.reconcileSession(ctx, lease, fake)
	state, err = s.Store.ReadSession(ctx, p, d.ApplicationID, "worker", owner, r.ID, "sessions:read")
	if err != nil || state.Status != sandbox.Closed {
		t.Fatal(fmt.Sprintf("orphan cleanup failed: %s %v", state.Status, err))
	}
}
