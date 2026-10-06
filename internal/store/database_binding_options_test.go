package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestManagedDatabaseBindingEnforcesTLSAndRoutingPolicy(t *testing.T) {
	d := database.Resource{Status: "ready", Spec: database.Spec{Engine: "postgresql", TLS: &database.TLSConfig{Mode: "required"}}, Observation: database.Observation{Status: "ready"}}
	b := spec.Binding{Protocol: "postgres", Endpoint: "read_write"}
	for _, mode := range []string{"", "require", "verify-ca", "verify-full"} {
		b.SSLMode = mode
		if err := validateDatabaseBinding(d, b); err != nil {
			t.Fatal(err)
		}
	}
	b.SSLMode = "disable"
	if err := validateDatabaseBinding(d, b); !errors.Is(err, ErrInput) {
		t.Fatal("TLS database accepted plaintext")
	}
	d.Spec.TLS = nil
	if err := validateDatabaseBinding(d, b); err != nil {
		t.Fatal(err)
	}
	b.SSLMode = "verify-full"
	if err := validateDatabaseBinding(d, b); !errors.Is(err, ErrInput) {
		t.Fatal("legacy database advertised unavailable TLS")
	}
	d.Spec.Engine = "vitess"
	b.Protocol = "mysql"
	b.SSLMode = ""
	b.Database = "app@replica"
	if err := validateDatabaseBinding(d, b); !errors.Is(err, ErrInput) {
		t.Fatal("database target bypassed endpoint routing")
	}
	d.Spec.Engine = "redis"
	d.Spec.Mode = "cluster"
	b.Protocol = "redis"
	b.Endpoint = "cluster"
	b.ClusterAware = true
	b.Database = "1"
	if err := validateDatabaseBinding(d, b); !errors.Is(err, ErrInput) {
		t.Fatal("Redis cluster accepted SELECT database")
	}
}

func TestDatabaseConnectionReviewPreservesCustomCredentials(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "custom-binding-database", "create"); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, claim, database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now().UTC()}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{Name: "custom-binding-fixture", Services: map[string]spec.Service{"api": {Image: "nginx:alpine"}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, "custom-binding-app")
	if err != nil {
		t.Fatal(err)
	}
	options := DatabaseConnectionOptions{Username: "infisical_user", Database: "infisical", Password: &spec.SecretRef{Ref: "infisical-password"}, SSLMode: "verify-ca"}
	plan, err := s.PlanDatabaseConnection(ctx, p, d.ID, first.ApplicationID, "api", "DB_CONNECTION_URI", "read_write", false, options)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Binding.Username != options.Username || plan.Binding.Database != options.Database || plan.Binding.SSLMode != options.SSLMode || !reflect.DeepEqual(plan.Binding.Password, options.Password) {
		t.Fatal("review lost custom settings")
	}
	if !strings.Contains(strings.Join(plan.Warnings, " "), "does not create users") {
		t.Fatal("review omitted credential provisioning limitation")
	}
	release, err := s.AcceptDatabaseConnection(ctx, p, d.ID, plan.ID, app.Name, "custom-binding-connect")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(release.Spec.Services["api"].Bindings["DB_CONNECTION_URI"], plan.Binding) {
		t.Fatal("accepted release changed reviewed settings")
	}
	appKey := p
	appKey.Application = app.Name
	changes := []func(*spec.Binding){func(b *spec.Binding) { b.Database = "another" }, func(b *spec.Binding) { b.Username = "another_user" }, func(b *spec.Binding) { b.Password = &spec.SecretRef{Ref: "another-password"} }, func(b *spec.Binding) { b.SSLMode = "require" }}
	for index, change := range changes {
		next, err := spec.Normalize(release.Spec)
		if err != nil {
			t.Fatal(err)
		}
		svc := next.Services["api"]
		b := svc.Bindings["DB_CONNECTION_URI"]
		change(&b)
		svc.Bindings["DB_CONNECTION_URI"] = b
		next.Services["api"] = svc
		if _, err = s.Accept(ctx, appKey, d.Project, d.Environment, next, release.Revision, "unauthorized-setting-"+string(rune('a'+index))); !errors.Is(err, ErrForbidden) {
			t.Fatal("application key changed database grant", err)
		}
	}
}
