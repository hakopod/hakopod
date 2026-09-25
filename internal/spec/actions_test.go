package spec

import (
	"github.com/pelletier/go-toml/v2"
	"reflect"
	"slices"
	"testing"
)

func actionsFixture() Application {
	return Application{SchemaVersion: 1, Name: "runners", Services: map[string]Service{"runner": {Actions: &Actions{Repository: "team/repository", Credential: "github-token"}}}}
}
func TestActionsCanonicalAndCredentialIsolation(t *testing.T) {
	app := actionsFixture()
	normalized, err := Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	runner := normalized.Services["runner"]
	if runner.Image != ActionsRunnerImage || runner.Size != "compute" || runner.Replicas != 1 || runner.Actions.TimeoutMinutes != 60 {
		t.Fatal(runner)
	}
	if !slices.Contains(LocalSecretNames(normalized), "github-token") {
		t.Fatal("missing control-plane credential requirement")
	}
	if len(SecretReferences(runner)) != 0 {
		t.Fatal("provider credential may reach workload snapshot")
	}
	encoded, err := toml.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Parse(encoded)
	if err != nil || !reflect.DeepEqual(normalized, decoded) {
		t.Fatal("TOML roundtrip", err)
	}
	if ValidatePreview(normalized) == nil {
		t.Fatal("runner pool copied into preview")
	}
}
func TestActionsRejectUnsafeWorkloadControls(t *testing.T) {
	for name, mutate := range map[string]func(*Service){
		"image":            func(s *Service) { s.Image = "ubuntu:latest" },
		"public":           func(s *Service) { s.Public = true },
		"env":              func(s *Service) { s.Env = map[string]string{"TOKEN": "value"} },
		"mount":            func(s *Service) { s.Volume = &Volume{SizeGiB: 1, MountPath: "/work"} },
		"command":          func(s *Service) { s.Command = []string{"sh"} },
		"replicas":         func(s *Service) { s.Replicas = 11 },
		"memory":           func(s *Service) { s.Resources = &Resources{MemoryLimit: "1Gi"} },
		"invalid-memory":   func(s *Service) { s.Resources = &Resources{MemoryLimit: "bad"} },
		"invalid-cpu":      func(s *Service) { s.Resources = &Resources{CPURequest: "bad"} },
		"overhead":         func(s *Service) { s.Resources = &Resources{MemoryRequest: "1Mi"} },
		"provider-url":     func(s *Service) { s.Actions.Repository = "https://evil.invalid/repo" },
		"timeout":          func(s *Service) { s.Actions.TimeoutMinutes = 361 },
		"duplicate-labels": func(s *Service) { s.Actions.Labels = []string{"Linux", "linux"} },
	} {
		t.Run(name, func(t *testing.T) {
			a := actionsFixture()
			s := a.Services["runner"]
			mutate(&s)
			a.Services["runner"] = s
			if _, err := Normalize(a); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	app := actionsFixture()
	app.Services["web"] = Service{Image: "nginx:alpine"}
	if _, err := Normalize(app); err == nil {
		t.Fatal("mixed privileged namespace")
	}
}
func TestActionsTemplateRequiresRepositoryAndManualCredential(t *testing.T) {
	if _, err := PlanTemplate("managed-actions", TemplateOptions{Name: "runners"}); err == nil {
		t.Fatal("missing repository accepted")
	}
	if _, err := PlanTemplate("managed-actions", TemplateOptions{Name: "runners", Values: map[string]string{"repository": "team/repo", "unexpected": "value"}}); err == nil {
		t.Fatal("unknown input accepted")
	}
	field, ok := TemplateSecretFieldByName("managed-actions", "github-runner-token")
	if !ok || field.Generate {
		t.Fatal("provider token must be supplied manually")
	}
}
