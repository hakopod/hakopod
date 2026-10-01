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
	actions     bool
	tlsIssuer   bool
}

func (s *Store) ClaimRuntime(ctx context.Context, application string, revision int64) (*RuntimeClaim, error) {
	return s.claimRuntime(ctx, application, revision, false)
}

// ClaimTLSIssuer permits provisioning after a failed or cancelled deployment so
// an application can repair its certificate configuration before redeploying.
// It retains the revision, deployment, deletion and volume resize fences.
func (s *Store) ClaimTLSIssuer(ctx context.Context, application string, revision int64) (*RuntimeClaim, error) {
	return s.claimRuntime(ctx, application, revision, true)
}

func (s *Store) claimRuntime(ctx context.Context, application string, revision int64, tlsIssuer bool) (*RuntimeClaim, error) {
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
	claim := &RuntimeClaim{conn: conn, application: application, revision: revision, tlsIssuer: tlsIssuer}
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
	if c.actions {
		var pending bool
		if err := c.conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployments WHERE application_id=$1 AND status IN ('queued','running'))`, c.application).Scan(&pending); err != nil {
			return err
		}
		if pending {
			return ErrClaimLost
		}
		return nil
	}
	var eligible bool
	var operation string
	err := c.conn.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM applications a JOIN deployments d ON d.application_id=a.id AND d.revision=a.revision
 WHERE a.id=$1 AND a.revision=$2 AND (d.status='succeeded' OR ($3 AND d.status IN ('failed','cancelled')))
 AND NOT EXISTS (SELECT 1 FROM volume_resizes vr WHERE vr.application_id=a.id AND `+resizeBlocking+`)
 AND NOT EXISTS (SELECT 1 FROM deployments pending WHERE pending.application_id=a.id AND pending.status IN ('queued','running'))
 ), COALESCE((SELECT id FROM deployments WHERE application_id=$1 AND revision=$2 AND (status='succeeded' OR ($3 AND status IN ('failed','cancelled')))),'')`, c.application, c.revision, c.tlsIssuer).Scan(&eligible, &operation)
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
