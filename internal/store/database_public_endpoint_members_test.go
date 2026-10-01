package store

import (
	"context"
	"github.com/hakopod/hakopod/internal/database"
	"testing"
)

func TestPublicMemberReservationRetainsEveryAllocationUntilFinalCleanup(t *testing.T) {
	s, _, d, legacy := activeDatabasePublicEndpointFixture(t)
	ctx := context.Background()
	e := database.PublicEndpoint{ID: NewID(), DatabaseID: d.ID, Spec: legacy.Spec, Status: "active"}
	e.Spec.Purpose = "read_only"
	for i, member := range []string{"database-0", "database-1"} {
		allocation := database.PublicEndpointAllocation{ID: member, Host: member + ".db.example.test", Address: "192.0.2.30", Port: int32(15440 + i)}
		e.MemberAllocations = append(e.MemberAllocations, database.PublicEndpointMemberAllocation{MemberName: member, MemberUID: member + "-uid", Allocation: allocation})
	}
	e.Allocation = e.MemberAllocations[0].Allocation
	if _, err := s.Pool.Exec(ctx, "INSERT INTO managed_database_public_endpoints(id,database_id,spec,allocation,member_allocations,status) VALUES($1,$2,$3,$4,$5,'active')", e.ID, e.DatabaseID, JSON(e.Spec), JSON(e.Allocation), JSON(e.MemberAllocations)); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.DatabasePublicEndpointInternal(ctx, e.ID)
	if err != nil || len(loaded.MemberAllocations) != 2 {
		t.Fatal("member inventory not durable", err)
	}
	members, err := s.DatabasePublicEndpointMembers(ctx, d.ID, "")
	if err != nil || len(members) != 2 {
		t.Fatal("member inventory unavailable to reconciliation", err)
	}
	names, err := s.DatabasePublicEndpointNames(ctx, d.ID, "")
	if err != nil || len(names) != 3 {
		t.Fatal("identity omitted a member hostname", err)
	}
	changed := append([]database.PublicEndpointMemberAllocation(nil), e.MemberAllocations...)
	changed[1].MemberUID = "replacement"
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_database_public_endpoints SET member_allocations=$2 WHERE id=$1", e.ID, JSON(changed)); err == nil {
		t.Fatal("existing reservation adopted an unreviewed member")
	}
	conflict := database.PublicEndpoint{ID: NewID(), DatabaseID: d.ID, Spec: legacy.Spec, Allocation: e.MemberAllocations[1].Allocation}
	conflict.Spec.Purpose = "pooled_read_write"
	insert := func() error {
		_, err := s.Pool.Exec(ctx, "INSERT INTO managed_database_public_endpoints(id,database_id,spec,allocation) VALUES($1,$2,$3,$4)", conflict.ID, conflict.DatabaseID, JSON(conflict.Spec), JSON(conflict.Allocation))
		return err
	}
	if err := insert(); err == nil {
		t.Fatal("secondary member allocation reused while active")
	}
	for _, status := range []string{"revoking", "error"} {
		if _, err := s.Pool.Exec(ctx, "UPDATE managed_database_public_endpoints SET status=$2 WHERE id=$1", e.ID, status); err != nil {
			t.Fatal(err)
		}
		if err := insert(); err == nil {
			t.Fatal("partial cleanup released a secondary member")
		}
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE managed_database_public_endpoints SET status='revoked',revoked_at=now() WHERE id=$1", e.ID); err != nil {
		t.Fatal(err)
	}
	if err := insert(); err != nil {
		t.Fatal("final cleanup did not release complete allocation set", err)
	}
}
