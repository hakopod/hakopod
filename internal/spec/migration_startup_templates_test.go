package spec

import (
	"reflect"
	"testing"
)

func TestIntegratedMigrationTemplatesDeclareStartupBudgets(t *testing.T) {
	for _, test := range []struct {
		id           string
		dependencies []string
	}{
		{id: "infisical", dependencies: []string{"db", "redis"}},
		{id: "glitchtip", dependencies: []string{"postgres", "valkey"}},
	} {
		t.Run(test.id, func(t *testing.T) {
			var template Template
			for _, candidate := range Templates() {
				if candidate.ID == test.id {
					template = candidate
					break
				}
			}
			app, err := PlanTemplate(test.id, TemplateOptions{Name: "migration-test", SiteURL: "https://migration.example.test", Values: catalogTestValues(template)})
			if err != nil {
				t.Fatal(err)
			}
			main := app.Services["main"]
			if main.StartupTimeoutSeconds != 600 || main.Job != nil || !reflect.DeepEqual(main.DependsOn, test.dependencies) {
				t.Fatalf("integrated migration lifecycle changed: %+v", main)
			}
			if test.id == "glitchtip" && main.Env["SERVER_ROLE"] != "all_in_one" {
				t.Fatal("GlitchTip migration, web and worker roles were split")
			}
		})
	}
}
