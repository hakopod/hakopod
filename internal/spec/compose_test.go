package spec

import (
	"reflect"
	"strings"
	"testing"
)

func TestComposeImportRoundTripAndRuntime(t *testing.T) {
	input := `name: fastapi
services:
  api:
    image: example/api:${TAG:-latest}
    entrypoint: [python, -m, uvicorn]
    command: 'main:app --host 0.0.0.0 --port 8000 --header "X-Name: hello world"'
    ports: ["8080:8000", "9000/udp"]
    expose: [8000]
    environment:
      APP_ENV: production
      LABEL: ${LABEL}
      LITERAL: $$HOME
      EMPTY: ""
    deploy:
      replicas: 3
    networks: [frontend]
networks:
  frontend: {}
`
	result, err := ImportCompose([]byte(input), "", map[string]string{"LABEL": "hello"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := result.Spec.Services["api"]
	if s.Image != "example/api:latest" || s.Port != 8000 || s.Public || s.Replicas != 3 || len(s.Ports) != 1 || s.Ports[0].Protocol != "UDP" {
		t.Fatalf("incorrect converted runtime: %+v", s)
	}
	if s.Env["LITERAL"] != "$HOME" || s.Env["LABEL"] != "hello" || s.Args[len(s.Args)-1] != "X-Name: hello world" {
		t.Fatal("argv or environment changed")
	}
	parsed, err := Parse([]byte(result.TOML))
	if err != nil || !reflect.DeepEqual(parsed, result.Spec) {
		t.Fatal("TOML did not round trip", err)
	}
	if !strings.Contains(strings.Join(result.Warnings, " "), "publishing") {
		t.Fatal("host port difference omitted")
	}
}

func TestComposeAddsServicesWithoutMutatingBase(t *testing.T) {
	base, err := Normalize(Application{Name: "existing", Services: map[string]Service{"web": {Image: "example/web:stable", Env: map[string]string{"KEEP": "yes"}}}})
	if err != nil {
		t.Fatal(err)
	}
	input := []byte("services:\n  worker:\n    image: example/worker:stable\n    volumes: [data:/data]\nvolumes:\n  data: {}\n")
	result, err := ImportCompose(input, "existing", nil, &base)
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Services) != 1 || len(base.Volumes) != 0 || len(result.Spec.Services) != 2 || result.Spec.Volumes["data"].SizeGiB != 1 {
		t.Fatal("base mutated or volume missing")
	}
	if !reflect.DeepEqual(base.Services["web"], result.Spec.Services["web"]) {
		t.Fatal("existing service changed")
	}
	if _, err = ImportCompose(input, "existing", nil, &result.Spec); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatal("collision accepted", err)
	}
}

func TestComposeSecretReferencesNeverRetainValues(t *testing.T) {
	input := `services:
  db:
    image: postgres:17
    environment:
      POSTGRES_PASSWORD: do-not-save-this
    x-hakopod:
      secrets:
        POSTGRES_PASSWORD: {ref: db-password}
    volumes: [data:/var/lib/postgresql/data]
volumes:
  data:
    x-hakopod: {size_gib: 5, access_mode: ReadWriteOnce}
`
	result, err := ImportCompose([]byte(input), "database", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.TOML, "do-not-save-this") || strings.Contains(strings.Join(result.Warnings, " "), "do-not-save-this") {
		t.Fatal("secret persisted")
	}
	if result.Spec.Services["db"].Secrets["POSTGRES_PASSWORD"].Ref != "db-password" || result.Spec.Volumes["data"].SizeGiB != 5 {
		t.Fatal("secret or storage declaration lost")
	}
}

func TestComposeRejectsUnsupportedOrAmbiguousInputs(t *testing.T) {
	tests := map[string]string{
		"healthcheck":            "healthcheck: {test: [CMD, pg_isready]}",
		"privileged":             "privileged: true",
		"bind mount":             "volumes: [./data:/data]",
		"build":                  "build: .",
		"external env":           "env_file: .env",
		"host network":           "network_mode: host",
		"conditional dependency": "depends_on: {db: {condition: service_healthy}}",
		"missing variable":       "environment: {VALUE: '${UNSET}'}",
		"password":               "environment: {POSTGRES_PASSWORD: do-not-echo}",
		"credential URL":         "environment: {DATABASE_URL: 'postgres://user:do-not-echo@db/test'}",
		"clear command":          "command: []",
		"host root":              "user: '0'",
		"resources":              "deploy: {resources: {limits: {memory: 10m}}}",
		"replicas zero":          "deploy: {replicas: 0}",
		"port range":             "ports: ['8000-8002']",
		"aliases":                "networks: {default: {aliases: [other]}}",
	}
	for name, field := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ImportCompose([]byte("services:\n  api:\n    image: busybox:stable\n    "+field+"\n"), "test", nil, nil)
			if err == nil {
				t.Fatal("unsupported input accepted")
			}
			if strings.Contains(err.Error(), "do-not-echo") {
				t.Fatal("secret leaked in error")
			}
		})
	}
	for _, doc := range []string{
		"services: {api: {image: one, image: two}}",
		"services: {api: &x {image: busybox}, other: *x}",
		"services: {}\n---\nservices: {}",
		"services: {bad_name: {image: busybox}}",
		"include: [other.yaml]\nservices: {api: {image: busybox}}",
		strings.Repeat("x", MaxBytes+1),
	} {
		if _, err := ImportCompose([]byte(doc), "test", nil, nil); err == nil {
			t.Fatal("unsafe document accepted")
		}
	}
}

func TestComposeInterpolationIsExplicitAndBounded(t *testing.T) {
	t.Setenv("COMPOSE_IMPORT_HOST_ONLY", "do-not-read")
	r := composeReader{variables: map[string]string{"EMPTY": "", "SET": "value"}}
	for input, want := range map[string]string{"${EMPTY:-fallback}": "fallback", "${EMPTY-fallback}": "", "${SET:+yes}": "yes", "${UNSET+yes}": "", "$$HOME": "$HOME", "$SET": "value"} {
		got, err := r.interpolate(input, "env")
		if err != nil || got != want {
			t.Fatalf("%s: %q %v", input, got, err)
		}
	}
	for _, input := range []string{"${COMPOSE_IMPORT_HOST_ONLY}", "${UNSET:?private-error-body}", "${SET:-${OTHER}}"} {
		_, err := r.interpolate(input, "env")
		if err == nil || strings.Contains(err.Error(), "private-error-body") {
			t.Fatal("unsafe expansion", err)
		}
	}
}
