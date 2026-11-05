package spec

import (
	"slices"
	"testing"
)

func TestPlausibleIndependentDatabases(t *testing.T) {
	for _, database := range []string{"bundled", "external"} {
		for _, events := range []string{"bundled", "external"} {
			t.Run(database+"/"+events, func(t *testing.T) {
				app, err := PlanTemplate("plausible", TemplateOptions{Name: "stats", Public: true, StorageGiB: 7, SiteURL: "https://stats.example.com", Values: map[string]string{
					"database-mode": database, "events-mode": events,
				}})
				if err != nil {
					t.Fatal(err)
				}
				if app, err = Normalize(app); err != nil {
					t.Fatal(err)
				}
				main := app.Services["main"]
				required := []string{"secret-key-base"}
				for name, mode := range map[string]string{"db": database, "events": events} {
					service, exists := app.Services[name]
					if exists != (mode == "bundled") || exists && service.Volume.SizeGiB != 7 || slices.Contains(main.DependsOn, name) != exists {
						t.Fatalf("%s does not match the database selection", name)
					}
				}
				if _, bound := main.Bindings["DATABASE_URL"]; bound != (database == "bundled") {
					t.Fatal("DATABASE_URL must come from the binding only for bundled PostgreSQL")
				}
				if database == "bundled" {
					required = append(required, "database-password")
				} else {
					required = append(required, "database-url")
				}
				if events == "bundled" {
					required = append(required, "clickhouse-password")
				} else {
					required = append(required, "clickhouse-url")
				}
				slices.Sort(required)
				if got := TemplateSecretNames(app); !slices.Equal(got, required) {
					t.Fatalf("secrets = %v, want %v", got, required)
				}
			})
		}
	}
}

func TestPlausibleClickHouseURL(t *testing.T) {
	for value, valid := range map[string]bool{
		"https://plausible:secret@clickhouse.internal:8443/plausible_events_db": true,
		"http://default:secret@events:8123/plausible_events_db":                 true,
		"https://plausible:secret@clickhouse.internal:8443/":                    false,
		"https://plausible@clickhouse.internal:8443/plausible_events_db":        false,
		"tcp://plausible:secret@clickhouse.internal:9000/plausible_events_db":   false,
	} {
		if err := ValidateTemplateSecret("plausible", "clickhouse-url", value); (err == nil) != valid {
			t.Errorf("%s: %v", value, err)
		}
	}
}
