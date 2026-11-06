package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestExportPlansBackToTheSameSpecification(t *testing.T) {
	app, err := spec.Parse([]byte(`schema_version = 1
name = "kole"

[services.kole]
image = "ghcr.io/syne/kole@sha256:` + strings.Repeat("a", 64) + `"
port = 8000
public = true

[services.kole.env]
KOLE_APP_API_URL = "http://app:3001/api"

[services.kole.secrets.ANTHROPIC_API_KEY]
ref = "anthropic-key"
`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exportTOML(store.Application{Name: "kole", Project: "demo", Environment: "production", Revision: 12, Spec: app})
	if err != nil {
		t.Fatal(err)
	}
	back, err := spec.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if changes := spec.Diff(&app, back); len(changes) != 0 {
		t.Fatalf("export does not round-trip: %+v\n%s", changes, out)
	}
	if !strings.HasPrefix(string(out), "# Exported from demo/production application kole at revision 12.") {
		t.Fatal(string(out))
	}
}

func TestSecretValueComesOnlyFromFileOrStdin(t *testing.T) {
	if _, err := readSecretValue("", false, nil); err == nil {
		t.Fatal("no source accepted")
	}
	if _, err := readSecretValue("x", true, nil); err == nil {
		t.Fatal("two sources accepted")
	}
	v, err := readSecretValue("", true, strings.NewReader("sk-live-123\n"))
	if err != nil || v != "sk-live-123" {
		t.Fatal(v, err)
	}
	if _, err := readSecretValue("", true, strings.NewReader("\n")); err == nil {
		t.Fatal("empty value accepted")
	}
}

func TestRestartSetImageAndExecPodSelection(t *testing.T) {
	var restartBody, planBody map[string]any
	var restartKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/applications/app/services/kole/restart":
			restartKey = r.Header.Get("Idempotency-Key")
			json.NewDecoder(r.Body).Decode(&restartBody)
			io.WriteString(w, `{"id":"d1","revision":13,"status":"queued"}`)
		case r.URL.Path == "/api/v1/plan":
			json.NewDecoder(r.Body).Decode(&planBody)
			io.WriteString(w, `{"expected_revision":12,"spec":{"name":"kole","services":{"kole":{"image":"ghcr.io/syne/kole:2"}}},"changes":[{"service":"kole","field":"image","before":"ghcr.io/syne/kole:1","after":"ghcr.io/syne/kole:2"}]}`)
		case r.URL.Path == "/api/v1/deployments":
			io.WriteString(w, `{"id":"d2","revision":13,"status":"queued"}`)
		case r.URL.Path == "/api/v1/applications/app/services/kole/runtime":
			io.WriteString(w, `{"pods":[{"name":"kole-7f-abc","ready":true},{"name":"kole-6c-old","ready":false}]}`)
		default:
			t.Fatal(r.URL.Path)
		}
	}))
	defer server.Close()
	c := &client{url: server.URL, key: "k", http: server.Client()}
	cfg := config{Project: "demo", Environment: "production"}
	a := store.Application{ID: "app", Name: "kole", Revision: 12, Spec: spec.Application{Name: "kole", Services: map[string]spec.Service{"kole": {Image: "ghcr.io/syne/kole:1"}}}}
	if _, err := restartService(context.Background(), c, a, "kole", "retry-key-123"); err != nil {
		t.Fatal(err)
	}
	if restartBody["expected_revision"] != float64(12) || restartKey != "retry-key-123" {
		t.Fatal(restartBody, restartKey)
	}
	if _, err := setImage(context.Background(), c, cfg, a, "kole", "ghcr.io/syne/kole:2", "retry-key-456", true); err != nil {
		t.Fatal(err)
	}
	if planBody["spec"].(map[string]any)["services"].(map[string]any)["kole"].(map[string]any)["image"] != "ghcr.io/syne/kole:2" {
		t.Fatal(planBody)
	}
	if a.Spec.Services["kole"].Image != "ghcr.io/syne/kole:1" {
		t.Fatal("set-image changed the caller's application")
	}
	if _, err := setImage(context.Background(), c, cfg, a, "kole", "ghcr.io/syne/kole:1", "retry-key-789", true); err == nil {
		t.Fatal("no-op image change accepted")
	}
	if pod, err := readyPod(context.Background(), c, a, "kole"); err != nil || pod != "kole-7f-abc" {
		t.Fatal(pod, err)
	}
	if svc, err := soleService(a, ""); err != nil || svc != "kole" {
		t.Fatal(svc, err)
	}
}
