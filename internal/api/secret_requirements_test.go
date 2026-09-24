package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestDeploymentSecretSetupCreatesWithoutRotation(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	token, err := db.Bootstrap(ctx, "secret-setup")
	if err != nil {
		t.Fatal(err)
	}
	kube := templateSecretKube(t)
	db.ValidateDeployment = func(ctx context.Context, app store.Application, next spec.Application) error {
		return kube.ValidateWorkloadSecrets(ctx, app.Project, app.Environment, next)
	}
	h := (&Server{Store: db, Cluster: kube}).Handler()
	call := func(method, path string, body any, status int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, "http://localhost/api/v1"+path, bytes.NewReader(store.JSON(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", "secret-setup-review")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	app := spec.Application{Name: "custom", SchemaVersion: 1, Services: map[string]spec.Service{"main": {Image: "nginx:stable", Port: 8080, Secrets: map[string]spec.SecretRef{"PASSWORD": {Ref: "password"}}}}}
	input := map[string]any{"project": "demo", "environment": "development", "spec": app, "expected_revision": 0}
	var plan struct {
		Missing []string `json:"missing_secrets"`
	}
	if err = json.Unmarshal(call("POST", "/plan", input, 200), &plan); err != nil || len(plan.Missing) != 1 {
		t.Fatal("missing plan", err)
	}
	call("POST", "/deployments", input, 409)
	path := "/secrets/password?project=demo&environment=development&application=custom"
	call("POST", path, map[string]any{"value": "both", "generate": true}, 400)
	response := call("POST", path, map[string]any{"generate": true, "format": "hex"}, 201)
	if bytes.Contains(response, []byte(`"value"`)) {
		t.Fatal("creation disclosed value")
	}
	values, err := kube.ReadWorkloadSecrets(ctx, "demo", "development", "custom", []string{"password"})
	if err != nil || len(values["password"]) != 64 {
		t.Fatal("generation failed", err)
	}
	call("POST", path, map[string]any{"generate": true}, 409)
	after, err := kube.ReadWorkloadSecrets(ctx, "demo", "development", "custom", []string{"password"})
	if err != nil || after["password"] != values["password"] {
		t.Fatal("secret rotated", err)
	}
	result := call("POST", "/deployment-secret-requirements", map[string]any{"project": "demo", "environment": "development", "spec": app}, 200)
	if json.Unmarshal(result, &plan) != nil || len(plan.Missing) != 0 {
		t.Fatal("saved secret still missing")
	}
	response = call("POST", "/deployments", input, 202)
	if bytes.Contains(response, []byte(values["password"])) {
		t.Fatal("value entered deployment")
	}
	// Every valid name remains available; requirements is not a reserved secret.
	call("POST", "/secrets/requirements?project=demo&environment=development&application=custom", map[string]any{"value": "fixture-requirements"}, 201)
	named, err := kube.ReadWorkloadSecrets(ctx, "demo", "development", "custom", []string{"requirements"})
	if err != nil || named["requirements"] != "fixture-requirements" {
		t.Fatal("secret name collided with the preflight route", err)
	}
}
