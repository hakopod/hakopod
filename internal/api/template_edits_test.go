package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/pelletier/go-toml/v2"
)

type editedTemplatePlan struct {
	Spec          spec.Application      `json:"spec"`
	TOML          string                `json:"toml"`
	Configuration templateConfiguration `json:"configuration"`
	Required      []string              `json:"required_secrets"`
	Missing       []string              `json:"missing_secrets"`
	Revision      int64                 `json:"expected_revision"`
}

func TestEditedTemplateReviewRemovalAndScopedSecrets(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	token, err := db.Bootstrap(ctx, "edited-template-fixture")
	if err != nil {
		t.Fatal(err)
	}
	kube := templateSecretKube(t)
	db.ValidateDeployment = func(ctx context.Context, app store.Application, next spec.Application) error {
		return kube.ValidateWorkloadSecrets(ctx, app.Project, app.Environment, next)
	}
	handler := (&Server{Store: db, Cluster: kube}).Handler()
	call := func(path string, body any, want int) []byte {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/v1/templates/infisical/"+path, bytes.NewReader(store.JSON(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", "edited-template-fixture")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s returned %d, want %d: %s", path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	cfg := templateConfiguration{Project: "demo", Environment: "development", TemplateOptions: spec.TemplateOptions{Name: "edited-template-fixture", SiteURL: "https://fixture.example.test"}}
	var generated editedTemplatePlan
	if err = json.Unmarshal(call("plan", cfg, 200), &generated); err != nil {
		t.Fatal(err)
	}
	if len(generated.Spec.Services) < 3 {
		t.Fatal("fixture must begin with bundled services")
	}
	edited, err := spec.Normalize(spec.Application{Name: cfg.Name, Services: map[string]spec.Service{"main": {Image: "nginx:alpine", Env: map[string]string{"CUSTOM_SETTING": "edited"}, Secrets: map[string]spec.SecretRef{"KEY": {Ref: "encryption-key"}, "CUSTOM": {Ref: "custom-token"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := toml.Marshal(edited)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TOML = "# user's scratch comment\n" + string(raw)
	var plan editedTemplatePlan
	if err = json.Unmarshal(call("plan", cfg, 200), &plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Required, []string{"custom-token", "encryption-key"}) || !reflect.DeepEqual(plan.Missing, plan.Required) || len(plan.Spec.Services) != 1 || !reflect.DeepEqual(plan.Spec, edited) {
		t.Fatal("review restored removed template services or secret requirements")
	}
	if plan.Configuration.TOML != plan.TOML || strings.Contains(plan.Configuration.TOML, "scratch comment") {
		t.Fatal("review did not canonicalize the edited draft")
	}
	deploy := map[string]any{"configuration": plan.Configuration, "toml": plan.TOML, "expected_revision": plan.Revision}
	call("deploy", deploy, 400)
	if err = kube.PutWorkloadSecret(ctx, cfg.Project, cfg.Environment, "another-application", "custom-token", "foreign-fixture"); err != nil {
		t.Fatal(err)
	}
	if err = kube.PutWorkloadSecret(ctx, cfg.Project, cfg.Environment, cfg.Name, "encryption-key", strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
	call("deploy", deploy, 400)
	if err = kube.PutWorkloadSecret(ctx, cfg.Project, cfg.Environment, cfg.Name, "custom-token", "application-specific-fixture"); err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(plan.TOML, "CUSTOM_SETTING", "OTHER_SETTING", 1)
	call("deploy", map[string]any{"configuration": plan.Configuration, "toml": changed, "expected_revision": 0}, 409)
	var accepted, replay store.Deployment
	first := call("deploy", deploy, 202)
	if err = json.Unmarshal(first, &accepted); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(call("deploy", deploy, 202), &replay); err != nil || replay.ID != accepted.ID {
		t.Fatal("edited deploy retry not idempotent", err)
	}
	if !reflect.DeepEqual(accepted.Spec, plan.Spec) {
		t.Fatal("deployment differs from reviewed edits")
	}
	if bytes.Contains(first, []byte("application-specific-fixture")) {
		t.Fatal("secret value entered deployment response")
	}
	wrong := cfg
	wrong.TOML = strings.Replace(string(raw), cfg.Name, "another-name", 1)
	call("plan", wrong, 400)
	invalid := cfg
	invalid.TOML = "name = invalid-sensitive-value"
	response := call("plan", invalid, 400)
	if bytes.Contains(response, []byte("invalid-sensitive-value")) {
		t.Fatal("invalid TOML leaked source contents")
	}
}

func TestEditedTemplateExistingApplicationRevisionAndExactSpec(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	token, err := db.Bootstrap(ctx, "edited-existing-fixture")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{Name: "edited-existing", Services: map[string]spec.Service{"existing": {Image: "nginx:alpine", Port: 8080}}})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := db.Accept(ctx, principal, "demo", "development", app, 0, "edited-existing-create")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal("fixture claim unavailable", err)
	}
	pinned, _ := spec.Normalize(app)
	service := pinned.Services["existing"]
	service.Image = "nginx@sha256:" + strings.Repeat("a", 64)
	pinned.Services["existing"] = service
	if _, err = claim.SetResolved(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if err = claim.Finish(ctx, "succeeded", "", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	handler := (&Server{Store: db, Cluster: templateSecretKube(t)}).Handler()
	call := func(path string, body any, want int) []byte {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/v1/templates/nginx/"+path, bytes.NewReader(store.JSON(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", "edited-existing-deploy")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s returned %d, want %d: %s", path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	revision := int64(1)
	cfg := templateConfiguration{Project: "demo", Environment: "development", ApplicationID: initial.ApplicationID, ExpectedRevision: &revision, TemplateOptions: spec.TemplateOptions{Name: app.Name}}
	var generated editedTemplatePlan
	if err = json.Unmarshal(call("plan", cfg, 200), &generated); err != nil {
		t.Fatal(err)
	}
	next := generated.Spec
	delete(next.Services, "existing")
	main := next.Services["main"]
	main.Env = map[string]string{"CUSTOM": "retained"}
	next.Services["main"] = main
	raw, _ := toml.Marshal(next)
	cfg.TOML = string(raw)
	var plan editedTemplatePlan
	if err = json.Unmarshal(call("plan", cfg, 200), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Spec.Services) != 1 || plan.Spec.Services["existing"].Image != "" {
		t.Fatal("edited review remerged removed services")
	}
	wrong := cfg
	wrong.ApplicationID = strings.Repeat("f", 32)
	call("plan", wrong, 404)
	zero := int64(0)
	wrong = cfg
	wrong.ExpectedRevision = &zero
	call("plan", wrong, 409)
	deploy := map[string]any{"configuration": plan.Configuration, "toml": plan.TOML, "expected_revision": 1}
	var accepted, replay store.Deployment
	if err = json.Unmarshal(call("deploy", deploy, 202), &accepted); err != nil || accepted.Revision != 2 {
		t.Fatal("edited existing application not deployed", err)
	}
	if err = json.Unmarshal(call("deploy", deploy, 202), &replay); err != nil || replay.ID != accepted.ID {
		t.Fatal("existing edited retry failed", err)
	}
	call("plan", cfg, 409)
}

func TestEditedTemplateManyApplicationSecretsUseOrdinaryPreflight(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	token, err := db.Bootstrap(ctx, "edited-many-secrets-fixture")
	if err != nil {
		t.Fatal(err)
	}
	kube := templateSecretKube(t)
	db.ValidateDeployment = func(ctx context.Context, app store.Application, next spec.Application) error {
		return kube.ValidateWorkloadSecrets(ctx, app.Project, app.Environment, next)
	}
	handler := (&Server{Store: db, Cluster: kube}).Handler()
	call := func(path string, body any, want int) []byte {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/v1/templates/infisical/"+path, bytes.NewReader(store.JSON(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", "edited-many-secrets-fixture")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s returned %d, want %d: %s", path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	cfg := templateConfiguration{Project: "demo", Environment: "development", TemplateOptions: spec.TemplateOptions{Name: "edited-many-secrets-fixture", SiteURL: "https://fixture.example.test"}}
	services := map[string]spec.Service{
		"main":   {Image: "nginx:alpine", Secrets: map[string]spec.SecretRef{"KEY": {Ref: "encryption-key"}}},
		"worker": {Image: "nginx:alpine", Secrets: map[string]spec.SecretRef{}},
	}
	const missingName = "custom-39"
	const secretMarker = "generic-development-secret-value"
	genericValue := secretMarker + strings.Repeat("x", (16<<10)-len(secretMarker))
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("custom-%02d", i)
		service := "main"
		if i >= 20 {
			service = "worker"
		}
		services[service].Secrets[fmt.Sprintf("CUSTOM_%02d", i)] = spec.SecretRef{Ref: name}
		application := cfg.Name
		if name == missingName {
			application = "another-application"
		}
		if err = kube.PutWorkloadSecret(ctx, cfg.Project, cfg.Environment, application, name, genericValue); err != nil {
			t.Fatal(err)
		}
	}
	const invalidKey = "invalid-catalog-key-development-fixture"
	if err = kube.PutWorkloadSecret(ctx, cfg.Project, cfg.Environment, cfg.Name, "encryption-key", invalidKey); err != nil {
		t.Fatal(err)
	}
	next, err := spec.Normalize(spec.Application{Name: cfg.Name, Services: services})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := toml.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TOML = string(raw)
	var plan editedTemplatePlan
	if err = json.Unmarshal(call("plan", cfg, 200), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Required) != 41 || !reflect.DeepEqual(plan.Missing, []string{missingName}) {
		t.Fatal("review lost full application requirements or accepted a foreign secret")
	}
	deploy := map[string]any{"configuration": plan.Configuration, "toml": plan.TOML, "expected_revision": plan.Revision}
	response := call("deploy", deploy, 400)
	if !bytes.Contains(response, []byte("encryption-key")) || bytes.Contains(response, []byte(invalidKey)) {
		t.Fatal("retained catalog credential validation was skipped or leaked its value")
	}
	if err = kube.PutWorkloadSecret(ctx, cfg.Project, cfg.Environment, cfg.Name, "encryption-key", strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
	response = call("deploy", deploy, 400)
	if !bytes.Contains(response, []byte(missingName)) {
		t.Fatal("ordinary deployment preflight did not reject the missing generic reference")
	}
	if err = kube.PutWorkloadSecret(ctx, cfg.Project, cfg.Environment, cfg.Name, missingName, genericValue); err != nil {
		t.Fatal(err)
	}
	// The full application has more than 32 refs and 512 KiB of secret values,
	// while each service remains within the ordinary runtime snapshot bounds.
	response = call("deploy", deploy, 202)
	var accepted store.Deployment
	if err = json.Unmarshal(response, &accepted); err != nil || !reflect.DeepEqual(accepted.Spec, plan.Spec) {
		t.Fatal("valid full application was not accepted unchanged", err)
	}
	if bytes.Contains(response, []byte(secretMarker)) {
		t.Fatal("generic secret values entered the deployment response")
	}
}
