package spec

import (
	"github.com/pelletier/go-toml/v2"
	"reflect"
	"strings"
	"testing"
)

func TestManagedBindingOptionsRoundTripAndSecretDiscovery(t *testing.T) {
	input := []byte(`schema_version = 1
name = "managed-binding-development-fixture"
[services.api]
image = "nginx:alpine"
[services.api.bindings.DATABASE_URL]
managed_database = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
protocol = "postgres"
endpoint = "read_write"
username = "infisical_user"
database = "infisical"
ssl_mode = "verify-full"
[services.api.bindings.DATABASE_URL.password]
ref = "database-password"
`)
	app, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	b := app.Services["api"].Bindings["DATABASE_URL"]
	if b.Username != "infisical_user" || b.Database != "infisical" || b.SSLMode != "verify-full" || b.Password.Ref != "database-password" {
		t.Fatal("connection choices lost")
	}
	for _, names := range [][]string{LocalSecretNames(app), TemplateSecretNames(app)} {
		if !reflect.DeepEqual(names, []string{"database-password"}) {
			t.Fatal("binding password missing from setup requirements")
		}
	}
	encoded, err := toml.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(encoded)
	if err != nil || !reflect.DeepEqual(app, again) {
		t.Fatal("TOML did not preserve managed connection options", err)
	}
}

func TestManagedBindingOptionValidation(t *testing.T) {
	base := Binding{ManagedDatabase: strings.Repeat("a", 32), Endpoint: "read_write", Protocol: "postgres"}
	cases := []struct {
		name   string
		change func(*Binding)
		valid  bool
	}{
		{"unchanged defaults", func(b *Binding) {}, true},
		{"default username", func(b *Binding) { b.Username = "app" }, true},
		{"custom database", func(b *Binding) { b.Database = "restored" }, true},
		{"custom credentials", func(b *Binding) { b.Username = "restored_user"; b.Password = &SecretRef{Ref: "restored-password"} }, true},
		{"external credentials", func(b *Binding) {
			b.Username = "restored_user"
			b.Password = &SecretRef{Provider: "vault", Path: "databases", Key: "password"}
		}, true},
		{"missing custom password", func(b *Binding) { b.Username = "restored_user" }, false},
		{"invalid secret", func(b *Binding) { b.Password = &SecretRef{Ref: "bad/ref"} }, false},
		{"ambiguous secret", func(b *Binding) { b.Password = &SecretRef{Ref: "password", Provider: "vault", Key: "password"} }, false},
		{"username controls", func(b *Binding) { b.Username = "app\n" }, false},
		{"database injection", func(b *Binding) { b.Database = "app?sslmode=disable" }, false},
		{"long database", func(b *Binding) { b.Database = strings.Repeat("x", 65) }, false},
		{"pooled defaults", func(b *Binding) {
			b.Endpoint = "pooled_read_write"
			b.Username = "app"
			b.Database = "app"
			b.Password = &SecretRef{Ref: "password"}
		}, true},
		{"pooled custom database", func(b *Binding) { b.Endpoint = "pooled_read_write"; b.Database = "restored" }, false},
		{"pooled custom user", func(b *Binding) {
			b.Endpoint = "pooled_read_write"
			b.Username = "restored_user"
			b.Password = &SecretRef{Ref: "password"}
		}, false},
		{"require", func(b *Binding) { b.SSLMode = "require" }, true},
		{"verify ca", func(b *Binding) { b.SSLMode = "verify-ca" }, true},
		{"verify full", func(b *Binding) { b.SSLMode = "verify-full" }, true},
		{"disable checked by database policy", func(b *Binding) { b.SSLMode = "disable" }, true},
		{"opportunistic ssl", func(b *Binding) { b.SSLMode = "prefer" }, false},
		{"redis index", func(b *Binding) { b.Protocol = "redis"; b.Database = "15"; b.SSLMode = "verify-full" }, true},
		{"redis cluster", func(b *Binding) {
			b.Protocol = "redis"
			b.Endpoint = "cluster"
			b.ClusterAware = true
			b.Database = "0"
		}, true},
		{"redis cluster nonzero", func(b *Binding) {
			b.Protocol = "redis"
			b.Endpoint = "cluster"
			b.ClusterAware = true
			b.Database = "1"
		}, false},
		{"redis negative", func(b *Binding) { b.Protocol = "redis"; b.Database = "-1" }, false},
		{"redis require unsupported", func(b *Binding) { b.Protocol = "redis"; b.SSLMode = "require" }, false},
		{"mysql driver tls", func(b *Binding) { b.Protocol = "mysql"; b.SSLMode = "verify-full" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := base
			tc.change(&b)
			a := Application{Name: "bindings", Services: map[string]Service{"api": {Image: "nginx:alpine", Bindings: map[string]Binding{"DATABASE_URL": b}}}}
			_, err := Normalize(a)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestManagedSSLModeCannotLeakToOtherBindingKinds(t *testing.T) {
	for _, b := range []Binding{
		{Service: "db", Protocol: "postgres", Username: "app", Database: "app", Password: &SecretRef{Ref: "password"}, SSLMode: "verify-full"},
		{ExternalDatabase: strings.Repeat("a", 32), ExternalDatabaseRevision: 1, Protocol: "postgres", SSLMode: "verify-full"},
	} {
		a := Application{Name: "bindings", Services: map[string]Service{"api": {Image: "nginx:alpine", Bindings: map[string]Binding{"DATABASE_URL": b}}, "db": {Image: "postgres:17", Port: 5432}}}
		if _, err := Normalize(a); err == nil {
			t.Fatal("unsupported SSL option accepted")
		}
	}
}
