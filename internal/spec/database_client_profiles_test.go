package spec

import (
	"strings"
	"testing"
)

func databaseProfileApplication(profile string, managed *Binding) Application {
	variable := "DATABASE_URL"
	if profile == DatabaseClientInfisicalPostgresV1 {
		variable = "DB_CONNECTION_URI"
	}
	service := Service{
		Image:                  "registry.example.test/app@sha256:" + strings.Repeat("a", 64),
		Secrets:                map[string]SecretRef{variable: {Ref: "database-url"}},
		DatabaseClientProfiles: map[string]string{variable: profile},
	}
	if managed != nil {
		service.Secrets = nil
		service.Bindings = map[string]Binding{variable: *managed}
	}
	return Application{Name: "profile-test", Services: map[string]Service{"api": service}}
}

func TestDatabaseClientProfilesRequireDeclaredCompatibleConnections(t *testing.T) {
	for _, profile := range []string{DatabaseClientInfisicalPostgresV1, DatabaseClientLibpqURLV1, DatabaseClientNodeExtraCAV1, DatabaseClientGlitchTipValkeyV21} {
		if _, err := Normalize(databaseProfileApplication(profile, nil)); err != nil {
			t.Fatalf("saved connection profile %s: %v", profile, err)
		}
	}
	postgres := Binding{ManagedDatabase: strings.Repeat("a", 32), Protocol: "postgres", Endpoint: "read_write"}
	if _, err := Normalize(databaseProfileApplication(DatabaseClientInfisicalPostgresV1, &postgres)); err != nil {
		t.Fatal(err)
	}
	redis := Binding{ManagedDatabase: strings.Repeat("b", 32), Protocol: "redis", Endpoint: "read_write"}
	if _, err := Normalize(databaseProfileApplication(DatabaseClientNodeExtraCAV1, &redis)); err != nil {
		t.Fatal(err)
	}
	if _, err := Normalize(databaseProfileApplication(DatabaseClientLibpqURLV1, &redis)); err == nil {
		t.Fatal("PostgreSQL profile accepted a Redis binding")
	}
	if _, err := Normalize(databaseProfileApplication(DatabaseClientGlitchTipValkeyV21, &redis)); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseClientProfileRejectsGeneratedEnvironmentCollisions(t *testing.T) {
	app := databaseProfileApplication(DatabaseClientNodeExtraCAV1, nil)
	service := app.Services["api"]
	service.Env = map[string]string{"NODE_EXTRA_CA_CERTS": "/other/ca.pem"}
	app.Services["api"] = service
	if _, err := Normalize(app); err == nil || !strings.Contains(err.Error(), "NODE_EXTRA_CA_CERTS is already defined") {
		t.Fatalf("generated environment collision was accepted: %v", err)
	}
	app = databaseProfileApplication(DatabaseClientInfisicalPostgresV1, nil)
	service = app.Services["api"]
	service.Secrets["DB_ROOT_CERT"] = SecretRef{Ref: "other-ca"}
	app.Services["api"] = service
	if _, err := Normalize(app); err == nil || !strings.Contains(err.Error(), "DB_ROOT_CERT is already defined") {
		t.Fatalf("Infisical CA collision was accepted: %v", err)
	}
	app = databaseProfileApplication(DatabaseClientGlitchTipValkeyV21, nil)
	service = app.Services["api"]
	service.Env = map[string]string{"SSL_CERT_DIR": "/other/certs"}
	app.Services["api"] = service
	if _, err := Normalize(app); err == nil || !strings.Contains(err.Error(), "SSL_CERT_DIR is already defined") {
		t.Fatalf("GlitchTip CA collision was accepted: %v", err)
	}
}
