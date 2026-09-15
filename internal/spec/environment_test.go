package spec

import (
	"fmt"
	"github.com/pelletier/go-toml/v2"
	"reflect"
	"strings"
	"testing"
)

func TestApplicationEnvironmentInheritance(t *testing.T) {
	app, err := Parse([]byte(`schema_version=1
name="example"
inject_env=true
[env]
MODE="production"
REGION="eu"
OVERRIDE="inherited"
[secrets.SHARED_TOKEN]
ref="shared-token"
[secrets.MODE_LOCAL]
ref="mode-local"
[services.api]
image="nginx:alpine"
[services.api.env]
MODE=""
MODE_LOCAL="plain"
[services.api.secrets.OVERRIDE]
ref="local-override"
[services.worker]
image="nginx:alpine"
`))
	if err != nil {
		t.Fatal(err)
	}
	runtime := RuntimeEnvironment(app)
	api := runtime.Services["api"]
	if !reflect.DeepEqual(api.Env, map[string]string{"MODE": "", "REGION": "eu", "MODE_LOCAL": "plain"}) {
		t.Fatalf("wrong overrides: %+v", api.Env)
	}
	if api.Secrets["SHARED_TOKEN"].Ref != "shared-token" || api.Secrets["OVERRIDE"].Ref != "local-override" || len(api.Secrets) != 2 {
		t.Fatal("secret overrides lost")
	}
	if runtime.Services["worker"].Env["MODE"] != "production" || runtime.Services["worker"].Secrets["MODE_LOCAL"].Ref != "mode-local" {
		t.Fatal("worker defaults missing")
	}
	if runtime.Env != nil || runtime.Secrets != nil {
		t.Fatal("runtime still has defaults")
	}
	api.Env["REGION"] = "changed"
	api.Secrets["SHARED_TOKEN"] = SecretRef{Ref: "changed"}
	if app.Env["REGION"] != "eu" || app.Secrets["SHARED_TOKEN"].Ref != "shared-token" || len(app.Services["api"].Env) != 2 {
		t.Fatal("runtime mutated desired config")
	}
	data, err := toml.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	roundtrip, err := Parse(data)
	if err != nil || !reflect.DeepEqual(roundtrip, app) {
		t.Fatal("inheritance lost through TOML round trip", err)
	}
	changed := app
	changed.Env = map[string]string{"REGION": "us"}
	found := false
	for _, diff := range Diff(&app, changed) {
		if diff.Field == "env" {
			found = true
		}
	}
	if !found {
		t.Fatal("shared environment changes missing from review")
	}
}

func TestApplicationEnvironmentValidationAndPreview(t *testing.T) {
	base := Application{InjectEnv: true, SchemaVersion: 1, Name: "test", Services: map[string]Service{"api": {Image: "nginx", Env: map[string]string{}}}}
	for i := 0; i < 128; i++ {
		base.Services["api"].Env[fmt.Sprintf("KEY_%d", i)] = "value"
	}
	base.Env = map[string]string{"EXTRA": "value"}
	if _, err := Normalize(base); err == nil || !strings.Contains(err.Error(), "128") {
		t.Fatal("effective variable limit bypassed", err)
	}
	base.Env = map[string]string{"KEY_0": "fallback"}
	if _, err := Normalize(base); err != nil {
		t.Fatal("overridden variable counted twice", err)
	}
	base.Env = map[string]string{"DATABASE_URL": "postgres://name:password@db/database"}
	if _, err := Normalize(base); err == nil {
		t.Fatal("sensitive shared variable accepted")
	}
	base.Env = nil
	base.Secrets = map[string]SecretRef{"TOKEN": {Key: "remote-value", Provider: "vault"}}
	normalized, err := Normalize(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePreview(normalized); err == nil {
		t.Fatal("preview inherited external secret")
	}
	native := normalized
	native.Secrets = map[string]SecretRef{"TOKEN": {Ref: "native-value"}}
	if got := TemplateSecretNames(native); !reflect.DeepEqual(got, []string{"native-value"}) {
		t.Fatal("shared secret missing from prerequisites", got)
	}
	normalized.Services["api"] = Service{Image: "nginx", Size: "small", Secrets: map[string]SecretRef{"TOKEN": {Ref: "local"}}}
	if err := ValidatePreview(normalized); err != nil {
		t.Fatal("local override should make preview safe", err)
	}
}
