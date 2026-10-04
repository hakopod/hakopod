package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestMathesarReviewedTemplateRequiresScopedCredentials(t *testing.T) {
	db := sourceDatabase(t)
	raw, err := db.Bootstrap(context.Background(), "mathesar-template-fixture")
	if err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: db, Cluster: templateSecretKube(t, "arm64")}).Handler()
	call := func(method, path string, input any, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(store.JSON(input)))
		r.Header.Set("Authorization", "Bearer "+raw)
		r.Header.Set("Idempotency-Key", "mathesar-template-fixture")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		if out.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, out.Code, want, out.Body.String())
		}
		return out.Body.Bytes()
	}
	cfg := templateConfiguration{Project: "demo", Environment: "development", TemplateOptions: spec.TemplateOptions{
		Name: "tables", SiteURL: "https://tables.example.test", Architecture: "arm64",
		Values: map[string]string{"media-storage-class": "test-storage"},
	}}
	var plan struct {
		TOML          string                `json:"toml"`
		Configuration templateConfiguration `json:"configuration"`
		Required      []string              `json:"required_secrets"`
	}
	if err := json.Unmarshal(call("POST", "/templates/mathesar/plan", cfg, 200), &plan); err != nil {
		t.Fatal(err)
	}
	if strings.Join(plan.Required, ",") != "database-password,secret-key" || strings.Contains(plan.TOML, "{{") {
		t.Fatal("plan did not render shared storage and required secret references")
	}
	external := cfg
	external.Name = "external-tables"
	external.Values = map[string]string{"media-storage-class": "test-storage", "database-mode": "external", "database-host": "postgres.example.test"}
	var externalPlan struct {
		Spec spec.Application `json:"spec"`
	}
	if err := json.Unmarshal(call("POST", "/templates/mathesar/plan", external, 200), &externalPlan); err != nil || len(externalPlan.Spec.Services) != 2 || externalPlan.Spec.Services["db"].Image != "" {
		t.Fatal("API plan did not honor the existing database choice", err)
	}
	deploy := map[string]any{"configuration": plan.Configuration, "toml": plan.TOML, "expected_revision": 0}
	call("POST", "/templates/mathesar/deploy", deploy, 400)
	for _, name := range plan.Required {
		response := call("PUT", "/templates/mathesar/secrets/"+name+"?project=demo&environment=development&application=tables", map[string]bool{"generate": true}, 200)
		if bytes.Contains(response, []byte(`"value"`)) {
			t.Fatal("secret write returned credential material")
		}
	}
	wrongScope := plan.Configuration
	wrongScope.Name = "other-tables"
	var other struct {
		TOML string `json:"toml"`
	}
	if err := json.Unmarshal(call("POST", "/templates/mathesar/plan", wrongScope, 200), &other); err != nil {
		t.Fatal(err)
	}
	call("POST", "/templates/mathesar/deploy", map[string]any{"configuration": wrongScope, "toml": other.TOML, "expected_revision": 0}, 400)
	var count int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM deployments").Scan(&count); err != nil || count != 0 {
		t.Fatal("missing credentials queued a deployment", err)
	}
	first := call("POST", "/templates/mathesar/deploy", deploy, 202)
	second := call("POST", "/templates/mathesar/deploy", deploy, 202)
	var accepted, replayed store.Deployment
	if json.Unmarshal(first, &accepted) != nil || json.Unmarshal(second, &replayed) != nil || accepted.ID == "" || accepted.ID != replayed.ID {
		t.Fatal("reviewed deployment did not preserve durable idempotency")
	}
}
