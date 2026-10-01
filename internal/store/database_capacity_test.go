package store

import (
	"context"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
	"testing"
)

func TestDatabaseCPUCapacitySerializesConcurrentCreates(t *testing.T) {
	s, p, first := databaseFixture(t)
	ctx := context.Background()
	cpu, err := first.Spec.CPUReservationMilli()
	if err != nil {
		t.Fatal(err)
	}
	s.DatabaseCapacityBudget = func(context.Context, pgx.Tx, string, string) (database.Capacity, error) {
		return database.Capacity{CPUMilli: cpu, MemoryBytes: 1 << 40, StorageGiB: 128}, nil
	}
	second := first
	second.ID = NewID()
	second.Spec.Name = "another-database"
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, d := range []database.Resource{first, second} {
		go func(index int, item database.Resource) {
			<-start
			_, e := s.AcceptDatabase(ctx, p, item, 0, []string{"capacity-first", "capacity-second"}[index], "create")
			results <- e
		}(i, d)
	}
	close(start)
	accepted := 0
	for range 2 {
		if <-results == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d databases in one CPU envelope", accepted)
	}
	var reserved int64
	if err = s.Pool.QueryRow(ctx, `SELECT sum(reserved_cpu_milli) FROM managed_databases WHERE deleted_at IS NULL`).Scan(&reserved); err != nil || reserved != cpu {
		t.Fatal(reserved, err)
	}
}

func TestDatabaseCPUCapacityPreservesHistoryDuringMigration(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	d.Spec.CPU = "1000m"
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "cpu-history-create", "create"); err != nil {
		t.Fatal(err)
	}
	want, err := d.Spec.CPUReservationMilli()
	if err != nil {
		t.Fatal(err)
	}
	d.Spec.CPU = "100m"
	if _, err = s.Pool.Exec(ctx, `UPDATE managed_databases SET spec=$2,reserved_cpu_milli=0 WHERE id=$1`, d.ID, d.Spec); err != nil {
		t.Fatal(err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	used, _, _, err := databaseCapacityUsed(ctx, tx, d.Project, d.Environment, "")
	if err != nil || used != want {
		t.Fatalf("history reservation = %d, want %d: %v", used, want, err)
	}
	if _, err = tx.Exec(ctx, `UPDATE managed_databases SET reserved_cpu_milli=0 WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.reserveDatabaseAllocation(ctx, tx, d, "resize"); err != nil {
		t.Fatal(err)
	}
	var reserved int64
	if err = tx.QueryRow(ctx, `SELECT reserved_cpu_milli FROM managed_databases WHERE id=$1`, d.ID).Scan(&reserved); err != nil || reserved != want {
		t.Fatalf("resize reservation = %d, want %d: %v", reserved, want, err)
	}
}

func TestDatabaseCPUCapacityBoundsMigrationHistory(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "cpu-history-bound", "create"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO managed_database_operations(id,database_id,revision,identity_id,key_id,idempotency_key,request_hash,kind,spec,status)
 SELECT o.id||'-history-'||n,o.database_id,n+1,o.identity_id,o.key_id,'cpu-history-'||n,o.request_hash,'resize',o.spec,'succeeded'
 FROM managed_database_operations o CROSS JOIN generate_series(1,128) n WHERE o.database_id=$1 AND o.revision=1`, d.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = historicalDatabaseCPU(ctx, tx, d.ID, 0); err == nil {
		t.Fatal("unbounded legacy operation history accepted")
	}
}

func TestDatabaseCPUCapacityRejectsReducedOperatorGrant(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "cpu-reduced-grant", "create"); err != nil {
		t.Fatal(err)
	}
	cpu, err := d.Spec.CPUReservationMilli()
	if err != nil {
		t.Fatal(err)
	}
	limit := database.Capacity{CPUMilli: cpu, MemoryBytes: DatabaseMemoryReservation(d.Spec), StorageGiB: DatabaseStorageReservation(d.Spec)}
	s.DatabaseCapacityBudget = func(context.Context, pgx.Tx, string, string) (database.Capacity, error) { return limit, nil }
	if err = s.CheckDatabaseCapacityReservation(ctx, d.Project, d.Environment); err != nil {
		t.Fatal(err)
	}
	limit.CPUMilli--
	if s.CheckDatabaseCapacityReservation(ctx, d.Project, d.Environment) == nil {
		t.Fatal("reduced CPU grant accepted")
	}
	limit.CPUMilli++
	limit.MemoryBytes--
	if s.CheckDatabaseCapacityReservation(ctx, d.Project, d.Environment) == nil {
		t.Fatal("reduced memory grant accepted")
	}
	limit.MemoryBytes++
	limit.StorageGiB = 0
	if s.CheckDatabaseCapacityReservation(ctx, d.Project, d.Environment) == nil {
		t.Fatal("reduced storage grant accepted")
	}
}
