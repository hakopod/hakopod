package store

import (
	"context"
	"fmt"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

// reserveStorage runs in revision acceptance. Removed volumes and deleted
// applications retain their reservation until an operator verifies reclamation.
// Kubernetes PVC requests alone do not enforce physical filesystem limits.
func (s *Store) reserveStorage(ctx context.Context, tx pgx.Tx, a Application, next spec.Application, migrationCredit ...int64) error {
	if s.StorageBudget == nil {
		return nil
	}
	limit, err := s.StorageBudget(ctx, a.Project, a.Environment)
	if err != nil {
		return err
	}
	if limit == 0 {
		return nil
	}
	volumes := map[string]int64{}
	for name, v := range next.Volumes {
		volumes["hakopod-volume-"+name] = v.SizeGiB
	}
	for name, service := range next.Services {
		if service.Volume != nil {
			volumes[name+"-data"] = service.Volume.SizeGiB
		}
	}
	if limit < 0 && len(volumes) > 0 {
		return fmt.Errorf("persistent storage is not configured for this workspace")
	}
	if len(volumes) == 0 {
		return nil
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,42))", a.Project+":"+a.Environment); err != nil {
		return err
	}
	for claim, size := range volumes {
		if size < 1 || size > limit {
			return fmt.Errorf("volume %s exceeds the %d GiB workspace storage quota", claim, limit)
		}
		var old int64
		err = tx.QueryRow(ctx, "INSERT INTO storage_reservations(project,environment,application_id,claim,size_gib) VALUES($1,$2,$3,$4,$5) ON CONFLICT(application_id,claim) DO UPDATE SET size_gib=storage_reservations.size_gib RETURNING size_gib", a.Project, a.Environment, a.ID, claim, size).Scan(&old)
		if err != nil {
			return err
		}
		if old != size {
			return fmt.Errorf("retained volume %s has a fixed size of %d GiB; create and migrate to a new volume", claim, old)
		}
	}
	var total int64
	if err = tx.QueryRow(ctx, "SELECT COALESCE(sum(size_gib),0) FROM storage_reservations WHERE project=$1 AND environment=$2", a.Project, a.Environment).Scan(&total); err != nil {
		return err
	}
	credit := int64(0)
	if len(migrationCredit) == 1 {
		credit = migrationCredit[0]
	}
	if total-credit > limit {
		return fmt.Errorf("workspace storage quota is %d GiB; %d GiB requested including retained volumes", limit, total)
	}
	return nil
}
