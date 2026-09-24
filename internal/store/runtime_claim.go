package store

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RuntimeClaim serializes opt-in maintenance with deployments without holding
// a database transaction during Kubernetes requests.
type RuntimeClaim struct {
	mu          sync.Mutex
	conn        *pgxpool.Conn
	application string
	revision    int64
	OperationID string
}

func (s *Store) ClaimRuntime(ctx context.Context, application string, revision int64) (*RuntimeClaim, error) {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var locked bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,3))", application).Scan(&locked); err != nil {
		releaseClaimConnection(conn)
		return nil, err
	}
	if !locked {
		conn.Release()
		return nil, nil
	}
	claim := &RuntimeClaim{conn: conn, application: application, revision: revision}
	if err = claim.Check(ctx); err != nil {
		claim.Release()
		if err == ErrClaimLost {
			return nil, nil
		}
		return nil, err
	}
	return claim, nil
}

func (c *RuntimeClaim) Check(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.Conn().IsClosed() {
		return ErrClaimLost
	}
	var eligible bool
	var operation string
	err := c.conn.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM applications a JOIN deployments d ON d.application_id=a.id AND d.revision=a.revision
 WHERE a.id=$1 AND a.revision=$2 AND d.status='succeeded'
 AND NOT EXISTS (SELECT 1 FROM volume_resizes vr WHERE vr.application_id=a.id AND `+resizeBlocking+`)
 AND NOT EXISTS (SELECT 1 FROM deployments pending WHERE pending.application_id=a.id AND pending.status IN ('queued','running'))
 ), COALESCE((SELECT id FROM deployments WHERE application_id=$1 AND revision=$2 AND status='succeeded'),'')`, c.application, c.revision).Scan(&eligible, &operation)
	if err != nil {
		return err
	}
	if !eligible {
		return ErrClaimLost
	}
	c.OperationID = operation
	return nil
}

func (c *RuntimeClaim) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return
	}
	releaseClaimConnection(c.conn)
	c.conn = nil
}
