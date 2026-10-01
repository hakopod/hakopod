package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
	"k8s.io/apimachinery/pkg/api/resource"
)

// DatabaseMemoryReservation includes one replacement member, sandbox overhead
// per member and a bounded recovery helper. The configured member sizes remain
// visible separately from the allocation needed to operate them safely.
func DatabaseMemoryReservation(s database.Spec) int64 {
	q := resource.MustParse(s.Memory)
	total := (q.Value()+(50<<20))*int64(s.Members()+1) + (128 << 20)
	if s.PoolerInstances() > 0 {
		total += int64(s.PoolerInstances()+s.PoolerRoutes()) * (306 << 20)
	}
	if s.Engine == "mysql" {
		total += int64(s.Members()+1) * (256 << 20)
		total += int64(s.RouterInstances()+1) * (178 << 20)
	}
	if s.Engine == "mongodb" {
		total += int64(s.Members()+1) * (256 << 20)
	}
	if s.KeeperInstances() > 0 {
		total += int64(s.KeeperInstances()+1) * (306 << 20)
	}
	if s.Engine == "vitess" {
		// Tablets share the mysqld pod. Every other component has its own
		// sandbox and replacement capacity, including the scoped operator.
		total += int64(s.Members()+1) * quantityBytes(database.VitessTabletMemory)
		total += int64(s.VitessGateways()+1) * (quantityBytes(database.VitessGatewayMemory) + (50 << 20))
		total += int64(s.VitessOrchestrators()+2) * (quantityBytes(database.VitessControlMemory) + (50 << 20))
		total += int64(s.VitessTopologyMembers()+1) * (quantityBytes(database.VitessTopologyMemory) + (50 << 20))
		total += 2 * (quantityBytes(database.VitessOperatorMemory) + (50 << 20))
		total += quantityBytes(database.VitessBackupControllerMemory) + (50 << 20)
		total += 2 * int64(s.Shards) * (q.Value() + (50 << 20))
	}
	total += int64(s.OracleBrokerInstances()) * (quantityBytes(database.OracleBrokerMemory) + (50 << 20))
	return total
}

func quantityBytes(value string) int64 {
	q := resource.MustParse(value)
	return q.Value()
}
func DatabaseStorageReservation(s database.Spec) int64 {
	perMember := s.StorageGiB
	if s.Engine == "mongodb" {
		perMember += database.MongoDBLogStorageGiB
	}
	if s.Engine == "clickhouse" || s.Engine == "oracle" {
		perMember *= 2
	}
	if s.Engine == "oracle" && s.Oracle != nil && s.Oracle.Edition == "enterprise" {
		// Enterprise keeps data, the fast recovery area and schema archive
		// staging on three separately bounded volumes per member.
		perMember = s.StorageGiB * 3
	}
	total := perMember*int64(s.Members()) + int64(s.KeeperInstances())*database.ClickHouseKeeperStorageGiB + int64(s.VitessTopologyMembers())*database.VitessTopologyStorageGiB
	if s.Engine == "vitess" {
		total += 2 * int64(s.Shards) * s.StorageGiB
	}
	return total
}
func applicationMemoryReservation(s spec.Application) (int64, error) {
	var total int64
	for _, svc := range s.Services {
		profile, ok := spec.Profiles[svc.Size]
		if !ok {
			profile = spec.Profiles["small"]
		}
		memory := profile.MemoryLimit
		if svc.Resources != nil && svc.Resources.MemoryLimit != "" {
			memory = svc.Resources.MemoryLimit
		}
		q, err := resource.ParseQuantity(memory)
		if err != nil {
			return 0, ErrInput
		}
		overhead := int64(50 << 20)
		if svc.Actions != nil {
			overhead = 512 << 20
		}
		total += (q.Value() + overhead) * int64(max(svc.Replicas, 1))
	}
	return total, nil
}

// The embedding locks workspace authority before the environment lock. Both
// application and database acceptance then serialize this shared allocation.
func (s *Store) reserveDatabaseAllocation(ctx context.Context, tx pgx.Tx, d database.Resource, kind string) error {
	if kind == "delete" {
		return nil
	}
	memory, storage := DatabaseMemoryReservation(d.Spec), DatabaseStorageReservation(d.Spec)
	cpu, err := d.Spec.CPUReservationMilli()
	if err != nil {
		return err
	}
	var oldMemory, oldStorage int64
	var oldCPU int64
	err = tx.QueryRow(ctx, "SELECT reserved_memory_bytes,reserved_storage_gib,reserved_cpu_milli FROM managed_databases WHERE id=$1", d.ID).Scan(&oldMemory, &oldStorage, &oldCPU)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	memory, storage = max(memory, oldMemory), max(storage, oldStorage)
	cpu = max(cpu, oldCPU)
	if oldCPU == 0 {
		cpu, err = historicalDatabaseCPU(ctx, tx, d.ID, cpu)
		if err != nil {
			return err
		}
	}
	if s.DatabaseCapacityBudget != nil {
		limit, e := s.DatabaseCapacityBudget(ctx, tx, d.Project, d.Environment)
		if e != nil {
			return e
		}
		if limit.CPUMilli < 1 || limit.MemoryBytes < 1 || limit.StorageGiB < 1 {
			return fmt.Errorf("database capacity allocation is unavailable")
		}
		usedCPU, usedMemory, usedStorage, e := databaseCapacityUsed(ctx, tx, d.Project, d.Environment, d.ID)
		if e != nil {
			return e
		}
		if usedCPU+cpu > limit.CPUMilli || usedMemory+memory > limit.MemoryBytes || usedStorage+storage > limit.StorageGiB {
			return fmt.Errorf("%w: databases exceed the reserved CPU, memory or storage allocation including replacement capacity", ErrConflict)
		}
	}
	if s.ComputeBudget != nil {
		limit, err := s.ComputeBudget(ctx, tx, d.Project, d.Environment)
		if err != nil {
			return err
		}
		if limit < 0 {
			return fmt.Errorf("workspace compute allocation is unavailable")
		}
		if limit > 0 {
			used, err := allocationMemory(ctx, tx, d.Project, d.Environment, "", nil, d.ID)
			if err != nil {
				return err
			}
			if used+memory+(256<<20) > limit {
				return fmt.Errorf("%w: database and application memory exceed the workspace allocation including recovery headroom", ErrConflict)
			}
		}
	}
	if s.StorageBudget != nil || s.StorageBudgetTx != nil {
		limit, err := s.storageBudgetTx(ctx, tx, d.Project, d.Environment)
		if err != nil {
			return err
		}
		if limit < 0 {
			return fmt.Errorf("persistent storage is unavailable for this workspace")
		}
		if limit > 0 {
			var used int64
			err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT sum(size_gib) FROM storage_reservations WHERE project=$1 AND environment=$2),0)+COALESCE((SELECT sum(reserved_storage_gib) FROM managed_databases WHERE project=$1 AND environment=$2 AND id<>$3 AND deleted_at IS NULL),0)`, d.Project, d.Environment, d.ID).Scan(&used)
			if err != nil {
				return err
			}
			if used+storage > limit {
				return fmt.Errorf("%w: database and retained application storage exceed the %d GiB workspace quota", ErrConflict, limit)
			}
		}
	}
	// Called after the database row is inserted or updated, in the same transaction.
	_, err = tx.Exec(ctx, "UPDATE managed_databases SET reserved_memory_bytes=$2,reserved_storage_gib=$3,reserved_cpu_milli=$4 WHERE id=$1", d.ID, memory, storage, cpu)
	return err
}

func databaseCapacityUsed(ctx context.Context, tx pgx.Tx, project, environment, except string) (cpu, memory, storage int64, err error) {
	rows, err := tx.Query(ctx, `SELECT id,spec,reserved_cpu_milli,reserved_memory_bytes,reserved_storage_gib FROM managed_databases WHERE project=$1 AND environment=$2 AND id<>$3 AND deleted_at IS NULL ORDER BY id LIMIT 65`, project, environment, except)
	if err != nil {
		return 0, 0, 0, err
	}
	type reservation struct {
		id                   string
		spec                 database.Spec
		cpu, memory, storage int64
	}
	var existing []reservation
	for rows.Next() {
		var r reservation
		if err = rows.Scan(&r.id, &r.spec, &r.cpu, &r.memory, &r.storage); err != nil {
			break
		}
		existing = append(existing, r)
	}
	rows.Close()
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		return 0, 0, 0, err
	}
	if len(existing) > database.MaxDatabases {
		return 0, 0, 0, ErrConflict
	}
	for _, r := range existing {
		if r.cpu == 0 {
			// Upgrade existing allocations conservatively from every retained
			// operation, including failed resizes that may leave old members.
			r.cpu, err = r.spec.CPUReservationMilli()
			if err != nil {
				return 0, 0, 0, err
			}
			r.cpu, err = historicalDatabaseCPU(ctx, tx, r.id, r.cpu)
			if err != nil {
				return 0, 0, 0, err
			}
			if _, err = tx.Exec(ctx, `UPDATE managed_databases SET reserved_cpu_milli=$2 WHERE id=$1 AND reserved_cpu_milli=0`, r.id, r.cpu); err != nil {
				return 0, 0, 0, err
			}
		}
		cpu += r.cpu
		memory += r.memory
		storage += r.storage
	}
	return
}

func historicalDatabaseCPU(ctx context.Context, tx pgx.Tx, id string, total int64) (int64, error) {
	history, err := tx.Query(ctx, `SELECT spec FROM managed_database_operations WHERE database_id=$1 ORDER BY revision DESC LIMIT 129`, id)
	if err != nil {
		return 0, err
	}
	defer history.Close()
	count := 0
	for history.Next() {
		count++
		var version database.Spec
		if err = history.Scan(&version); err != nil {
			return 0, err
		}
		value, e := version.CPUReservationMilli()
		if e != nil {
			return 0, e
		}
		total = max(total, value)
	}
	if err = history.Err(); err != nil {
		return 0, err
	}
	if count > 128 {
		return 0, fmt.Errorf("existing database CPU history exceeds the migration bound")
	}
	return total, nil
}

// CheckDatabaseCapacityReservation rejects a reduced operator grant while
// existing databases still hold a larger recovery envelope.
func (s *Store) CheckDatabaseCapacityReservation(ctx context.Context, project, environment string) error {
	if s.DatabaseCapacityBudget == nil {
		return fmt.Errorf("database capacity policy is unavailable")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	limit, err := s.DatabaseCapacityBudget(ctx, tx, project, environment)
	if err != nil {
		return err
	}
	var scope string
	if err = tx.QueryRow(ctx, `SELECT name FROM environments WHERE project=$1 AND name=$2 FOR UPDATE`, project, environment).Scan(&scope); err != nil {
		return err
	}
	cpu, memory, storage, err := databaseCapacityUsed(ctx, tx, project, environment, "")
	if err != nil {
		return err
	}
	if limit.CPUMilli < 1 || limit.MemoryBytes < 1 || limit.StorageGiB < 1 || cpu > limit.CPUMilli || memory > limit.MemoryBytes || storage > limit.StorageGiB {
		return fmt.Errorf("existing databases exceed the approved CPU, memory or storage capacity")
	}
	return tx.Commit(ctx)
}
func allocationMemory(ctx context.Context, tx pgx.Tx, project, environment, appID string, next *spec.Application, databaseID string) (int64, error) {
	var total int64
	if err := tx.QueryRow(ctx, "SELECT COALESCE(sum(reserved_memory_bytes),0) FROM managed_databases WHERE project=$1 AND environment=$2 AND id<>$3 AND deleted_at IS NULL", project, environment, databaseID).Scan(&total); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `SELECT a.id,a.spec,COALESCE((SELECT jsonb_agg(jsonb_build_array(d.spec,d.resolved_spec,d.recovery_spec)) FROM (
 SELECT spec,resolved_spec,recovery_spec FROM deployments WHERE application_id=a.id AND (
 status IN ('queued','running') OR id=(SELECT id FROM deployments WHERE application_id=a.id ORDER BY revision DESC LIMIT 1)
 OR id=(SELECT id FROM deployments WHERE application_id=a.id AND status='succeeded' ORDER BY revision DESC LIMIT 1)) ORDER BY revision DESC LIMIT 33
 ) d),'[]'::jsonb) FROM applications a WHERE project=$1 AND environment=$2 ORDER BY id LIMIT 201`, project, environment)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count, found := 0, false
	for rows.Next() {
		count++
		var id string
		var app spec.Application
		var raw []byte
		if err = rows.Scan(&id, &app, &raw); err != nil {
			return 0, err
		}
		var revisions [][]*spec.Application
		if err = json.Unmarshal(raw, &revisions); err != nil {
			return 0, err
		}
		if len(revisions) > 32 {
			return 0, ErrConflict
		}
		candidates := []spec.Application{app}
		for _, revision := range revisions {
			for _, version := range revision {
				if version != nil {
					candidates = append(candidates, *version)
				}
			}
		}
		if id == appID && next != nil {
			found = true
			candidates = append(candidates, *next)
		}
		// A failed rollout may leave both renamed services and the previous replica
		// count running. Reserve the per-service maximum until a replacement succeeds.
		members := map[string]int64{}
		for _, candidate := range candidates {
			for name, svc := range candidate.Services {
				amount, e := applicationMemoryReservation(spec.Application{Services: map[string]spec.Service{name: svc}})
				if e != nil {
					return 0, e
				}
				members[name] = max(members[name], amount)
			}
		}
		for _, amount := range members {
			total += amount
		}
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	if count > 200 {
		return 0, ErrConflict
	}
	if !found && next != nil {
		amount, e := applicationMemoryReservation(*next)
		if e != nil {
			return 0, e
		}
		total += amount
	}

	return total, nil
}
func (s *Store) checkApplicationAllocation(ctx context.Context, tx pgx.Tx, a Application, next spec.Application) error {
	if s.ComputeBudget == nil {
		return nil
	}
	limit, err := s.ComputeBudget(ctx, tx, a.Project, a.Environment)
	if err != nil {
		return err
	}
	if limit == 0 {
		return nil
	}
	used, err := allocationMemory(ctx, tx, a.Project, a.Environment, a.ID, &next, "")
	if err != nil {
		return err
	}
	if limit < 0 || used+(256<<20) > limit {
		return fmt.Errorf("%w: application and database memory exceed the workspace allocation including recovery headroom", ErrConflict)
	}
	return nil
}
