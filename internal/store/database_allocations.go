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
	return (q.Value()+(50<<20))*int64(s.Members()+1) + (128 << 20)
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
	memory, storage := DatabaseMemoryReservation(d.Spec), d.Spec.StorageGiB*int64(d.Spec.Members())
	var oldMemory, oldStorage int64
	err := tx.QueryRow(ctx, "SELECT reserved_memory_bytes,reserved_storage_gib FROM managed_databases WHERE id=$1", d.ID).Scan(&oldMemory, &oldStorage)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	memory, storage = max(memory, oldMemory), max(storage, oldStorage)
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
	_, err = tx.Exec(ctx, "UPDATE managed_databases SET reserved_memory_bytes=$2,reserved_storage_gib=$3 WHERE id=$1", d.ID, memory, storage)
	return err
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
