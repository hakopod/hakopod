// Generates synthetic UI review data from the real catalog and planner.
// It does not connect to a cluster or simulate a successful deployment.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/pelletier/go-toml/v2"
)

func main() {
	plans := map[string]any{}
	for _, database := range []string{"bundled", "external"} {
		for _, redis := range []string{"bundled", "external"} {
			for _, broker := range []string{"bundled", "external"} {
				options := spec.TemplateOptions{Name: "fixture-outpost", Architecture: "arm64", Values: map[string]string{
					"database-mode": database, "redis-mode": redis, "broker-mode": broker, "redis-host": "redis.example.invalid",
				}}
				app, err := spec.PlanTemplate("outpost", options)
				if err != nil {
					panic(err)
				}
				canonical, err := toml.Marshal(app)
				if err != nil {
					panic(err)
				}
				warnings := []string{"Development UI fixture. No deployment or provider connection is performed."}
				for _, template := range spec.Templates() {
					if template.ID == "outpost" {
						warnings = append(warnings, template.Verification, template.ResourceSummary)
						warnings = append(warnings, template.Requirements...)
					}
				}
				plans[strings.Join([]string{database, redis, broker}, "-")] = map[string]any{
					"application_id": "", "expected_revision": 0, "spec": app, "toml": string(canonical),
					"changes": spec.Diff(nil, app), "warnings": warnings, "resource_profiles": spec.Profiles,
					"required_secrets": spec.TemplateSecretNames(app), "model_source": "",
				}
			}
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"items": spec.Templates(), "plans": plans}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
