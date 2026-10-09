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
	redis := Binding{ManagedDatabase: strings.Repeat("b", 32), Protocol: "redis", Endpoint: "read_write"}
	postgres := Binding{ManagedDatabase: strings.Repeat("a", 32), Protocol: "postgres", Endpoint: "read_write"}
	app := databaseProfileApplication(DatabaseClientNodeExtraCAV1, &redis)
	service := app.Services["api"]
	service.Env = map[string]string{"NODE_EXTRA_CA_CERTS": "/other/ca.pem"}
	app.Services["api"] = service
	if _, err := Normalize(app); err == nil || !strings.Contains(err.Error(), "NODE_EXTRA_CA_CERTS is already defined") {
		t.Fatalf("generated environment collision was accepted: %v", err)
	}
	app = databaseProfileApplication(DatabaseClientInfisicalPostgresV1, &postgres)
	service = app.Services["api"]
	service.Secrets = map[string]SecretRef{"DB_ROOT_CERT": {Ref: "other-ca"}}
	app.Services["api"] = service
	if _, err := Normalize(app); err == nil || !strings.Contains(err.Error(), "DB_ROOT_CERT is already defined") {
		t.Fatalf("Infisical CA collision was accepted: %v", err)
	}
	app = databaseProfileApplication(DatabaseClientGlitchTipValkeyV21, &redis)
	service = app.Services["api"]
	service.Env = map[string]string{"SSL_CERT_DIR": "/other/certs"}
	app.Services["api"] = service
	if _, err := Normalize(app); err == nil || !strings.Contains(err.Error(), "SSL_CERT_DIR is already defined") {
		t.Fatalf("GlitchTip CA collision was accepted: %v", err)
	}
}

func TestDatabaseClientProfileAllowsManualTrustUntilManaged(t *testing.T) {
	for _, item := range []struct {
		name      string
		profile   string
		generated string
	}{
		{"infisical saved secret", DatabaseClientInfisicalPostgresV1, "DB_ROOT_CERT"},
		{"node saved secret", DatabaseClientNodeExtraCAV1, "NODE_EXTRA_CA_CERTS"},
		{"glitchtip saved secret", DatabaseClientGlitchTipValkeyV21, "SSL_CERT_FILE"},
	} {
		t.Run(item.name, func(t *testing.T) {
			app := databaseProfileApplication(item.profile, nil)
			app.Env = map[string]string{item.generated: "/manual/trust.pem"}
			if _, err := Normalize(app); err != nil {
				t.Fatalf("dormant profile rejected application trust: %v", err)
			}
			app.Env = nil
			service := app.Services["api"]
			service.Secrets[item.generated] = SecretRef{Ref: "manual-trust"}
			app.Services["api"] = service
			if _, err := Normalize(app); err != nil {
				t.Fatalf("dormant profile rejected service trust: %v", err)
			}
		})
	}

	app := databaseProfileApplication(DatabaseClientInfisicalPostgresV1, nil)
	service := app.Services["api"]
	service.Secrets = map[string]SecretRef{"DB_ROOT_CERT": {Ref: "external-ca"}}
	service.Bindings = map[string]Binding{"DB_CONNECTION_URI": {ExternalDatabase: strings.Repeat("c", 32), ExternalDatabaseRevision: 1, Protocol: "postgres"}}
	service.Networks = []string{"default"}
	app.Services["api"] = service
	app.Networks = map[string]Network{"default": {Internal: false}}
	if _, err := Normalize(app); err != nil {
		t.Fatalf("external connection profile rejected explicit provider trust: %v", err)
	}
}

func TestDatabaseClientProfileAppearsInServiceDiff(t *testing.T) {
	before := databaseProfileApplication(DatabaseClientLibpqURLV1, nil)
	after := databaseProfileApplication(DatabaseClientLibpqURLV1, nil)
	service := after.Services["api"]
	service.DatabaseClientProfiles["DATABASE_URL"] = DatabaseClientGlitchTipValkeyV21
	after.Services["api"] = service
	changes := Diff(&before, after)
	for _, change := range changes {
		if change.Service == "api" && change.Field == "database_client_profiles" {
			return
		}
	}
	t.Fatal("database client profile change was omitted from the deployment review")
}
