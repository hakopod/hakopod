package store

import (
	"context"
	"errors"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
)

func TestDatabaseCapacityPlanWorksWithoutSamplesOrCapacityPolicy(t *testing.T) {
	s, p, fixture := databaseFixture(t)
	plan, err := s.PlanDatabaseCapacity(context.Background(), p, fixture.Project, fixture.Environment, "", fixture.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Advisory || plan.Capacity.Known || plan.Capacity.Reason == "" {
		t.Fatalf("unexpected unknown capacity plan: %#v", plan.Capacity)
	}
	if plan.Usage.CurrentAvailable || plan.Usage.Samples24h != 0 || plan.Recommendation.Confidence != "low" {
		t.Fatalf("unexpected sample-free recommendation: %#v", plan)
	}
	if plan.RequestedAllocation.MemoryBytes != DatabaseMemoryReservation(fixture.Spec) || plan.RequestedAllocation.StorageGiB != DatabaseStorageReservation(fixture.Spec) {
		t.Fatalf("plan did not reuse database reservation formulas: %#v", plan.RequestedAllocation)
	}
}

func TestDatabaseCapacityResizeCountsOtherReservationsAndEnforcesScope(t *testing.T) {
	s, p, target := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, target, 0, "capacity-scope-target", "create"); err != nil {
		t.Fatal(err)
	}
	other := target
	other.ID = NewID()
	other.Spec.Name = "capacity-plan-other"
	other.Spec.CPU = "250m"
	other.Spec.Memory = "512Mi"
	other.Spec.StorageGiB = 3
	if _, err := s.AcceptDatabase(ctx, p, other, 0, "capacity-scope-other", "create"); err != nil {
		t.Fatal(err)
	}
	wantCPU, err := other.Spec.CPUReservationMilli()
	if err != nil {
		t.Fatal(err)
	}
	wantUsed := database.Capacity{CPUMilli: wantCPU, MemoryBytes: DatabaseMemoryReservation(other.Spec), StorageGiB: DatabaseStorageReservation(other.Spec)}
	plan, err := s.PlanDatabaseCapacity(ctx, p, target.Project, target.Environment, target.ID, target.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Capacity.Used != wantUsed {
		t.Fatalf("resize excluded another database reservation: got %#v want %#v", plan.Capacity.Used, wantUsed)
	}
	foreign := p
	foreign.Project = "elsewhere"
	if _, err = s.PlanDatabaseCapacity(ctx, foreign, target.Project, target.Environment, target.ID, target.Spec); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign principal planned database capacity: %v", err)
	}
	if _, err = s.PlanDatabaseCapacity(ctx, p, target.Project, "production", target.ID, target.Spec); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("database ID hid reservations outside its exact scope: %v", err)
	}
}

func TestDatabaseCapacityResizeExcludesCurrentAndRetainsRecoveryReservation(t *testing.T) {
	s, p, fixture := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, fixture, 0, "capacity-plan-create", "create"); err != nil {
		t.Fatal(err)
	}
	requestedCPU, err := fixture.Spec.CPUReservationMilli()
	if err != nil {
		t.Fatal(err)
	}
	retained := database.Capacity{CPUMilli: requestedCPU + 500, MemoryBytes: DatabaseMemoryReservation(fixture.Spec) + 1, StorageGiB: DatabaseStorageReservation(fixture.Spec) + 1}
	if _, err = s.Pool.Exec(ctx, `UPDATE managed_databases SET reserved_cpu_milli=$2,reserved_memory_bytes=$3,reserved_storage_gib=$4 WHERE id=$1`, fixture.ID, retained.CPUMilli, retained.MemoryBytes, retained.StorageGiB); err != nil {
		t.Fatal(err)
	}
	limit := database.Capacity{CPUMilli: retained.CPUMilli + 1000, MemoryBytes: retained.MemoryBytes + 1<<30, StorageGiB: retained.StorageGiB + 10}
	s.DatabaseCapacityBudget = func(context.Context, pgx.Tx, string, string) (database.Capacity, error) { return limit, nil }
	plan, err := s.PlanDatabaseCapacity(ctx, p, fixture.Project, fixture.Environment, fixture.ID, fixture.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Capacity.Used != (database.Capacity{}) {
		t.Fatalf("current reservation was counted as used: %#v", plan.Capacity.Used)
	}
	if plan.EffectiveAllocation != retained {
		t.Fatalf("effective allocation = %#v, want retained %#v", plan.EffectiveAllocation, retained)
	}
	if plan.Capacity.AfterPlan != (database.Capacity{CPUMilli: 1000, MemoryBytes: 1 << 30, StorageGiB: 10}) {
		t.Fatalf("wrong after-plan capacity: %#v", plan.Capacity.AfterPlan)
	}
}
