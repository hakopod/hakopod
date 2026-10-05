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

func TestOutpostReviewedProviderCredentials(t *testing.T) {
	db := sourceDatabase(t)
	key, err := db.Bootstrap(context.Background(), "outpost-template-fixture")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: db, Cluster: templateSecretKube(t, "amd64")}
	handler := server.Handler()
	call := func(method, path string, input any, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(store.JSON(input)))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Idempotency-Key", "outpost-template-fixture")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != want {
			t.Fatalf("%s %s: got %d, want %d", method, path, out.Code, want)
		}
		if bytes.Contains(out.Body.Bytes(), []byte("provider-fixture-password")) {
			t.Fatal("API response exposed provider credentials")
		}
		return out.Body.Bytes()
	}
	cfg := templateConfiguration{Project: "demo", Environment: "development", TemplateOptions: spec.TemplateOptions{
		Name: "events", Values: map[string]string{"redis-host": "redis.example.test"},
	}}
	var plan struct {
		TOML          string                `json:"toml"`
		Spec          spec.Application      `json:"spec"`
		Configuration templateConfiguration `json:"configuration"`
		Required      []string              `json:"required_secrets"`
	}
	for _, database := range []string{"bundled", "external"} {
		for _, redis := range []string{"bundled", "external"} {
			for _, broker := range []string{"bundled", "external"} {
				cfg.Values["database-mode"], cfg.Values["redis-mode"], cfg.Values["broker-mode"] = database, redis, broker
				plan.Spec = spec.Application{}
				if err := json.Unmarshal(call("POST", "/templates/outpost/plan", cfg, 200), &plan); err != nil {
					t.Fatal(err)
				}
				for service, mode := range map[string]string{"db": database, "redis": redis, "broker": broker} {
					if _, exists := plan.Spec.Services[service]; exists != (mode == "bundled") {
						t.Fatal("review differs from provider selection", service)
					}
				}
				if strings.Contains(plan.TOML, "{{") || plan.Spec.Services["migrate"].Job == nil {
					t.Fatal("review is not a complete migration-aware specification")
				}
			}
		}
	}
	deploy := map[string]any{"configuration": plan.Configuration, "toml": plan.TOML, "expected_revision": 0}
	call("POST", "/templates/outpost/deploy", deploy, 400)
	for _, name := range plan.Required {
		input := map[string]any{"generate": true}
		switch name {
		case "database-url":
			call("PUT", "/templates/outpost/secrets/"+name+"?project=demo&environment=development&application=events", input, 400)
			input = map[string]any{"value": "postgres://outpost:provider-fixture-password@db.example.test/outpost?sslmode=verify-full"}
		case "broker-url":
			input = map[string]any{"value": "amqps://outpost:provider-fixture-password@broker.example.test/outpost"}
		}
		call("PUT", "/templates/outpost/secrets/"+name+"?project=demo&environment=development&application=events", input, 200)
	}
	values, err := server.Cluster.ReadWorkloadSecrets(context.Background(), "demo", "development", "events", plan.Required)
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.ValidateTemplateSecretSet("outpost", plan.Required, values); err != nil || len(values["encryption-secret"]) != 32 {
		t.Fatal("saved credentials cannot configure Outpost", err)
	}
	other := cfg
	other.Name = "other-events"
	var otherPlan struct {
		TOML string `json:"toml"`
	}
	if err := json.Unmarshal(call("POST", "/templates/outpost/plan", other, 200), &otherPlan); err != nil {
		t.Fatal(err)
	}
	call("POST", "/templates/outpost/deploy", map[string]any{"configuration": other, "toml": otherPlan.TOML, "expected_revision": 0}, 400)
	var accepted, replayed store.Deployment
	if json.Unmarshal(call("POST", "/templates/outpost/deploy", deploy, 202), &accepted) != nil ||
		json.Unmarshal(call("POST", "/templates/outpost/deploy", deploy, 202), &replayed) != nil || accepted.ID == "" || accepted.ID != replayed.ID {
		t.Fatal("reviewed Outpost deployment did not preserve idempotency")
	}
}
