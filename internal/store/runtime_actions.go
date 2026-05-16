package store

import (
	"context"
	"encoding/json"
	"time"
)

// RuntimeBase reads immutable input so a repeated action computes exactly the
// same accepted specification even after newer revisions have been queued.
func (s *Store) RuntimeBase(ctx context.Context, app string, revision int64) (Deployment, error) {
	return scanDep(s.Pool.QueryRow(ctx, "SELECT "+depCols+" FROM deployments WHERE application_id=$1 AND revision=$2", app, revision))
}

// Node maintenance spans multiple Kubernetes objects. Serialize its preflight
// and effects across API processes so two drains cannot each consider the other
// node to be the last remaining schedulable capacity.
func (s *Store) LockNodeActions(ctx context.Context, p Principal) (func(), error) {
	if !p.IsAdmin() {
		return nil, ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	release := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}
	var held bool
	if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended('hakopod-node-actions',7))").Scan(&held); err != nil {
		release()
		return nil, err
	}
	if !held {
		release()
		return nil, ErrConflict
	}
	return release, nil
}

func (s *Store) RuntimeAudit(ctx context.Context, p Principal, action, resource string, metadata any) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,$3,$4,$5)", p.ID, p.KeyID, action, resource, data)
	return err
}
