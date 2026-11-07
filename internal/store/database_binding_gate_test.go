package store

import (
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestManagedDatabaseBindingGateUsesLifecycleStatus(t *testing.T) {
	b := spec.Binding{Protocol: "postgres", Endpoint: "read_write"}
	d := database.Resource{ID: "db1", Status: "ready", Spec: database.Spec{Name: "orders", Engine: "postgresql"}, Observation: database.Observation{Status: "unknown"}}
	// The deployment observes the database live; a stale observation alone
	// does not fail the deploy.
	if err := validateDatabaseBinding(d, b); err != nil {
		t.Fatalf("stale observation failed a ready database: %v", err)
	}
	d.Status = "updating"
	err := validateDatabaseBinding(d, b)
	if !errors.Is(err, ErrConflict) || err.Error() != "managed database orders (db1) is not ready: status updating" {
		t.Fatalf("not-ready database: %v", err)
	}
	d.Status = "ready"
	d.DeletedAt = &time.Time{}
	err = validateDatabaseBinding(d, b)
	if !errors.Is(err, ErrConflict) || err.Error() != "managed database orders (db1) was deleted" {
		t.Fatalf("deleted database: %v", err)
	}
}
