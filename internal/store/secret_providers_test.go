package store

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/hakopod/hakopod/internal/secretprovider"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestSecretProviderScopeRevisionAndWriteOnlyStorage(t *testing.T) {
	db := isolatedDatabase(t)
	principal := bootstrapPrincipal(t, db)
	ctx := context.Background()
	p := secretprovider.Provider{Name: "vault", Kind: "vault", Endpoint: "https://vault.example.com", Mount: "secret", RootPath: "shop", Scopes: []secretprovider.Scope{{Project: "demo", Environments: []string{"development"}}}}
	key := bytes.Repeat([]byte{7}, 32)
	var err error
	p.EncryptedCredentials, err = secretprovider.SealCredentials(key, p, secretprovider.Credentials{Token: "private-test-token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.PutSecretProvider(ctx, Principal{}, p, 0); !errors.Is(err, ErrForbidden) {
		t.Fatal("non-admin configured provider")
	}
	p, err = db.PutSecretProvider(ctx, principal, p, 0)
	if err != nil || p.Revision != 1 {
		t.Fatalf("create provider: %v", err)
	}
	if bytes.Contains(JSON(p), []byte("private-test-token")) || bytes.Contains(JSON(p), p.EncryptedCredentials) {
		t.Fatal("credentials exposed")
	}
	stored, err := db.SecretProvider(ctx, p.Name)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := secretprovider.OpenCredentials(key, stored)
	if err != nil || credentials.Token != "private-test-token" {
		t.Fatal("encrypted credentials lost")
	}
	if _, err = db.PutSecretProvider(ctx, principal, p, 0); !errors.Is(err, ErrConflict) {
		t.Fatal("stale write accepted")
	}
	changed := p
	changed.Endpoint = "https://attacker.example.com"
	if _, err = db.PutSecretProvider(ctx, principal, changed, 1); !errors.Is(err, secretprovider.ErrInput) {
		t.Fatal("endpoint replaced while retaining credentials")
	}
	changed = p
	changed.Scopes = []secretprovider.Scope{{Project: "missing"}}
	if _, err = db.PutSecretProvider(ctx, principal, changed, 1); !errors.Is(err, secretprovider.ErrInput) {
		t.Fatal("unknown project scope accepted")
	}
	changed = p
	changed.Scopes = []secretprovider.Scope{{Project: "demo", Environments: []string{"missing"}}}
	if _, err = db.PutSecretProvider(ctx, principal, changed, 1); !errors.Is(err, secretprovider.ErrInput) {
		t.Fatal("unknown environment scope accepted")
	}
	changed = p
	changed.Scopes = []secretprovider.Scope{{Project: "demo"}}
	changed, err = db.PutSecretProvider(ctx, principal, changed, 1)
	if err != nil || changed.Revision != 2 {
		t.Fatalf("update access: %v", err)
	}
	app := emptyTestSpec()
	svc := app.Services["api"]
	svc.Secrets = map[string]spec.SecretRef{"PASSWORD": {Provider: "vault", Key: "password"}}
	app.Services["api"] = svc
	if _, err = db.Accept(ctx, principal, "demo", "development", app, 0, "provider-bound-app"); err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteSecretProvider(ctx, principal, p.Name, 2); !errors.Is(err, ErrConflict) {
		t.Fatal("referenced provider deleted")
	}
	svc.Secrets = nil
	svc.Files = map[string]spec.File{"credentials": {MountPath: "/app/credentials", Secret: &spec.SecretRef{Provider: "vault", Key: "password"}}}
	app.Services["api"] = svc
	if _, err = db.Accept(ctx, principal, "demo", "development", app, 1, "provider-file-app"); err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteSecretProvider(ctx, principal, p.Name, 2); !errors.Is(err, ErrConflict) {
		t.Fatal("file provider reference did not prevent deletion", err)
	}
	svc.Files = nil
	svc.Bindings = map[string]spec.Binding{"DATABASE_URL": {Service: "api", Protocol: "postgres", Username: "app", Database: "app", Password: &spec.SecretRef{Provider: "vault", Key: "password"}}}
	app.Services["api"] = svc
	if _, err = db.Accept(ctx, principal, "demo", "development", app, 2, "provider-binding-app"); err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteSecretProvider(ctx, principal, p.Name, 2); !errors.Is(err, ErrConflict) {
		t.Fatal("binding provider reference did not prevent deletion", err)
	}
	var auditCount int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='secret.provider.configured'").Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("configuration audit missing: %v", err)
	}
}
