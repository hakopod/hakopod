package spec

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestCustomHostnamesValidation(t *testing.T) {
	base := "schema_version = 1\nname = \"shop\"\n\n[services.web]\nimage = \"nginx\"\n"
	cases := map[string]string{
		"port = 8080\npublic = true\n[services.web.custom_hostnames]\nenabled = true\n":                                             "",
		"port = 8080\npublic = true\n[services.web.custom_hostnames]\nenabled = true\nmax = 5000\ntarget = \"edge.shop.example\"\n": "",
		"port = 8080\n[services.web.custom_hostnames]\nenabled = true\n":                                                            "public service with a port",
		"public = true\n[services.web.custom_hostnames]\nenabled = true\n":                                                          "public service with a port",
		"port = 8080\npublic = true\n[services.web.custom_hostnames]\nenabled = true\nmax = -1\n":                                   "custom_hostnames.max",
		"port = 8080\npublic = true\n[services.web.custom_hostnames]\nenabled = true\nmax = 50001\n":                                "custom_hostnames.max",
		"port = 8080\npublic = true\n[services.web.custom_hostnames]\nenabled = true\ntarget = \"*.example.com\"\n":                 "custom_hostnames.target",
		"port = 8080\npublic = true\n[services.web.custom_hostnames]\nenabled = true\n[services.web.serverless]\n":                  "serverless",
		"[services.web.job]\n[services.web.custom_hostnames]\nenabled = true\n":                                                     "custom_hostnames",
	}
	for extra, want := range cases {
		_, err := Parse([]byte(base + extra))
		if want == "" {
			if err != nil {
				t.Fatalf("%q: %v", extra, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: got %v, want %q", extra, err, want)
		}
	}
}

func TestCustomHostnamesDefaultsDiffAndRoundTrip(t *testing.T) {
	a, err := Parse([]byte("schema_version = 1\nname = \"shop\"\n[services.web]\nimage = \"nginx\"\nport = 8080\npublic = true\n[services.web.custom_hostnames]\nenabled = true\n"))
	if err != nil {
		t.Fatal(err)
	}
	c := a.Services["web"].CustomHostnames
	if c == nil || !c.Enabled || c.Limit() != DefaultCustomHostnamesMax || !CustomHostnamesEnabled(a.Services["web"]) {
		t.Fatalf("custom_hostnames not parsed with defaults: %+v", c)
	}
	data, err := toml.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(data)
	if err != nil || !reflect.DeepEqual(back, a) {
		t.Fatalf("TOML round trip: %v", err)
	}
	b, _ := Normalize(a)
	web := b.Services["web"]
	web.CustomHostnames = nil
	b.Services["web"] = web
	found := false
	for _, change := range Diff(&a, b) {
		found = found || change.Field == "custom_hostnames"
	}
	if !found {
		t.Fatal("removing custom_hostnames is not visible in the plan")
	}
}
