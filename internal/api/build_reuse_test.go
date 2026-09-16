package api

import (
	"encoding/json"
	"testing"
)

func TestVerifiedBuildImageReuseCompatibility(t *testing.T) {
	original := buildConfig{ID: "build", Revision: 1, Provider: "github", Repository: "owner/repo", Branch: "main", Mode: "dockerfile", Dockerfile: "Dockerfile", Architecture: "amd64", ManagedRegistry: "registry.example"}
	run := buildRun{ConfigRevision: 1, Config: original}
	current := original
	current.Revision = 2
	current.ReuseServices = []string{"worker", "migrate"}
	env := map[string]string{"MODE": "production"}
	current.Env = &env
	command := []string{"uvicorn"}
	current.Command = &command
	if !compatibleBuildImage(current, run) {
		t.Fatal("runtime-only image reuse needs unnecessary rebuild")
	}
	current.BuildArgs = map[string]string{}
	current.BuildSecrets = map[string]string{}
	if !compatibleBuildImage(current, run) {
		t.Fatal("empty optional build inputs need unnecessary rebuild")
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot buildConfig
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Revision = 1
	if !compatibleBuildImage(current, buildRun{ConfigRevision: 1, Config: snapshot}) {
		t.Fatal("stored JSON changed artifact equivalence")
	}
	for _, change := range []func(*buildConfig){func(c *buildConfig) { c.Repository = "other/repo" }, func(c *buildConfig) { c.Branch = "other" }, func(c *buildConfig) { c.Dockerfile = "Otherfile" }, func(c *buildConfig) { c.Architecture = "arm64" }, func(c *buildConfig) { c.ManagedRegistry = "other.example" }, func(c *buildConfig) { c.ConnectionID = "other" }, func(c *buildConfig) { c.BuildArgs = map[string]string{"MODE": "changed"} }} {
		next := current
		change(&next)
		if compatibleBuildImage(next, run) {
			t.Fatal("changed artifact inputs reused old image")
		}
	}
	run.Config = buildConfig{}
	if compatibleBuildImage(current, run) {
		t.Fatal("missing artifact provenance accepted")
	}
}

func TestBuildReuseRejectsDefaultPrimaryService(t *testing.T) {
	_, err := normalizeBuild(buildInput{ApplicationID: "existing", ReuseServices: []string{"web"}})
	if err == nil || err.Error() != "invalid input: reuse_services: choose unique additional services in this application" {
		t.Fatalf("default primary service accepted as an additional target: %v", err)
	}
}
