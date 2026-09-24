package spec

import (
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestCatalogRequirementsAndPersistentConstraints(t *testing.T) {
	for _, template := range Templates() {
		options := TemplateOptions{Name: "template-test", Public: true, StorageGiB: 5, Model: "Qwen/Qwen3-0.6B", ModelRevision: strings.Repeat("a", 40), Architecture: "arm64"}
		if template.SiteURLSupported {
			options.SiteURL = "https://workspace.example.test"
		}
		options.Values = catalogTestValues(template)
		if template.Deployable && !slices.Contains(template.Architectures, options.Architecture) && len(template.Architectures) > 0 {
			options.Architecture = template.Architectures[0]
		}
		a, err := PlanTemplate(template.ID, options)
		if !template.Deployable {
			if err == nil {
				t.Fatal("guided template silently deployed")
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", template.ID, err)
		}
		expected := append([]string(nil), template.RequiredSecrets...)
		sort.Strings(expected)
		actual := TemplateSecretNames(a)
		if len(expected) != len(actual) || len(expected) > 0 && !reflect.DeepEqual(expected, actual) {
			t.Fatalf("%s secret prerequisites do not match the final specification: %v / %v", template.ID, expected, actual)
		}
		for name, s := range a.Services {
			if !strings.Contains(s.Image, "@sha256:") || s.Architecture != options.Architecture {
				t.Fatalf("%s/%s lacks immutable artifact or requested architecture", template.ID, name)
			}
			if (template.Category == "database" || name != "main") && s.Public {
				t.Fatal("database dependency exposed publicly")
			}
			if s.Volume != nil {
				changed, _ := Normalize(a)
				s.Replicas = 2
				changed.Services[name] = s
				if _, err = Normalize(changed); err == nil {
					t.Fatal("unsafe multi-writer persistent deployment allowed")
				}
			}
		}
	}
	a, _ := FromTemplate("postgresql", "db", false, 5, "", "")
	s := a.Services["main"]
	s.Volume.MountPath = "/etc"
	a.Services["main"] = s
	if _, err := Normalize(a); err == nil {
		t.Fatal("system directory mount allowed")
	}
	if _, err := FromTemplate("vllm", "model", false, 30, "Qwen/Qwen3-0.6B", "main"); err == nil {
		t.Fatal("mutable model revision accepted")
	}
}

func TestAgentTemplateCredentialsAndExternalModels(t *testing.T) {
	for _, bad := range []TemplateOptions{
		{Name: "agent", Model: "gpt-5", Provider: "openai-compatible", ProviderURL: "http://provider.example/v1"},
		{Name: "agent", Model: "gpt-5", Provider: "openai-compatible", ProviderURL: "https://key@provider.example/v1"},
		{Name: "agent", Model: "gpt-5", Provider: "openai-compatible", ProviderURL: "https://provider.example/v1?key=x"},
		{Name: "agent", Model: "gpt-5", Provider: "unrecognized"},
		{Name: "agent", Model: ""},
		{Name: "agent", Model: "model;another"},
		{Name: "agent", Model: "gpt-5", Architecture: "x86"},
	} {
		if _, err := PlanTemplate("open-webui", bad); err == nil {
			t.Fatalf("unsafe provider configuration accepted: %+v", bad)
		}
	}
	a, err := PlanTemplate("open-webui", TemplateOptions{Name: "agent", Model: "provider/model-1", Provider: "openai-compatible", ProviderURL: "https://provider.example/v1"})
	if err != nil {
		t.Fatal(err)
	}
	s := a.Services["main"]
	if s.Env["DEFAULT_MODELS"] != "provider/model-1" || s.Env["OPENAI_API_BASE_URL"] != "https://provider.example/v1" || s.Env["HF_HUB_OFFLINE"] != "1" || s.Env["ENABLE_OLLAMA_API"] != "false" || s.Secrets["OPENAI_API_KEY"].Ref != "provider-key" {
		t.Fatal("model/provider selection or explicit credential isolation lost")
	}
	for _, id := range []string{"infisical", "flowise"} {
		if _, err := PlanTemplate(id, TemplateOptions{Name: "tool"}); err == nil {
			t.Fatal("missing stable HTTPS site URL accepted", id)
		}
	}
}
