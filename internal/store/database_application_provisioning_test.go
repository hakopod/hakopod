package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestApplicationProvisioningRejectsInvalidBindingWithoutReview(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "create-provisioning-validation", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, op, database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now().UTC()}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: "provisioning-validation", Services: map[string]spec.Service{"main": {Image: "example.invalid/app@sha256:" + strings.Repeat("a", 64)}}})
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, "create-provisioning-app")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct{ variable, endpoint string }{{"not valid", "read_write"}, {"DATABASE_URL", "arbitrary"}} {
		_, err = s.PlanDatabaseApplicationProvisioning(ctx, p, d.ID, release.ApplicationID, "main", input.variable, input.endpoint, "hp_valid", "hp_valid", "database-password", []byte("encrypted-password-material-12345"))
		if !errors.Is(err, ErrInput) {
			t.Fatalf("invalid binding accepted: %q %q: %v", input.variable, input.endpoint, err)
		}
	}
	var reviews int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM managed_database_reviews WHERE kind='application-provisioning'`).Scan(&reviews); err != nil || reviews != 0 {
		t.Fatalf("invalid plan left a review: %d %v", reviews, err)
	}
}

func provisioningStoreFixture(t *testing.T) (*Store, Principal, database.Resource, Deployment, database.ApplicationProvisioningPlan) {
	t.Helper()
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "create-provisioning-fixture", "create"); err != nil {
		t.Fatal(err)
	}
	databaseOp, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, databaseOp, database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now().UTC()}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: "provisioning-fixture", Services: map[string]spec.Service{"main": {Image: "example.invalid/app@sha256:" + strings.Repeat("a", 64)}}})
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, "create-provisioning-fixture-app")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanDatabaseApplicationProvisioning(ctx, p, d.ID, release.ApplicationID, "main", "DATABASE_URL", "read_write", "hp_fixture", "hp_fixture", "database-password", []byte("encrypted-password-material-12345"))
	if err != nil {
		t.Fatal(err)
	}
	return s, p, d, release, plan
}

func TestApplicationProvisioningReplayReauthorizesCurrentKey(t *testing.T) {
	s, p, d, _, plan := provisioningStoreFixture(t)
	ctx := context.Background()
	first, err := s.AcceptDatabaseApplicationProvisioning(ctx, p, d.ID, plan.ID, plan.ApplicationName, "provisioning-replay")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.AcceptDatabaseApplicationProvisioning(ctx, p, d.ID, plan.ID, plan.ApplicationName, "provisioning-replay")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("authorized replay failed: %v", err)
	}
	narrowed := p
	narrowed.Admin, narrowed.Owner = false, false
	narrowed.Permissions, narrowed.IdentityPermissions, narrowed.ProjectRoles = nil, nil, nil
	if _, err = s.AcceptDatabaseApplicationProvisioning(ctx, narrowed, d.ID, plan.ID, plan.ApplicationName, "provisioning-replay"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("narrowed same-identity replay authorized: %v", err)
	}
}

func TestMigrationRecoveryReplayReauthorizesCurrentKey(t *testing.T) {
	s, p, d, release, _ := provisioningStoreFixture(t)
	ctx := context.Background()
	evidence := database.MigrationLockEvidence{SchemaVersion: 1, Profile: database.InfisicalKnexPostgresProfile, DatabaseID: d.ID, DatabaseRevision: 1, ApplicationID: release.ApplicationID, ApplicationRevision: release.Revision, Service: "main", Variable: "DATABASE_URL", LogicalDatabase: "infisical", ApplicationImage: "docker.io/infisical/infisical:v0.165.10@sha256:204bd63c7a281d9157752ce0bf8d506e7380cac5a0665324eeab8d580b069266", SchemaFingerprint: "infisical-knex-postgresql-v1:lock(index:int,is_locked:int):startup(index:int,is_locked:int,session_id:text,node:text,heartbeat:timestamptz)", KnexLockTable: "infisical_migrations_lock", KnexLockRows: 1, KnexLockedRows: 1, ObservedAt: time.Now().UTC()}
	plan, err := s.SaveMigrationLockRecoveryPlan(ctx, p, d.ID, release.ApplicationID, "main", database.InfisicalKnexPostgresProfile, evidence)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.AcceptMigrationLockRecovery(ctx, p, d.ID, plan.ID, plan.ApplicationName, plan.DatabaseName, "migration-replay")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.AcceptMigrationLockRecovery(ctx, p, d.ID, plan.ID, plan.ApplicationName, plan.DatabaseName, "migration-replay")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("authorized replay failed: %v", err)
	}
	narrowed := p
	narrowed.Admin, narrowed.Owner = false, false
	narrowed.Permissions, narrowed.IdentityPermissions, narrowed.ProjectRoles = nil, nil, nil
	if _, err = s.AcceptMigrationLockRecovery(ctx, narrowed, d.ID, plan.ID, plan.ApplicationName, plan.DatabaseName, "migration-replay"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("narrowed same-identity replay authorized: %v", err)
	}
}

func TestProvisionedBindingAndOperationOutcomeCommitAtomically(t *testing.T) {
	s, p, d, release, plan := provisioningStoreFixture(t)
	ctx := context.Background()
	accepted, err := s.AcceptDatabaseApplicationProvisioning(ctx, p, d.ID, plan.ID, plan.ApplicationName, "provisioning-atomic")
	if err != nil {
		t.Fatal(err)
	}
	running, err := s.ClaimDatabaseApplicationProvisioning(ctx)
	if err != nil || running.ID != accepted.ID {
		t.Fatalf("claim: %v", err)
	}
	connection, err := s.PlanDatabaseConnection(ctx, p, d.ID, release.ApplicationID, "main", "DATABASE_URL", "read_write", false, DatabaseConnectionOptions{Username: plan.Role, Database: plan.LogicalDatabase, Password: &spec.SecretRef{Ref: plan.SecretReference}, SSLMode: "verify-full"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptProvisionedDatabaseConnection(ctx, p, d.ID, connection.ID, plan.ApplicationName, "provision-bind-"+running.ID, running.ID, "wrong-lease"); !errors.Is(err, ErrConflict) {
		t.Fatalf("invalid lease did not abort transaction: %v", err)
	}
	unchanged, err := s.Application(ctx, release.ApplicationID)
	if err != nil || unchanged.Revision != release.Revision {
		t.Fatalf("failed completion changed application: r%d %v", unchanged.Revision, err)
	}
	stillRunning, err := s.DatabaseApplicationProvisioningOperation(ctx, p, running.ID)
	if err != nil || stillRunning.Status != "running" {
		t.Fatalf("failed completion changed operation: %s %v", stillRunning.Status, err)
	}
	deployment, err := s.AcceptProvisionedDatabaseConnection(ctx, p, d.ID, connection.ID, plan.ApplicationName, "provision-bind-"+running.ID, running.ID, running.Lease)
	if err != nil || deployment.Revision != release.Revision+1 {
		t.Fatalf("valid completion: r%d %v", deployment.Revision, err)
	}
	completed, err := s.DatabaseApplicationProvisioningOperation(ctx, p, running.ID)
	if err != nil || completed.Status != "succeeded" || completed.Phase != "deployment" {
		t.Fatalf("operation did not commit with binding: %s/%s %v", completed.Status, completed.Phase, err)
	}
}
