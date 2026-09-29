package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ActionsPoolWork leases scheduling only. The application's runtime advisory
// lock remains mandatory before any provider or Kubernetes mutation.
type ActionsPoolWork struct {
	Pool  ActionsPool
	Lease time.Time
}

// NextActionsPool reserves the oldest due pool atomically. No fleet inventory
// is loaded into memory, and another process can resume work after a crash.
func (s *Store) NextActionsPool(ctx context.Context) (*ActionsPoolWork, error) {
	w := &ActionsPoolWork{}
	p := &w.Pool
	var data []byte
	err := s.Pool.QueryRow(ctx, `WITH due AS (
	 SELECT application_id,service FROM actions_pools
	 WHERE next_reconcile_at<=now()
	 ORDER BY last_served_at,next_reconcile_at,application_id,service
	 FOR UPDATE SKIP LOCKED LIMIT 1
	) UPDATE actions_pools p SET next_reconcile_at=now()+interval '1 minute'
	FROM due WHERE p.application_id=due.application_id AND p.service=due.service
	RETURNING p.application_id,p.service,p.project,p.environment,p.application_name,p.revision,p.config,p.removed,p.message,p.updated_at,p.next_reconcile_at`).Scan(&p.ApplicationID, &p.Service, &p.Project, &p.Environment, &p.ApplicationName, &p.Revision, &data, &p.Removed, &p.Message, &p.UpdatedAt, &w.Lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &p.Config); err != nil {
		return nil, err
	}
	return w, nil
}

// CompleteActionsPool cannot overwrite a newer lease or a deployment wakeup.
func (s *Store) CompleteActionsPool(ctx context.Context, w ActionsPoolWork, next time.Time, progress bool) error {
	// Removed pools with no registrations only need to wake when history expires.
	// Retaining metadata must not compete with active pools every ten seconds.
	_, err := s.Pool.Exec(ctx, `UPDATE actions_pools p SET next_reconcile_at=CASE
 WHEN p.removed AND NOT EXISTS(SELECT 1 FROM actions_slots s WHERE s.application_id=p.application_id AND s.service=p.service)
 THEN COALESCE((SELECT min(j.created_at)+interval '30 days' FROM actions_jobs j
 WHERE j.application_id=p.application_id AND j.service=p.service AND j.created_at>=now()-interval '30 days'),$4)
 ELSE $4 END,
 last_served_at=CASE WHEN $5 THEN now() ELSE last_served_at END
 WHERE application_id=$1 AND service=$2 AND next_reconcile_at=$3`, w.Pool.ApplicationID, w.Pool.Service, w.Lease, next, progress)
	return err
}
