package spec

import "testing"

func TestTemplateURLRejectsInvalidExplicitPorts(t *testing.T) {
	for _, port := range []string{"", "0", "65536", "999999999999999999999"} {
		t.Run("port-"+port, func(t *testing.T) {
			origin := "https://tables.example.test:" + port
			if _, err := templateURL(origin); err == nil {
				t.Fatal("unusable explicit URL port accepted")
			}
			if _, err := PlanTemplate("mathesar", TemplateOptions{
				Name: "tables", SiteURL: origin,
				Values: map[string]string{"media-storage-class": "shared-media"},
			}); err == nil {
				t.Fatal("deployment plan accepted an unusable canonical origin")
			}
		})
	}
}

func TestTemplateURLPreservesUsableOrigins(t *testing.T) {
	for _, origin := range []string{
		"https://tables.example.test",
		"https://tables.example.test:1",
		"https://tables.example.test:443",
		"https://tables.example.test:8443",
		"https://tables.example.test:65535",
	} {
		t.Run(origin, func(t *testing.T) {
			got, err := templateURL(origin + "/")
			if err != nil || got != origin {
				t.Fatal("usable HTTPS origin was rejected or changed", err)
			}
		})
	}
}
