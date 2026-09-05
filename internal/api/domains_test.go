package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestDomainProofReservationAndRollback(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	db.ProtectedDomains = []string{"apps.example.test", "dashboard.example.test"}
	raw, err := db.Bootstrap(ctx, "domain-test")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	next, err := spec.Normalize(spec.Application{Name: "domain-test", Services: map[string]spec.Service{"web": {Image: "python:3.13-alpine", Public: true, Port: 8080}}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Accept(ctx, p, "demo", "development", next, 0, "domain-start")
	if err != nil {
		t.Fatal(err)
	}
	a, err := db.Application(ctx, d.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"apps.example.test", "service.apps.example.test", "dashboard.example.test", "https://app.example.test", "*.example.test", "127.0.0.1", "A.example.test"} {
		if _, err = db.BeginDomainVerification(ctx, p, a, host, "web"); err == nil {
			t.Fatal("invalid or reserved hostname accepted", host)
		}
	}
	next.Domains = map[string]string{"store.example.test": "web"}
	if _, err = db.Accept(ctx, p, a.Project, a.Environment, next, 1, "domain-unverified"); !errors.Is(err, store.ErrInput) {
		t.Fatal("unverified raw spec bypassed proof", err)
	}
	v, err := db.BeginDomainVerification(ctx, p, a, "store.example.test", "web")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db}
	s.domainLookupTXT = func(context.Context, string) ([]string, error) { return []string{"wrong"}, nil }
	handler := s.Handler()
	verify := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/applications/"+a.ID+"/domains/store.example.test/verify", bytes.NewBufferString(`{}`))
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := verify(); w.Code != 409 {
		t.Fatal("wrong DNS record verified", w.Code, w.Body.String())
	}
	s.domainLookupTXT = func(_ context.Context, host string) ([]string, error) {
		if host != "_hakopod.store.example.test" {
			t.Fatal(host)
		}
		return []string{v.Token}, nil
	}
	if w := verify(); w.Code != 200 {
		t.Fatal("valid DNS record denied", w.Code, w.Body.String())
	}
	d, err = db.Accept(ctx, p, a.Project, a.Environment, next, 1, "domain-verified")
	if err != nil {
		t.Fatal(err)
	}
	copyNext, err := spec.Normalize(next)
	if err != nil {
		t.Fatal(err)
	}
	copyNext.Name = "other-app"
	if _, err = db.Accept(ctx, p, a.Project, a.Environment, copyNext, 0, "domain-collision"); !errors.Is(err, store.ErrConflict) {
		t.Fatal("another app claimed reserved hostname", err)
	}
	next.Domains = nil
	if _, err = db.Accept(ctx, p, a.Project, a.Environment, next, d.Revision, "domain-remove-route"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Accept(ctx, p, a.Project, a.Environment, copyNext, 0, "domain-collision-again"); !errors.Is(err, store.ErrConflict) {
		t.Fatal("historical route reservation released before rollback retirement", err)
	}
	next.Domains = map[string]string{"store.example.test": "web"}
	if _, err = db.Accept(ctx, p, a.Project, a.Environment, next, 3, "domain-reapply"); err != nil {
		t.Fatal("same app could not reapply domain", err)
	}
	data, _ := json.Marshal(next)
	parsed, err := spec.Parse([]byte("name='domain-toml'\n[domains]\n'store.example.test'='web'\n[services.web]\nimage='python:3.13-alpine'\nport=8080\npublic=true\n"))
	if err != nil || parsed.Domains["store.example.test"] != "web" || !strings.Contains(string(data), "domains") {
		t.Fatal("custom domain round trip", err)
	}
}
