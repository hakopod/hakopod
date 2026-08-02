package spec

import (
	"strings"
	"testing"
)

func TestShowcaseIsBoundedRealSample(t *testing.T) {
	app, err := Showcase()
	if err != nil {
		t.Fatal(err)
	}
	if app.Name != "shop" || len(app.Services) != 2 {
		t.Fatal("unexpected sample shape")
	}
	for name, s := range app.Services {
		if s.Size != "small" || s.Replicas != 1 || s.Volume != nil || len(s.Secrets) != 0 || s.Env["HAKOPOD_SAMPLE"] != "true" || !strings.Contains(s.Image, "@sha256:") {
			t.Fatalf("%s sample is not bounded, stateless and pinned", name)
		}
	}
	web, api := app.Services["web"], app.Services["api"]
	if !web.Public || api.Public || web.Env["API_URL"] != "http://api:8080" || len(web.DependsOn) != 1 || web.DependsOn[0] != "api" {
		t.Fatal("sample exposure/dependency incorrect")
	}
	if !strings.Contains(web.Args[1], "Sample") || !strings.Contains(api.Args[0], "order_created") {
		t.Fatal("sample labels or checkout contract missing")
	}
}
