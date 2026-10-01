package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

func TestDatabaseConnectionsScopeHistoryAndBounds(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "consumer-database", "create"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE managed_databases SET status='ready',observation=$2 WHERE id=$1", d.ID, JSON(database.Observation{Status: "ready", Revision: 1})); err != nil {
		t.Fatal(err)
	}
	var first Deployment
	for i := 0; i < 15; i++ {
		app, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: fmt.Sprintf("consumer-%02d", i), Services: map[string]spec.Service{"api": {Image: "nginx:alpine", Env: map[string]string{"APP_LABEL": "never-return-this-value"}, Bindings: map[string]spec.Binding{"DATABASE_URL": {ManagedDatabase: d.ID, Protocol: "postgres", Endpoint: "read_write"}}}}})
		if err != nil {
			t.Fatal(err)
		}
		dep, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, fmt.Sprintf("consumer-create-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = dep
		}
	}
	list, err := s.DatabaseConnections(ctx, p, d.ID)
	if err != nil || len(list.Items) != 15 || list.Truncated {
		t.Fatalf("15 consumers: %d, %v", len(list.Items), err)
	}
	for _, item := range list.Items {
		if item.SavedRevision != 1 || item.LatestAttemptRevision != 1 || item.LastSuccessfulRevision != 0 || item.LatestAttemptStatus != "queued" || item.Project != d.Project || item.Environment != d.Environment {
			t.Fatalf("incorrect evidence: %+v", item)
		}
	}
	encoded, _ := json.Marshal(list)
	if strings.Contains(string(encoded), "never-return") || strings.Contains(string(encoded), "APP_LABEL") || strings.Contains(string(encoded), "sealed-test") {
		t.Fatal("connection metadata leaked configuration or credentials")
	}
	for _, principal := range []Principal{
		{Admin: true, Permissions: []string{"admin"}, Application: "consumer-00"},
		{Admin: true, Permissions: []string{"admin"}, Project: "other"},
		{Admin: true, Permissions: []string{"admin"}, Environment: "production"},
		{Admin: true, Permissions: []string{"deployments:write"}},
	} {
		if _, err := s.DatabaseConnections(ctx, principal, d.ID); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("scope escaped", err)
		}
	}
	// An unsuccessful replacement must not erase the previous successful binding.
	if _, err := s.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	next := first.Spec
	next.Services = map[string]spec.Service{"api": first.Spec.Services["api"]}
	svc := next.Services["api"]
	svc.Bindings = nil
	next.Services["api"] = svc
	second, err := s.Accept(ctx, p, d.Project, d.Environment, next, 1, "consumer-remove-binding")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE deployments SET status='failed',recovery_spec=$2 WHERE id=$1", second.ID, JSON(first.Spec)); err != nil {
		t.Fatal(err)
	}
	list, err = s.DatabaseConnections(ctx, p, d.ID)
	if err != nil || len(list.Items) != 15 {
		t.Fatal("history disappeared", err)
	}
	item := list.Items[0]
	if item.ApplicationID != first.ApplicationID || item.SavedRevision != 0 || item.LastSuccessfulRevision != 1 || item.LatestAttemptRevision != 2 || item.LatestAttemptStatus != "failed" {
		t.Fatalf("incorrect historical evidence: %+v", item)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded',recovery_spec=NULL WHERE id=$1", second.ID); err != nil {
		t.Fatal(err)
	}
	list, err = s.DatabaseConnections(ctx, p, d.ID)
	if err != nil || len(list.Items) != 14 {
		t.Fatal("replaced connection retained", err)
	}
	// A corrupt out-of-scope reference is still never disclosed.
	if _, err := s.Pool.Exec(ctx, "INSERT INTO environments(project,name) VALUES($1,'production') ON CONFLICT DO NOTHING", d.Project); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE applications SET environment='production' WHERE id=$1", list.Items[0].ApplicationID); err != nil {
		t.Fatal(err)
	}
	list, err = s.DatabaseConnections(ctx, p, d.ID)
	if err != nil || len(list.Items) != 13 {
		t.Fatal("out-of-scope reference returned", err)
	}
	// Bound the API even if persisted input predates today's validation limits.
	bindings := map[string]spec.Binding{}
	for i := 0; i < MaxDatabaseConnections+1; i++ {
		bindings[fmt.Sprintf("DB_%03d", i)] = spec.Binding{ManagedDatabase: d.ID, Protocol: "postgres", Endpoint: "read_write"}
	}
	svc.Bindings = bindings
	recovery := next
	recovery.Services["api"] = svc
	if _, err := s.Pool.Exec(ctx, "UPDATE applications SET spec=$2 WHERE id=$1", first.ApplicationID, JSON(recovery)); err != nil {
		t.Fatal(err)
	}
	list, err = s.DatabaseConnections(ctx, p, d.ID)
	if err != nil || len(list.Items) != MaxDatabaseConnections || !list.Truncated {
		t.Fatalf("unbounded connections: %d, %v", len(list.Items), err)
	}
}
