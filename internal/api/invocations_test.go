package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/invocation"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func invocationAPIFixture(t *testing.T) (*Server, store.Principal, store.Deployment, invocation.CreateRequest) {
	t.Helper()
	db := actionsDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "invocation-api")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{Name: "invocation-api", Services: map[string]spec.Service{"report": {Image: "busybox@sha256:" + strings.Repeat("a", 64), Job: &spec.Job{TimeoutSeconds: 60, Invocation: &spec.JobInvocation{AllowedIdentities: []string{admin.ID}, InputKeys: []string{"payload"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	db.ValidateDeployment = func(context.Context, store.Application, spec.Application) error { return nil }
	d, err := db.Accept(ctx, admin, "demo", "development", app, 0, "api-initial", app)
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
	_, key, err := db.CreateKey(ctx, admin, store.KeyInput{Name: "invocation-app", Project: "demo", Environment: "development", Application: app.Name, Permissions: []string{"jobs:invoke", "jobs:read", "jobs:cancel", "jobs:logs"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db, Auth: AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))}}
	in := invocation.CreateRequest{ExpectedImage: app.Services["report"].Image, ExpectedRevision: 1, OwnerScope: "team-one", CorrelationID: "run-one", Inputs: map[string]json.RawMessage{"payload": json.RawMessage(`{"private":"fixture-value"}`)}}
	return s, p, d, in
}
func TestInvocationAPIReceiptRecoveryAndOwnerBoundaries(t *testing.T) {
	s, p, d, in := invocationAPIFixture(t)
	mux := http.NewServeMux()
	s.registerInvocationRoutes(mux)
	root := "/api/v1/applications/" + d.ApplicationID + "/services/report/invocations"
	call := func(method, path, owner string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("X-Hakopod-Owner-Scope", owner)
		req.Header.Set("Idempotency-Key", "request-fixture")
		req = req.WithContext(context.WithValue(req.Context(), principalKey{}, p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	body, _ := json.Marshal(in)
	w := call("POST", root, in.OwnerScope, body)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "fixture-value") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("receipt exposed input or cacheable response")
	}
	var v invocation.Record
	if json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal("invalid receipt")
	}
	if got := call("GET", root+"/"+v.ID, "other-team", nil); got.Code != 404 {
		t.Fatal("cross-owner receipt", got.Code)
	}
	if got := call("POST", root+"/"+v.ID+"/cancel", "other-team", nil); got.Code != 404 {
		t.Fatal("cross-owner cancellation", got.Code)
	}
	if got := call("GET", root+"?correlation_id=run-one&active=true", in.OwnerScope, nil); got.Code != 200 || !strings.Contains(got.Body.String(), v.ID) {
		t.Fatal("recovery failed", got.Code)
	}
	if got := call("GET", root+"/"+v.ID+"/logs", in.OwnerScope, nil); got.Code != 409 {
		t.Fatal("nonterminal logs exposed")
	}
	if got := call("POST", root+"/"+v.ID+"/cancel", in.OwnerScope, nil); got.Code != 202 {
		t.Fatal("cancel rejected")
	}
	if got := call("POST", root, in.OwnerScope, []byte(`{"inputs":{"payload":"SECRET-MARKER"},"unknown":1}`)); got.Code != 400 || strings.Contains(got.Body.String(), "SECRET-MARKER") {
		t.Fatal("invalid JSON exposed input")
	}
}

type invocationWorkerFixture struct {
	claim                      *store.InvocationClaim
	starts, observes, cleanups int
	logFailure                 bool
}

func (f *invocationWorkerFixture) StartInvocation(ctx context.Context, r invocation.Record, input []byte, before func(context.Context) error) (invocation.RuntimeState, error) {
	if err := before(ctx); err != nil {
		return invocation.RuntimeState{}, err
	}
	f.starts++
	return invocation.RuntimeState{Status: invocation.Running, NamespaceUID: "ns-one", RuntimeUID: "job-one"}, nil
}
func (f *invocationWorkerFixture) ObserveInvocation(context.Context, invocation.Record) (invocation.RuntimeState, error) {
	f.observes++
	if f.logFailure {
		return invocation.RuntimeState{Status: invocation.Failed}, errors.New("private fixture transport detail")
	}
	return invocation.RuntimeState{Status: invocation.Succeeded, Log: []byte("private fixture output")}, nil
}

func TestInvocationPublicStatusWaitsForCleanup(t *testing.T) {
	now := time.Now()
	v := publicInvocation(invocation.Record{Status: invocation.Cancelled, CleanupPending: true, FinishedAt: &now})
	if v.Status != invocation.Running || v.FinishedAt != nil || !v.CleanupPending {
		t.Fatal("public receipt ended before cleanup")
	}
}

func TestInvocationWorkerTerminalLogFailureStillCleans(t *testing.T) {
	s, p, d, in := invocationAPIFixture(t)
	ctx := context.Background()
	r, err := s.Store.EnqueueInvocation(ctx, p, d.ApplicationID, "report", "logs-failure", in, s.authEncryptionKey())
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Store.ClaimInvocation(ctx)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	fake := &invocationWorkerFixture{claim: c, logFailure: true}
	bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	s.runInvocation(bounded, c, fake)
	owner, _ := invocation.OwnerHash(in.OwnerScope)
	got, err := s.Store.ReadInvocation(ctx, p, d.ApplicationID, "report", owner, r.ID, "jobs:read")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != invocation.Failed || got.CleanupPending || !got.LogTruncated || fake.cleanups == 0 || strings.Contains(got.Message, "private fixture") {
		t.Fatal("terminal log failure did not produce a redacted cleaned receipt")
	}
}
func (f *invocationWorkerFixture) CleanupInvocation(context.Context, invocation.Record) (bool, error) {
	f.cleanups++
	past := time.Now().Add(-2 * time.Minute)
	f.claim.Record.FinishedAt = &past
	return true, nil
}

func TestInvocationWorkerPersistsEncryptedReceiptBeforeCleanup(t *testing.T) {
	s, p, d, in := invocationAPIFixture(t)
	ctx := context.Background()
	r, err := s.Store.EnqueueInvocation(ctx, p, d.ApplicationID, "report", "worker-fixture", in, s.authEncryptionKey())
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Store.ClaimInvocation(ctx)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	fake := &invocationWorkerFixture{claim: c}
	bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	s.runInvocation(bounded, c, fake)
	owner, _ := invocation.OwnerHash(in.OwnerScope)
	got, err := s.Store.ReadInvocation(ctx, p, d.ApplicationID, "report", owner, r.ID, "jobs:read")
	if err != nil {
		t.Fatal(err)
	}
	if fake.starts != 1 || fake.observes != 1 || fake.cleanups < 1 || got.Status != invocation.Succeeded || got.CleanupPending || len(got.EncryptedInput) != 0 {
		t.Fatal("worker lifecycle incomplete", got.Status, fake)
	}
	plain, err := invocation.Open(s.authEncryptionKey(), got.ID, "logs", got.EncryptedLogs)
	if err != nil || string(plain) != "private fixture output" {
		t.Fatal("terminal logs not encrypted", err)
	}
}
