package spec

import (
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestSharedCatalogParametersAreDataAndBounded(t *testing.T) {
	options := TemplateOptions{Name: "dns", Values: map[string]string{"domains": "www.example.test,api.example.test"}}
	app, err := PlanTemplate("cloudflare-ddns", options)
	if err != nil {
		t.Fatal(err)
	}
	if app.Services["main"].Env["DOMAINS"] != options.Values["domains"] || app.Services["main"].Public || app.Services["main"].Port != 0 {
		t.Fatal("DDNS config or worker boundary changed")
	}
	for _, values := range []map[string]string{nil, {"domains": " "}, {"domains": "a\nb"}, {"domains": strings.Repeat("a", 2049)}, {"domains": "{{config.another}}"}, {"domains": "example.test", "unknown": "value"}} {
		options.Values = values
		if _, err := PlanTemplate("cloudflare-ddns", options); err == nil {
			t.Fatal("invalid or unknown config accepted")
		}
	}
	// Quotes are encoded by the TOML marshaler, never concatenated into source.
	options.Values = map[string]string{"domains": "example.test\" [services.extra]"}
	app, err = PlanTemplate("cloudflare-ddns", options)
	if err != nil {
		t.Fatal(err)
	}
	data, err := toml.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Parse(data)
	if err != nil || len(decoded.Services) != 1 {
		t.Fatal("configuration escaped its string value", err)
	}
}

func TestSharedCatalogPlansContainNoTemplateMarkers(t *testing.T) {
	for _, template := range Templates() {
		if !template.Deployable {
			continue
		}
		o := TemplateOptions{Name: "catalog", Values: catalogTestValues(template), Model: "owner/model", ModelRevision: strings.Repeat("a", 40)}
		if template.SiteURLSupported {
			o.SiteURL = "https://service.example.test"
		}
		app, err := PlanTemplate(template.ID, o)
		if err != nil {
			t.Fatal(template.ID, err)
		}
		data, err := toml.Marshal(app)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "{{config.") || strings.Contains(string(data), "{{site_host}}") || strings.Contains(string(data), "https://catalog.example.test") {
			t.Fatal("unrendered blueprint", template.ID)
		}
	}
}

func TestSharedCatalogLongSigningMaterial(t *testing.T) {
	for _, tc := range []struct{ id, name string }{{"doublezero", "secret-key-base"}, {"rustrak", "session-secret"}} {
		if err := ValidateTemplateSecret(tc.id, tc.name, strings.Repeat("a", 63)); err == nil {
			t.Fatal("short signing material accepted")
		}
		if err := ValidateTemplateSecret(tc.id, tc.name, strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
	}
}
