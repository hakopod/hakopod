package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestBindingConnectionCLIUsesRevisionAndFailedOutcome(t *testing.T) {
	application := store.Application{ID: "fixture-app", Revision: 4, Spec: spec.Application{Services: map[string]spec.Service{"api": {Bindings: map[string]spec.Binding{"DATABASE_URL": {Protocol: "postgres"}}}}}}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/applications/fixture-app/services/api/bindings/DATABASE_URL/test" {
			t.Error("connection test selected the wrong runtime")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 2 || body["expected_revision"] != float64(4) || body["pod"] != "api-fixture" {
			t.Error("connection test did not preserve its revision and pod")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"outcome": "failed", "stages": []any{}})
	}))
	defer server.Close()
	c := &client{url: server.URL, http: server.Client()}
	err := testBindingConnection(context.Background(), c, application, "api", "DATABASE_URL", "api-fixture")
	var exit *exitError
	if !errors.As(err, &exit) || calls != 1 {
		t.Fatal("failed verification must fail the CLI command")
	}
	if testBindingConnection(context.Background(), c, application, "api", "UNRELATED_SECRET", "api-fixture") == nil || calls != 1 {
		t.Fatal("CLI tested an undeclared secret")
	}
}
