package api

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestGenerateTemplateRSAEncryptionKey(t *testing.T) {
	field := spec.TemplateSecretField{Name: "encryption-private-key", Format: "base64-rsa-private-key", Generate: true}
	server := &Server{}
	first := ""
	for range 2 {
		value, err := server.generateTemplateSecret(context.Background(), "demo", "development", "mail", field)
		if err != nil {
			t.Fatal(err)
		}
		if err := spec.ValidateTemplateSecret("xem", field.Name, value); err != nil {
			t.Fatal("generated key rejected by deployment validation", err)
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(value)
		if err != nil {
			t.Fatal("generated key encoding is invalid")
		}
		block, _ := pem.Decode(decoded)
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			t.Fatal("generated PEM is invalid")
		}
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok || key.N.BitLen() != 2048 || value == first {
			t.Fatal("key generation did not produce an independent 2048-bit RSA key")
		}
		first = value
	}
}

func TestXemReviewedTemplateRequiresScopedCredentials(t *testing.T) {
	for _, storage := range []string{"bundled", "external"} {
		t.Run(storage, func(t *testing.T) { testXemReviewedTemplateCredentials(t, storage) })
	}
}

func testXemReviewedTemplateCredentials(t *testing.T, storage string) {
	db := sourceDatabase(t)
	raw, err := db.Bootstrap(context.Background(), "xem-template-fixture")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: db, Cluster: templateSecretKube(t, "arm64")}
	handler := server.Handler()
	call := func(method, path string, input any, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(store.JSON(input)))
		r.Header.Set("Authorization", "Bearer "+raw)
		r.Header.Set("Idempotency-Key", "xem-template-fixture")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		if out.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, out.Code, want, out.Body.String())
		}
		return out.Body.Bytes()
	}
	cfg := templateConfiguration{Project: "demo", Environment: "development", TemplateOptions: spec.TemplateOptions{
		Name: "mail", SiteURL: "https://mail.example.test", Architecture: "arm64",
		Values: map[string]string{"admin-email": "admin@example.test", "storage-mode": storage, "storage-endpoint": "https://s3.example.test", "storage-bucket": "xem-media", "storage-region": "us-east-1", "database-host": "postgres.example.test", "redis-host": "redis.example.test"},
	}}
	var plan struct {
		TOML          string                `json:"toml"`
		Spec          spec.Application      `json:"spec"`
		Configuration templateConfiguration `json:"configuration"`
		Required      []string              `json:"required_secrets"`
	}
	for _, database := range []string{"bundled", "external"} {
		for _, redis := range []string{"bundled", "external"} {
			cfg.Values["database-mode"] = database
			cfg.Values["redis-mode"] = redis
			plan.Spec = spec.Application{}
			if err := json.Unmarshal(call("POST", "/templates/xem/plan", cfg, 200), &plan); err != nil {
				t.Fatal(err)
			}
			_, hasDB := plan.Spec.Services["db"]
			_, hasRedis := plan.Spec.Services["redis"]
			_, hasStorage := plan.Spec.Services["storage"]
			if hasDB != (database == "bundled") || hasRedis != (redis == "bundled") || hasStorage != (storage == "bundled") || strings.Contains(plan.TOML, "{{") {
				t.Fatal("API review does not match selected dependencies")
			}
		}
	}
	deploy := map[string]any{"configuration": plan.Configuration, "toml": plan.TOML, "expected_revision": 0}
	call("POST", "/templates/xem/deploy", deploy, 400)
	for _, name := range plan.Required {
		input := map[string]any{"generate": true}
		if name == "storage-access-key" || name == "storage-secret-key" {
			input = map[string]any{"value": "development-provider-fixture"}
		}
		response := call("PUT", "/templates/xem/secrets/"+name+"?project=demo&environment=development&application=mail", input, 200)
		if bytes.Contains(response, []byte(`"value"`)) || bytes.Contains(response, []byte("development-provider-fixture")) {
			t.Fatal("secret write returned credential material")
		}
	}
	values, err := server.Cluster.ReadWorkloadSecrets(context.Background(), "demo", "development", "mail", plan.Required)
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.ValidateTemplateSecretSet("xem", plan.Required, values); err != nil {
		t.Fatal("saved credentials do not match deployable requirements", err)
	}
	wrongScope := cfg
	wrongScope.Name = "other-mail"
	var other struct {
		TOML string `json:"toml"`
	}
	if err := json.Unmarshal(call("POST", "/templates/xem/plan", wrongScope, 200), &other); err != nil {
		t.Fatal(err)
	}
	call("POST", "/templates/xem/deploy", map[string]any{"configuration": wrongScope, "toml": other.TOML, "expected_revision": 0}, 400)
	var count int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM deployments").Scan(&count); err != nil || count != 0 {
		t.Fatal("missing scoped credentials queued a deployment", err)
	}
	first := call("POST", "/templates/xem/deploy", deploy, 202)
	second := call("POST", "/templates/xem/deploy", deploy, 202)
	var accepted, replayed store.Deployment
	if json.Unmarshal(first, &accepted) != nil || json.Unmarshal(second, &replayed) != nil || accepted.ID == "" || accepted.ID != replayed.ID {
		t.Fatal("reviewed deployment did not preserve durable idempotency")
	}
}
