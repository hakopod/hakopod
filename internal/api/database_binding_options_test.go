package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestManagedDatabaseURLPreservesDefaultsAndSelectedTargets(t *testing.T) {
	for _, tc := range []struct{ engine, protocol, user, db, scheme string }{
		{"postgresql", "postgres", "app", "app", "postgres"},
		{"redis", "redis", "default", "0", "rediss"},
		{"mysql", "mysql", "app", "app", "mysql"},
		{"vitess", "mysql", "app", "app@primary", "mysql"},
		{"mongodb", "mongodb", "app", "app", "mongodb"},
		{"clickhouse", "clickhouse", "app", "app", "clickhouse"},
		{"oracle", "oracle", "APP", "FREEPDB1", "oracle"},
		{"duckdb-mysql", "mysql", "root", "app", "mysql"},
		{"duckdb-postgres", "postgres", "postgres", "app", "postgres"},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			engine := tc.engine
			if strings.HasPrefix(engine, "duckdb-") {
				engine = "duckdb"
			}
			d := database.Resource{ID: strings.Repeat("a", 32), Spec: database.Spec{Engine: engine, TLS: &database.TLSConfig{Mode: "required"}}}
			endpoint := database.Endpoint{Host: "database.example.internal", Port: 5432, Purpose: "read_write"}
			b := spec.Binding{ManagedDatabase: d.ID, Protocol: tc.protocol, Endpoint: endpoint.Purpose}
			password := []byte("development-fixture:p@ss/?#%")
			u, err := url.Parse(managedDatabaseURL(d, b, endpoint, password))
			if err != nil {
				t.Fatal("invalid URI")
			}
			got, _ := u.User.Password()
			if u.User.Username() != tc.user || u.Path != "/"+tc.db || u.Scheme != tc.scheme || got != string(password) {
				t.Fatal("managed defaults or credential escaping changed")
			}
			b.Username = "tenant@user"
			b.Database = "tenant db"
			if tc.engine == "redis" {
				b.Database = "12"
			}
			u, err = url.Parse(managedDatabaseURL(d, b, endpoint, password))
			if err != nil {
				t.Fatal("invalid custom URI")
			}
			want := b.Database
			if tc.engine == "vitess" {
				want += "@primary"
			}
			got, _ = u.User.Password()
			if u.User.Username() != b.Username || u.Path != "/"+want || got != string(password) || u.Host != endpoint.Host+":5432" {
				t.Fatal("custom target or credentials lost")
			}
			if tc.engine == "mongodb" && u.Query().Get("authSource") != b.Database {
				t.Fatal("custom MongoDB login lost its authentication database")
			}
		})
	}
}

func TestDatabaseCredentialDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, engine, edition, username, database string
	}{
		{"postgresql", "postgresql", "", "app", "app"},
		{"mysql", "mysql", "", "app", "app"},
		{"myduck", "duckdb", "", "root", "app"},
		{"redis", "redis", "", "default", "0"},
		{"oracle-free", "oracle", "free", "APP", "FREEPDB1"},
		{"oracle-enterprise", "oracle", "enterprise", "APP", "APPDB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := database.Resource{Spec: database.Spec{Engine: tc.engine}}
			if tc.engine == "oracle" {
				d.Spec.Oracle = &database.OracleConfig{Edition: tc.edition}
			}
			username, name := databaseCredentialDefaults(d)
			if username != tc.username || name != tc.database {
				t.Fatalf("credential defaults = %q/%q, want %q/%q", username, name, tc.username, tc.database)
			}
		})
	}
}

func TestNamedPostgresCredentialsAndBindingAgree(t *testing.T) {
	d := database.Resource{Spec: database.Spec{Engine: "postgresql", TLS: &database.TLSConfig{Mode: "required"}, Postgres: &database.PostgresConfig{Database: "feesbook", Username: "superuserfox"}}}
	user, name := databaseCredentialDefaults(d)
	u, err := url.Parse(managedDatabaseURL(d, spec.Binding{Protocol: "postgres", Endpoint: "read_write"}, database.Endpoint{Host: "database.internal", Port: 5432}, []byte("fixture-password")))
	if err != nil || user != "superuserfox" || name != "feesbook" || u.User.Username() != user || u.Path != "/"+name || u.Query().Get("sslmode") != "verify-full" {
		t.Fatal("the credentials view and application binding disagree with the configured PostgreSQL identity")
	}
}

func TestMyDuckURLsKeepProtocolSpecificTLS(t *testing.T) {
	d := database.Resource{ID: strings.Repeat("a", 32), Spec: database.Spec{Engine: "duckdb", TLS: &database.TLSConfig{Mode: "required"}}}
	for _, tc := range []struct {
		purpose, protocol string
		port              int
	}{{"mysql", "mysql", 3306}, {"postgresql", "postgres", 5432}} {
		u, err := url.Parse(managedDatabaseURL(d, spec.Binding{Protocol: tc.protocol, Endpoint: tc.purpose}, database.Endpoint{Purpose: tc.purpose, Host: "myduck.internal", Port: tc.port}, []byte("secret")))
		if err != nil || u.Scheme != tc.protocol || u.Host != "myduck.internal:"+strconv.Itoa(tc.port) {
			t.Fatal("MyDuck protocol endpoint changed")
		}
		if tc.protocol == "postgres" && (u.Query().Get("sslmode") != "verify-full" || u.Query().Get("sslrootcert") != cluster.DatabaseTrustPath(d.ID)) {
			t.Fatal("MyDuck PostgreSQL endpoint lost verified TLS")
		}
		if tc.protocol == "mysql" && u.RawQuery != "" {
			t.Fatal("MyDuck MySQL endpoint added unsupported URL TLS parameters")
		}
	}
}

func TestManagedPostgresSSLSelectionAndVitessRouting(t *testing.T) {
	d := database.Resource{ID: strings.Repeat("a", 32), Spec: database.Spec{Engine: "postgresql", TLS: &database.TLSConfig{Mode: "required"}}}
	endpoint := database.Endpoint{Host: "database.example.internal", Port: 5432}
	b := spec.Binding{ManagedDatabase: d.ID, Protocol: "postgres", Endpoint: "read_write"}
	for _, mode := range []string{"", "require", "verify-ca", "verify-full"} {
		b.SSLMode = mode
		u, _ := url.Parse(managedDatabaseURL(d, b, endpoint, nil))
		want := mode
		if want == "" {
			want = "verify-full"
		}
		if u.Query().Get("sslmode") != want {
			t.Fatal("SSL selection lost")
		}
		ca := u.Query().Get("sslrootcert")
		if want == "require" && ca != "" {
			t.Fatal("require unexpectedly inherited CA verification")
		}
		if want != "require" && ca != cluster.DatabaseTrustPath(d.ID) {
			t.Fatal("verified TLS lost mounted CA")
		}
	}
	d.Spec.TLS = nil
	b.SSLMode = "disable"
	u, _ := url.Parse(managedDatabaseURL(d, b, endpoint, nil))
	if u.Query().Get("sslmode") != "disable" {
		t.Fatal("explicit legacy plaintext mode lost")
	}
	d.Spec.Engine = "vitess"
	b.Protocol = "mysql"
	b.Endpoint = "read_only"
	b.Database = "app"
	u, _ = url.Parse(managedDatabaseURL(d, b, endpoint, nil))
	if u.Path != "/app@replica" {
		t.Fatal("Vitess keyspace lost replica route")
	}
	d.Spec.Engine = "mongodb"
	d.Spec.TLS = &database.TLSConfig{Mode: "required"}
	b.Protocol = "mongodb"
	b.Username = "app"
	u, _ = url.Parse(managedDatabaseURL(d, b, endpoint, nil))
	if u.Query().Get("authSource") != "app" {
		t.Fatal("managed MongoDB user lost its existing authentication database")
	}
}

func TestManagedDatabaseConnectionAPIKeepsReviewedOptionsAndRequiresScopedPassword(t *testing.T) {
	db, token, principal, managed := databasePublicEndpointAPIFixture(t, "postgresql")
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, "UPDATE managed_databases SET observation=$2 WHERE id=$1", managed.ID, store.JSON(database.Observation{Status: "ready", Revision: managed.Revision})); err != nil {
		t.Fatal(err)
	}
	kube := templateSecretKube(t)
	db.ValidateDeployment = func(ctx context.Context, app store.Application, next spec.Application) error {
		return kube.ValidateWorkloadSecrets(ctx, app.Project, app.Environment, next)
	}
	app, err := spec.Normalize(spec.Application{Name: "binding-api-development-fixture", Services: map[string]spec.Service{"api": {Image: "nginx:alpine"}}})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := db.Accept(ctx, principal, managed.Project, managed.Environment, app, 0, "binding-api-create")
	if err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: db, Cluster: kube}).Handler()
	call := func(path string, body any, status int) []byte {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/databases/"+managed.ID+path, bytes.NewReader(store.JSON(body)))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Idempotency-Key", "binding-api-accept")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("connection request returned %d, want %d: %s", response.Code, status, response.Body.String())
		}
		return response.Body.Bytes()
	}
	input := map[string]any{"application_id": initial.ApplicationID, "service": "api", "variable": "DATABASE_URL", "endpoint": "read_write", "username": "restored_user", "database": "restored", "password": spec.SecretRef{Ref: "restored-password"}, "ssl_mode": "verify-full"}
	var plan store.DatabaseConnectionPlan
	if err = json.Unmarshal(call("/connection-plan", input, http.StatusOK), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Binding.Username != "restored_user" || plan.Binding.Database != "restored" || plan.Binding.Password == nil || plan.Binding.Password.Ref != "restored-password" || plan.Binding.SSLMode != "verify-full" {
		t.Fatal("API discarded reviewed binding choices")
	}
	accept := map[string]any{"review_id": plan.ID, "confirm_application": app.Name}
	call("/connect", accept, http.StatusConflict)
	if err = kube.PutWorkloadSecret(ctx, managed.Project, managed.Environment, "another-application", "restored-password", "foreign-fixture-value"); err != nil {
		t.Fatal(err)
	}
	call("/connect", accept, http.StatusConflict)
	const password = "sensitive-development-fixture-value"
	if err = kube.PutWorkloadSecret(ctx, managed.Project, managed.Environment, app.Name, "restored-password", password); err != nil {
		t.Fatal(err)
	}
	response := call("/connect", accept, http.StatusAccepted)
	if bytes.Contains(response, []byte(password)) {
		t.Fatal("resolved password entered API response")
	}
	var release store.Deployment
	if err = json.Unmarshal(response, &release); err != nil {
		t.Fatal(err)
	}
	binding := release.Spec.Services["api"].Bindings["DATABASE_URL"]
	if binding.Username != plan.Binding.Username || binding.Database != plan.Binding.Database || binding.SSLMode != plan.Binding.SSLMode || binding.Password == nil || binding.Password.Ref != plan.Binding.Password.Ref {
		t.Fatal("accepted deployment lost reviewed binding choices")
	}
	input["password"] = password
	response = call("/connection-plan", input, http.StatusBadRequest)
	if bytes.Contains(response, []byte(password)) {
		t.Fatal("invalid raw password entered API error")
	}
}
