package spec

import (
	"encoding/json"
	"strings"
	"testing"
)

const minimum = `name = "demo"
[services.web]
image = "python:3.13-alpine"
port = 8080
public = true
`

func TestMinimalDefaultsAndWorker(t *testing.T) {
	app, err := Parse([]byte(minimum + "\n[services.worker]\nimage='python:3.13-alpine'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if app.SchemaVersion != 1 || app.Services["web"].Size != "small" || app.Services["web"].Replicas != 1 || app.Services["web"].Networks[0] != "default" {
		t.Fatalf("defaults not applied: %+v", app)
	}
	if app.Services["worker"].Port != 0 || app.Services["worker"].Public {
		t.Fatal("worker acquired network exposure")
	}
}

func TestStrictValidation(t *testing.T) {
	cases := []struct{ name, source, contains string }{
		{"unknown", minimum + "privileged=true", "services.web.privileged"},
		{"schema", "schema_version=99\n" + minimum, "unsupported version"},
		{"port", strings.Replace(minimum, "8080", "65536", 1), ".port"},
		{"worker_public", strings.Replace(minimum, "port = 8080", "", 1), "requires port"},
		{"undeclared_network", minimum + `networks=["backend"]`, "undeclared"},
		{"empty_network", minimum + `networks=[]`, "empty list"},
		{"duplicate_network", minimum + `networks=["default","default"]`, "duplicate"},
		{"cycle", minimum + `depends_on=["api"]
[services.api]
image="python"
depends_on=["web"]`, "cycle"},
		{"secret", minimum + "\n[services.web.env]\nDATABASE_PASSWORD='do-not-disclose'", "secret values"},
		{"credential_url", minimum + "\n[services.web.env]\nDATABASE_URL='postgres://me:do-not-disclose@db/app'", "secret values"},
		{"invalid_secret_ref", minimum + "\n[services.web.secrets]\nDATABASE_URL={ref='../db'}", "invalid"},
		{"unsafe_health", minimum + `healthcheck="//outside.example/"`, "HTTP path"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.source))
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("expected %q, got %v", test.contains, err)
			}
			if strings.Contains(err.Error(), "do-not-disclose") {
				t.Fatal("validation exposed a secret value")
			}
		})
	}
}

func TestTOMLErrorDoesNotIncludeValues(t *testing.T) {
	_, err := Parse([]byte(minimum + "\n[services.web.env]\nTOKEN='secret-do-not-leak'\nUNKNOWN = { invalid 'secret-do-not-leak' }"))
	if err == nil || strings.Contains(err.Error(), "secret-do-not-leak") {
		t.Fatalf("unsafe parse error: %v", err)
	}
}

func TestNormalizeDoesNotMutateRevisionAndDiffRedacts(t *testing.T) {
	before, err := Parse([]byte(minimum + "\n[services.web.env]\nMODE='before'"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := Normalize(before)
	if err != nil {
		t.Fatal(err)
	}
	svc := after.Services["web"]
	svc.Env["MODE"] = "after"
	svc.Networks[0] = "other"
	after.Services["web"] = svc
	if before.Services["web"].Env["MODE"] != "before" || before.Services["web"].Networks[0] != "default" {
		t.Fatal("normalization shares mutable revision state")
	}
	changes := Diff(&before, after)
	data, _ := json.Marshal(changes)
	if strings.Contains(string(data), `"MODE":"before"`) || strings.Contains(string(data), `"MODE":"after"`) {
		t.Fatalf("diff exposes values: %s", data)
	}
	if !strings.Contains(string(data), "[redacted]") {
		t.Fatalf("missing env diff: %s", data)
	}
}

func TestNetworksAndDependencies(t *testing.T) {
	app, err := Parse([]byte(`name="shop"
[networks.backend]
internal=true
[services.api]
image="python"
port=8080
networks=["backend"]
[services.web]
image="python"
port=8080
networks=["default","backend"]
depends_on=["api"]
`))
	if err != nil {
		t.Fatal(err)
	}
	order, err := Order(app)
	if err != nil || strings.Join(order, ",") != "api,web" {
		t.Fatalf("incorrect dependency order: %v, %v", order, err)
	}
	if len(Warnings(app)) != 1 || !strings.Contains(Warnings(app)[0], "egress-enabled") {
		t.Fatal("mixed internal/external membership needs an explicit warning")
	}
}

func TestAutoscalingBounds(t *testing.T) {
	app, err := Parse([]byte(minimum + "\n[services.web.autoscaling]\nmax_replicas=5\n"))
	if err != nil {
		t.Fatal(err)
	}
	a := app.Services["web"].Autoscaling
	if a.MinReplicas != 1 || a.TargetCPU != 70 {
		t.Fatalf("missing autoscaling defaults: %+v", a)
	}
	_, err = Parse([]byte(minimum + "\n[services.web.autoscaling]\nmax_replicas=21\n"))
	if err == nil {
		t.Fatal("unbounded autoscaling accepted")
	}
}

func TestExplicitEmptyServiceTableForRemoval(t *testing.T) {
	app, err := Parse([]byte("schema_version=1\nname='retire-app'\n[services]\n"))
	if err != nil || app.Services == nil || len(app.Services) != 0 {
		t.Fatal("explicit empty TOML services rejected", err)
	}
	if _, err = Parse([]byte("schema_version=1\nname='retire-app'\n")); err == nil {
		t.Fatal("omitted services accepted as removal")
	}
	before, err := Parse([]byte("name='retire-app'\n[services.api]\nimage='python:3.13'\n"))
	if err != nil {
		t.Fatal(err)
	}
	changes := Diff(&before, app)
	if len(changes) == 0 {
		t.Fatal("final-service removal missing from review diff")
	}
}
