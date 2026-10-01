package store

import (
	"context"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

func TestMigration68UpgradesLegacyEndpointInventoryAtomically(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "create-migration-68-fixture", "create"); err != nil {
		t.Fatal(err)
	}
	operation, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, operation, database.Observation{}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}

	// Recreate the version-67 boundary in this isolated database. This avoids a
	// second migration implementation in the test while still exercising the
	// exact embedded version-68 migration through Store.Migrate.
	for _, statement := range []string{
		"DROP TRIGGER managed_database_public_endpoint_allocation_inventory ON managed_database_public_endpoints",
		"DROP FUNCTION maintain_database_public_endpoint_allocations()",
		"DROP TABLE managed_database_public_endpoint_allocations",
		"ALTER TABLE managed_database_public_endpoints DROP COLUMN member_allocations",
		"DELETE FROM schema_migrations WHERE version=68",
	} {
		if _, err = s.Pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	activeID, revokedID := NewID(), NewID()
	activeSpec := database.PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 32}
	revokedSpec := database.PublicEndpointSpec{Purpose: "read_only", SourceCIDRs: []string{"198.51.100.0/24"}, MaxConnections: 16}
	activeAllocation := database.PublicEndpointAllocation{ID: "legacy-active", Host: "legacy-active.db.example.test", Address: "192.0.2.10", Port: 15432}
	revokedAllocation := database.PublicEndpointAllocation{ID: "legacy-revoked", Host: "legacy-revoked.db.example.test", Address: "192.0.2.11", Port: 15433}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO managed_database_public_endpoints(id,database_id,spec,allocation,status) VALUES($1,$2,$3,$4,'active')", activeID, d.ID, JSON(activeSpec), JSON(activeAllocation)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO managed_database_public_endpoints(id,database_id,spec,allocation,status,revoked_at) VALUES($1,$2,$3,$4,'revoked',now())", revokedID, d.ID, JSON(revokedSpec), JSON(revokedAllocation)); err != nil {
		t.Fatal(err)
	}

	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, endpointID := range []string{activeID, revokedID} {
		var memberAllocations []database.PublicEndpointMemberAllocation
		if err = s.Pool.QueryRow(ctx, "SELECT member_allocations FROM managed_database_public_endpoints WHERE id=$1", endpointID).Scan(&memberAllocations); err != nil || len(memberAllocations) != 0 {
			t.Fatal("legacy endpoint did not receive an empty member inventory", endpointID, err, memberAllocations)
		}
	}
	var activeClaims, revokedClaims int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM managed_database_public_endpoint_allocations WHERE endpoint_id=$1", activeID).Scan(&activeClaims); err != nil || activeClaims != 1 {
		t.Fatal("active legacy endpoint was not backfilled exactly once", err, activeClaims)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM managed_database_public_endpoint_allocations WHERE endpoint_id=$1", revokedID).Scan(&revokedClaims); err != nil || revokedClaims != 0 {
		t.Fatal("revoked legacy endpoint unexpectedly retained an allocation", err, revokedClaims)
	}
	var allocationID, memberName, memberUID, host, address string
	var port int32
	if err = s.Pool.QueryRow(ctx, "SELECT allocation_id,member_name,member_uid,host,host(address),port FROM managed_database_public_endpoint_allocations WHERE endpoint_id=$1", activeID).Scan(&allocationID, &memberName, &memberUID, &host, &address, &port); err != nil {
		t.Fatal(err)
	}
	if allocationID != activeAllocation.ID || memberName != "" || memberUID != "" || host != activeAllocation.Host || address != activeAllocation.Address || port != activeAllocation.Port {
		t.Fatal("legacy allocation backfill changed the endpoint identity", allocationID, memberName, memberUID, host, address, port)
	}

	// The trigger inserts one row at a time. Force the second member to collide
	// with the legacy host and prove PostgreSQL rolls back the endpoint and the
	// first inserted inventory row as one statement.
	conflictID := NewID()
	members := []database.PublicEndpointMemberAllocation{
		{MemberName: "database-0", MemberUID: "uid-0", Allocation: database.PublicEndpointAllocation{ID: "candidate-first", Host: "candidate-first.db.example.test", Address: "192.0.2.20", Port: 15440}},
		{MemberName: "database-1", MemberUID: "uid-1", Allocation: database.PublicEndpointAllocation{ID: "candidate-conflict", Host: activeAllocation.Host, Address: "192.0.2.21", Port: 15441}},
	}
	conflictSpec := database.PublicEndpointSpec{Purpose: "native", SourceCIDRs: []string{"203.0.113.0/24"}, MaxConnections: 8}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO managed_database_public_endpoints(id,database_id,spec,allocation,member_allocations,status) VALUES($1,$2,$3,$4,$5,'active')", conflictID, d.ID, JSON(conflictSpec), JSON(members[0].Allocation), JSON(members)); err == nil {
		t.Fatal("conflicting member inventory was accepted")
	}
	var endpointRows, leakedClaims, preservedClaims int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM managed_database_public_endpoints WHERE id=$1", conflictID).Scan(&endpointRows); err != nil || endpointRows != 0 {
		t.Fatal("failed trigger left the endpoint row behind", err, endpointRows)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM managed_database_public_endpoint_allocations WHERE allocation_id=$1", members[0].Allocation.ID).Scan(&leakedClaims); err != nil || leakedClaims != 0 {
		t.Fatal("failed trigger leaked a partial member allocation", err, leakedClaims)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM managed_database_public_endpoint_allocations WHERE endpoint_id=$1 AND allocation_id=$2", activeID, activeAllocation.ID).Scan(&preservedClaims); err != nil || preservedClaims != 1 {
		t.Fatal("failed trigger changed the existing allocation inventory", err, preservedClaims)
	}
}
