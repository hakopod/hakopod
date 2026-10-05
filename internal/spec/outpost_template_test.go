package spec

import (
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestOutpostIndependentDependencies(t *testing.T) {
	for _, database := range []string{"bundled", "external"} {
		for _, redis := range []string{"bundled", "external"} {
			for _, broker := range []string{"bundled", "external"} {
				t.Run(database+"/"+redis+"/"+broker, func(t *testing.T) {
					app, err := PlanTemplate("outpost", TemplateOptions{Name: "events", Public: true, StorageGiB: 9, Architecture: "arm64", Values: map[string]string{
						"database-mode": database, "redis-mode": redis, "broker-mode": broker, "redis-host": "redis.example.test",
					}})
					if err != nil {
						t.Fatal(err)
					}
					required := []string{"api-key", "encryption-secret", "jwt-secret", "redis-password"}
					for name, mode := range map[string]string{"db": database, "redis": redis, "broker": broker} {
						service, exists := app.Services[name]
						if exists != (mode == "bundled") || exists && (service.Volume == nil || service.Volume.SizeGiB != 9) {
							t.Fatalf("%s does not match the dependency and storage selection", name)
						}
					}
					if database == "bundled" {
						required = append(required, "database-password")
					} else {
						required = append(required, "database-url")
					}
					if broker == "bundled" {
						required = append(required, "broker-password")
					} else {
						required = append(required, "broker-url")
					}
					slices.Sort(required)
					if !slices.Equal(TemplateSecretNames(app), required) {
						t.Fatal("selected provider credentials do not match the rendered references", TemplateSecretNames(app))
					}
					for _, name := range []string{"main", "delivery", "log", "migrate"} {
						service := app.Services[name]
						if slices.Contains(service.DependsOn, "db") != (database == "bundled") || slices.Contains(service.DependsOn, "redis") != (redis == "bundled") {
							t.Fatal("server or migration dependency differs from the selected provider", name)
						}
						if database == "external" {
							if _, exists := service.Bindings["POSTGRES_URL"]; exists || service.Secrets["POSTGRES_URL"].Ref != "database-url" {
								t.Fatal("external database must use one scoped provider URL", name)
							}
						} else if binding := service.Bindings["POSTGRES_URL"]; binding.Service != "db" || binding.Database != "outpost" || binding.Password.Ref != "database-password" {
							t.Fatal("bundled database binding changed", name)
						}
						if service.Secrets["REDIS_PASSWORD"].Ref != "redis-password" || service.Env["REDIS_TLS_VERIFY"] != "true" {
							t.Fatal("Redis lost credential scope or TLS verification", name)
						}
						if redis == "external" && (service.Env["REDIS_HOST"] != "redis.example.test" || service.Env["REDIS_TLS_ENABLED"] != "true") {
							t.Fatal("external Redis must use the selected host and TLS by default", name)
						}
						if broker == "external" {
							if _, exists := service.Bindings["RABBITMQ_SERVER_URL"]; exists || service.Secrets["RABBITMQ_SERVER_URL"].Ref != "broker-url" {
								t.Fatal("external broker must use its scoped URL", name)
							}
						} else if binding := service.Bindings["RABBITMQ_SERVER_URL"]; binding.Protocol != "amqp" || binding.Service != "broker" || binding.Database != "outpost" || binding.Password.Ref != "broker-password" {
							t.Fatal("bundled broker binding changed", name)
						}
						if name == "migrate" {
							if service.Job == nil || !slices.Equal(service.Args, []string{"migrate", "apply", "--yes"}) || slices.Contains(service.DependsOn, "broker") != (broker == "bundled") || service.Secrets["AES_ENCRYPTION_SECRET"].Ref != "encryption-secret" || service.Secrets["API_KEY"].Ref != "" || service.Secrets["API_JWT_SECRET"].Ref != "" {
								t.Fatal("migration must validate dependency and encryption configuration without API keys")
							}
							continue
						}
						if !slices.Contains(service.DependsOn, "migrate") || service.Healthcheck != "/healthz" || slices.Contains(service.DependsOn, "broker") != (broker == "bundled") {
							t.Fatal("server must await migration and selected broker before readiness", name)
						}
						for key, ref := range map[string]string{"API_KEY": "api-key", "API_JWT_SECRET": "jwt-secret", "AES_ENCRYPTION_SECRET": "encryption-secret"} {
							if service.Secrets[key].Ref != ref {
								t.Fatal("server roles must share stable scoped credentials", name, key)
							}
						}
					}
					for name, service := range app.Services {
						if service.Public != (name == "main") || service.RunAsUser <= 0 || !strings.Contains(service.Image, "@sha256:") || service.Architecture != "arm64" {
							t.Fatal("service lost private boundary, user, image pin or selected architecture", name)
						}
					}
					encoded, err := toml.Marshal(app)
					if err != nil {
						t.Fatal(err)
					}
					parsed, err := Parse(encoded)
					if err != nil || !reflect.DeepEqual(app, parsed) || strings.Contains(string(encoded), "{{") {
						t.Fatal("reviewed TOML does not round-trip", err)
					}
				})
			}
		}
	}
}

func TestOutpostProviderValidationAndInactiveDrafts(t *testing.T) {
	values := map[string]string{"redis-mode": "external", "redis-host": "2001:db8::1", "redis-port": "6380", "redis-database": "2", "redis-username": "outpost", "redis-tls": "true"}
	for key, value := range map[string]string{"database-mode": "invalid", "broker-mode": "invalid", "redis-mode": "invalid", "redis-host": "rediss://user:secret@redis", "redis-port": "65536", "redis-database": "-1", "redis-username": " user ", "redis-tls": "no"} {
		bad := maps.Clone(values)
		bad[key] = value
		if _, err := PlanTemplate("outpost", TemplateOptions{Name: "events", Values: bad}); err == nil {
			t.Fatal("invalid provider input accepted", key)
		}
	}
	app, err := PlanTemplate("outpost", TemplateOptions{Name: "events", Values: values})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main", "delivery", "log", "migrate"} {
		if env := app.Services[name].Env; env["REDIS_HOST"] != "[2001:db8::1]" || env["REDIS_PORT"] != "6380" || env["REDIS_DATABASE"] != "2" || env["REDIS_USERNAME"] != "outpost" {
			t.Fatal("external Redis configuration was not applied consistently", name)
		}
	}
	values["redis-mode"] = "bundled"
	values["redis-host"], values["redis-port"] = "unused-invalid-host", "invalid"
	app, err = PlanTemplate("outpost", TemplateOptions{Name: "events", Values: values})
	if err != nil || app.Services["main"].Env["REDIS_HOST"] != "redis" || app.Services["main"].Env["REDIS_TLS_ENABLED"] != "false" || app.Services["main"].Public {
		t.Fatal("inactive provider draft affected the bundled/private plan", err)
	}
}

func TestOutpostProviderCredentialURLs(t *testing.T) {
	for name, values := range map[string][]string{
		"database-url": {"postgres://user:p%40ss@postgres.example.test/outpost?sslmode=verify-full", "postgresql://user:password@[2001:db8::1]:5432/outpost?sslmode=require", "postgres://user:password@db/outpost?sslmode=disable"},
		"broker-url":   {"amqps://user:p%40ss@broker.example.test:5671/outpost", "amqp://user:password@broker:5672/%2f"},
	} {
		for _, value := range values {
			if err := ValidateTemplateSecret("outpost", name, value); err != nil {
				t.Fatal("provider URL rejected", name, err)
			}
		}
	}
	for name, values := range map[string][]string{
		"database-url": {"postgres://user:secret@db/outpost", "postgres://user:secret@db/outpost?sslmode=prefer", "postgres://user:secret@db/outpost?sslmode=require&sslmode=disable", "postgres://user:secret@db/?sslmode=require", "postgres://user:secret@db:0/outpost?sslmode=require", "postgres://user:secret@db:/outpost?sslmode=require", "postgres://user@db/outpost?sslmode=require", "postgres://user:secret@db/outpost?sslmode=require#fragment", "http://user:secret@db/outpost?sslmode=require"},
		"broker-url":   {"http://user:secret@broker/outpost", "amqp://user@broker/outpost", "amqps://user:secret@broker:65536/outpost", "amqp://user:secret@/outpost", "amqp://user:secret@broker:/outpost", "amqp://user:secret@broker/outpost#fragment", "amqp://user:secret@broker/a/b", "amqp://user:secret@broker/%00"},
	} {
		for _, value := range values {
			if err := ValidateTemplateSecret("outpost", name, value); err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), value) {
				t.Fatal("invalid URL accepted or credential echoed", name)
			}
		}
	}
}

func TestAMQPBindingCredentialsAndNetworkIsolation(t *testing.T) {
	app := Application{Name: "events", Services: map[string]Service{
		"broker": {Image: "rabbitmq:4", Port: 5672},
		"main":   {Image: "example/app:1", Bindings: map[string]Binding{"RABBITMQ_SERVER_URL": {Service: "broker", Protocol: "amqp", Username: "hakopod", Database: "outpost", Password: &SecretRef{Ref: "broker-password"}}}},
	}}
	app, err := Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	binding := app.Services["main"].Bindings["RABBITMQ_SERVER_URL"]
	password := "private-p@ss:/?#word"
	u, err := url.Parse(BindingURL(binding, app.Services["broker"], []byte(password)))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := u.User.Password()
	if got != password || u.Scheme != "amqp" || u.Host != "broker:5672" || u.Path != "/outpost" {
		t.Fatal("AMQP binding lost escaped credentials or vhost")
	}
	broker := app.Services["broker"]
	broker.NetworkAccess = &NetworkAccess{From: []string{}}
	app.Services["broker"] = broker
	if _, err := Normalize(app); err == nil {
		t.Fatal("AMQP binding bypassed private network isolation")
	}
}
