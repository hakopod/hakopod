//go:build ignore

// Run from the repository root in the development build environment.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/pelletier/go-toml/v2"
)

func main() {
	var template spec.Template
	for _, item := range spec.Templates() {
		if item.ID == "mathesar" {
			template = item
		}
	}
	if template.ID == "" {
		panic("Mathesar catalog entry missing")
	}
	plans := map[string]any{}
	for _, database := range []string{"bundled", "external"} {
		for _, storage := range []string{"local", "rwx"} {
			values := map[string]string{"database-mode": database, "media-storage-mode": "local"}
			if storage == "rwx" {
				values["media-storage-mode"] = "shared"
				values["media-storage-class"] = "fixture-rwx"
			}
			if database == "external" {
				for key, value := range map[string]string{"database-host": "postgres.example.invalid", "database-port": "5432", "database-name": "mathesar_django", "database-user": "mathesar", "database-sslmode": "verify-full"} {
					values[key] = value
				}
			}
			options := spec.TemplateOptions{Name: "fixture-mathesar", StorageGiB: 5, Architecture: "arm64", SiteURL: "https://mathesar.example.invalid", Values: values}
			application, err := spec.PlanTemplate("mathesar", options)
			if err != nil {
				panic(fmt.Sprintf("%s/%s: %v", database, storage, err))
			}
			canonical, err := toml.Marshal(application)
			if err != nil {
				panic(err)
			}
			warnings := append([]string{template.Verification, template.ResourceSummary}, template.Requirements...)
			plans[database+"-"+storage] = map[string]any{"application_id": "", "expected_revision": 0, "spec": application, "toml": string(canonical), "configuration": options, "changes": spec.Diff(nil, application), "warnings": warnings, "resource_profiles": spec.Profiles, "required_secrets": spec.TemplateSecretNames(application), "model_source": ""}
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"items": []spec.Template{template}, "plans": plans}); err != nil {
		panic(err)
	}
}
