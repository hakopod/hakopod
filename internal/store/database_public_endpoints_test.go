package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
)

func activeDatabasePublicEndpointFixture(t *testing.T) (*Store, Principal, database.Resource, database.PublicEndpoint) {
	t.Helper()
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "create-public-endpoint-fixture", "create"); err != nil {
		t.Fatal(err)
	}
	operation, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, operation, database.Observation{}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	endpoint := database.PublicEndpoint{ID: NewID(), DatabaseID: d.ID, Revision: 1, Spec: database.PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 32}, Allocation: database.PublicEndpointAllocation{ID: "allocation", Host: "database-15432.example.test", Address: "192.0.2.10", Port: 15432}, Status: "active"}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO managed_database_public_endpoints(id,database_id,revision,spec,allocation,status) VALUES($1,$2,$3,$4,$5,$6)", endpoint.ID, endpoint.DatabaseID, endpoint.Revision, JSON(endpoint.Spec), JSON(endpoint.Allocation), endpoint.Status); err != nil {
		t.Fatal(err)
	}
	d, err = s.Database(ctx, p, d.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	return s, p, d, endpoint
}

func TestDatabasePublicEndpointReplayReauthorizesWriteScope(t *testing.T) {
	s, p, d, endpoint := activeDatabasePublicEndpointFixture(t)
	ctx := context.Background()
	if _, err := s.RevokeDatabasePublicEndpoint(ctx, p, d.ID, endpoint.ID, "public-endpoint-replay", endpoint.Revision); err != nil {
		t.Fatal(err)
	}
	narrowed := p
	narrowed.Admin = false
	narrowed.Permissions = []string{"deployments:read"}
	if _, err := s.RevokeDatabasePublicEndpoint(ctx, narrowed, d.ID, endpoint.ID, "public-endpoint-replay", endpoint.Revision); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("idempotent replay bypassed current write authorization", err)
	}
}

func TestDatabasePublicEndpointWorkerOwnsDatabaseMaintenanceLane(t *testing.T) {
	s, p, d, endpoint := activeDatabasePublicEndpointFixture(t)
	ctx := context.Background()
	if _, err := s.RevokeDatabasePublicEndpoint(ctx, p, d.ID, endpoint.ID, "public-endpoint-maintenance", endpoint.Revision); err != nil {
		t.Fatal(err)
	}
	operation, err := s.ClaimDatabasePublicEndpointOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claim, err := s.ClaimDatabaseMaintenance(ctx, d.ID, d.Revision); err != nil || claim != nil {
		t.Fatal("certificate maintenance raced a public endpoint operation", err)
	}
	if err = operationCheckDatabasePublicEndpoint(t, s, operation); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabasePublicEndpointStep(ctx, operation, database.PublicEndpointObservation{}, "failed", "closing", "fixture failure"); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimDatabaseMaintenance(ctx, d.ID, d.Revision)
	if err != nil || claim == nil {
		t.Fatal("database maintenance lane was not released", err)
	}
	claim.Release()
}

func TestDatabasePublicEndpointRetryPhasePersistsAcrossWorkerClaims(t *testing.T) {
	s, p, d, endpoint := activeDatabasePublicEndpointFixture(t)
	ctx := context.Background()
	if _, err := s.RevokeDatabasePublicEndpoint(ctx, p, d.ID, endpoint.ID, "public-endpoint-retry-phases", endpoint.Revision); err != nil {
		t.Fatal(err)
	}
	operation, err := s.ClaimDatabasePublicEndpointOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"identity", "release", "publishing", "cancelling"} {
		if err = s.RecordDatabasePublicEndpointStep(ctx, operation, database.PublicEndpointObservation{}, "queued", phase, "retry checkpoint"); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Pool.Exec(ctx, "UPDATE managed_database_public_endpoint_operations SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1", operation.ID); err != nil {
			t.Fatal(err)
		}
		operation, err = s.ClaimDatabasePublicEndpointOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if operation.Phase != phase {
			t.Fatalf("retry lost durable phase: got %q want %q", operation.Phase, phase)
		}
	}
}

func TestDatabasePublicEndpointCleanupFenceSurvivesRevokedAuthority(t *testing.T) {
	s, p, d, endpoint := activeDatabasePublicEndpointFixture(t)
	ctx := context.Background()
	if _, err := s.RevokeDatabasePublicEndpoint(ctx, p, d.ID, endpoint.ID, "public-endpoint-revoked-cleanup", endpoint.Revision); err != nil {
		t.Fatal(err)
	}
	operation, err := s.ClaimDatabasePublicEndpointOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=clock_timestamp() WHERE id=$1", operation.KeyID); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckDatabasePublicEndpointOperation(ctx, operation); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked actor retained mutation authority", err)
	}
	if err = s.CheckDatabasePublicEndpointOperationCleanup(ctx, operation); err != nil {
		t.Fatal("revoked actor prevented fenced fail-closed cleanup", err)
	}
	stale := operation
	stale.Lease = NewID()
	if err = s.CheckDatabasePublicEndpointOperationCleanup(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatal("stale worker passed the cleanup fence", err)
	}
}

func TestDatabasePublicEndpointIdentityTransitionIsDurableAndFenced(t *testing.T) {
	s, p, d, endpoint := activeDatabasePublicEndpointFixture(t)
	ctx := context.Background()
	if _, err := s.RevokeDatabasePublicEndpoint(ctx, p, d.ID, endpoint.ID, "public-endpoint-transition", endpoint.Revision); err != nil {
		t.Fatal(err)
	}
	operation, err := s.ClaimDatabasePublicEndpointOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transition := database.PublicEndpointIdentityTransition{
		SchemaVersion: database.PublicEndpointIdentityTransitionSchemaVersion, OperationID: operation.ID, Engine: "oracle", Kind: operation.Kind,
		DatabaseRevision: d.Revision, EndpointRevision: operation.Revision, RouteFingerprint: "route", ReviewedTopologyFingerprint: "old-topology",
		ReviewedLeafFingerprint: "old-leaf", ReviewedCAFingerprint: "ca", StatefulSetUID: "statefulset", OriginalGeneration: 7,
		OriginalTemplateHash: "old-template", OldMember: database.PublicEndpointTransitionMember{Name: "database-0", UID: "old-pod"},
		PVCs: []database.PublicEndpointTransitionPVC{{Name: "data-database-0", UID: "data-uid", VolumeName: "data-volume", PersistentVolumeUID: "data-pv-uid", BackingVolumeFingerprint: "data-backing"}, {Name: "backup-database-0", UID: "backup-uid", VolumeName: "backup-volume", PersistentVolumeUID: "backup-pv-uid", BackingVolumeFingerprint: "backup-backing"}},
	}
	if err = s.SaveDatabasePublicEndpointIdentityTransition(ctx, operation, transition); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.DatabasePublicEndpointOperation(ctx, p, operation.ID)
	if err != nil || loaded.IdentityTransition == nil || !reflect.DeepEqual(*loaded.IdentityTransition, transition) {
		t.Fatal("identity transition did not survive a store round trip", err, loaded.IdentityTransition)
	}
	stale := operation
	stale.Lease = NewID()
	transition.ProposedLeafFingerprint = "must-not-persist"
	if err = s.SaveDatabasePublicEndpointIdentityTransition(ctx, stale, transition); !errors.Is(err, ErrConflict) {
		t.Fatal("stale identity transition writer passed the operation fence", err)
	}
	loaded, err = s.DatabasePublicEndpointOperation(ctx, p, operation.ID)
	if err != nil || loaded.IdentityTransition.ProposedLeafFingerprint != "" {
		t.Fatal("stale writer changed the persisted transition", err)
	}
}

func operationCheckDatabasePublicEndpoint(t *testing.T, s *Store, operation database.PublicEndpointOperation) error {
	t.Helper()
	return s.CheckDatabasePublicEndpointOperation(context.Background(), operation)
}
