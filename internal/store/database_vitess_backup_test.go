package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestVitessNativeStorageReservation(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	p.Project, p.Environment = d.Project, d.Environment
	destination, err := s.PutBackupDestination(ctx, p, backup.Destination{ID: NewID(), Name: "native-only", Endpoint: "https://storage.example.com", Region: "test-region", Bucket: "dedicated", Prefix: "database", EncryptedCredentials: []byte("sealed-fixture")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	d.Spec.Engine, d.Spec.Version, d.Spec.CPU, d.Spec.Memory, d.Spec.StorageGiB = "vitess", "23", "500m", "1Gi", 5
	d.Spec.Vitess = &database.VitessConfig{BackupDestinationID: destination.ID, BackupDestinationRevision: destination.Revision}
	if _, err = s.AcceptDatabase(ctx, p, d, 0, "unapproved-native-fixture", "create"); !errors.Is(err, ErrForbidden) {
		t.Fatal("unapproved storage was accepted", err)
	}
	s.VitessBackupApprovals = []backup.VitessBackupApproval{{DestinationID: destination.ID, Revision: destination.Revision, Project: d.Project, Environment: d.Environment, Database: d.Spec.Name, DedicatedCredentials: true, EndpointCIDRs: []string{"93.184.216.34/32"}}}
	if _, err = s.AcceptDatabase(ctx, p, d, 0, "approved-native-fixture", "create"); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Database(ctx, p, d.ID, true)
	if err != nil || stored.Spec.Vitess == nil || stored.Spec.Vitess.BackupDestinationID != destination.ID {
		t.Fatal("native storage reference was not durable", err)
	}
	// This is a store fixture; native health is exercised separately on the development cluster.
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_databases SET status='ready',observation=$2 WHERE id=$1", d.ID, JSON(database.Observation{Status: "ready", Revision: 1})); err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: "vitess-client", Services: map[string]spec.Service{"api": {Image: "nginx:alpine"}}})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, "vitess-client-fixture")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanDatabaseConnection(ctx, p, d.ID, deployment.ApplicationID, "api", "DATABASE_URL", "read_write", false)
	if err != nil || plan.Binding.Protocol != "mysql" || plan.Binding.Endpoint != "read_write" {
		t.Fatal("Vitess connection review lost MySQL routing", err)
	}
	if _, err = s.PlanDatabaseConnection(ctx, p, d.ID, deployment.ApplicationID, "api", "DATABASE_URL", "read_only", false); err == nil {
		t.Fatal("standalone replica review accepted")
	}
	if _, err = s.PutBackupDestination(ctx, p, destination, destination.Revision); !errors.Is(err, backup.ErrConflict) {
		t.Fatal("in-use native keys rotated", err)
	}
	if err = s.DeleteBackupDestination(ctx, p, destination.ID, destination.Revision); !errors.Is(err, backup.ErrConflict) {
		t.Fatal("in-use native destination deleted", err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = lockArchiveDestinationTx(ctx, tx, destination.ID); !errors.Is(err, backup.ErrConflict) {
		t.Fatal("native destination reused for an archive", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	other := d
	other.ID = NewID()
	other.Spec.Name = "other-database"
	s.VitessBackupApprovals[0].Database = other.Spec.Name
	if _, err = s.AcceptDatabase(ctx, p, other, 0, "shared-native-fixture", "create"); !errors.Is(err, ErrConflict) {
		t.Fatal("native keys shared across databases", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_databases SET status='deleting' WHERE id=$1", d.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteBackupDestination(ctx, p, destination.ID, destination.Revision); !errors.Is(err, backup.ErrConflict) {
		t.Fatal("deletion released native keys before cleanup", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_databases SET deleted_at=now(),status='deleted' WHERE id=$1", d.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteBackupDestination(ctx, p, destination.ID, destination.Revision); err != nil {
		t.Fatal("completed cleanup retained destination reservation", err)
	}
}

func TestVitessAllocationAndBindingTargets(t *testing.T) {
	d := database.Resource{Status: "ready", Observation: database.Observation{Status: "ready"}, Spec: database.Spec{Engine: "vitess", Mode: "standalone", Shards: 1, Memory: "1Gi", StorageGiB: 5}}
	if got := DatabaseMemoryReservation(d.Spec); got != 9888<<20 {
		t.Fatalf("member/recovery memory = %d MiB; want 9888", got>>20)
	}
	if got := DatabaseStorageReservation(d.Spec); got != 18 {
		t.Fatalf("member/topology/transient storage = %d; want 18", got)
	}
	if err := validateDatabaseBinding(d, spec.Binding{Protocol: "mysql", Endpoint: "read_write"}); err != nil {
		t.Fatal(err)
	}
	for _, binding := range []spec.Binding{{Protocol: "vitess", Endpoint: "read_write"}, {Protocol: "mysql", Endpoint: "read_only"}, {Protocol: "mysql", Endpoint: "cluster", ClusterAware: true}} {
		if validateDatabaseBinding(d, binding) == nil {
			t.Fatal("unsupported Vitess route accepted")
		}
	}
	d.Spec.Mode, d.Spec.Replicas = "cluster", 1
	if err := validateDatabaseBinding(d, spec.Binding{Protocol: "mysql", Endpoint: "read_only"}); err != nil {
		t.Fatal(err)
	}
	d.Recovery = &database.Recovery{JobID: strings.Repeat("a", 32)}
	if validateDatabaseBinding(d, spec.Binding{Protocol: "mysql", Endpoint: "read_write"}) == nil {
		t.Fatal("uninspected recovery connected")
	}
}
