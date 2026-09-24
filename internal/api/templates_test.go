package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestTemplatePlanScopeBeforeMetadataAndProviderContract(t *testing.T) {
	restricted := store.Principal{ID: "restricted", Admin: true, Project: "allowed", Permissions: []string{"deployments:write"}}
	request := httptest.NewRequest("POST", "/api/v1/templates/vllm/plan", strings.NewReader(`{"project":"other","environment":"development","name":"agent","model":"Qwen/Qwen3-0.6B"}`))
	request.SetPathValue("id", "vllm")
	request = request.WithContext(context.WithValue(request.Context(), principalKey{}, restricted))
	response := httptest.NewRecorder()
	(&Server{}).planTemplate(response, request)
	if response.Code != 403 {
		t.Fatalf("out-of-scope request reached metadata or state lookup: %d", response.Code)
	}
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "catalog-plan")
	if err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: db, Cluster: templateSecretKube(t, "arm64")}).Handler()
	call := func(body string) (int, []byte) {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/v1/templates/open-webui/plan", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code, w.Body.Bytes()
	}
	code, body := call(`{"project":"demo","environment":"development","name":"agent","provider":"openai-compatible","provider_url":"https://provider.example.test/v1","model":"provider/model-x","architecture":"arm64"}`)
	var plan struct {
		Spec        spec.Application `json:"spec"`
		ModelSource string           `json:"model_source"`
		Required    []string         `json:"required_secrets"`
	}
	if err = json.Unmarshal(body, &plan); err != nil || code != 200 {
		t.Fatalf("template plan failed: %d %s", code, body)
	}
	if plan.ModelSource != "" || plan.Spec.Services["main"].Env["DEFAULT_MODELS"] != "provider/model-x" || strings.Join(plan.Required, ",") != "provider-key,session-secret" {
		t.Fatal("provider model or scoped secret plan is incorrect")
	}
	code, _ = call(`{"project":"demo","environment":"development","name":"agent","model":"provider/model-x","provider_api_key":"never-store-literal-credentials"}`)
	if code != 400 {
		t.Fatal("literal provider credential field accepted")
	}
}
