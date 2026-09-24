package store

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RetainedApplication struct {
	ApplicationID string `json:"application_id"`
	Project       string `json:"project"`
	Environment   string `json:"environment"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	Error         string `json:"error"`
	ReservedGiB   int64  `json:"reserved_gib"`
}

const retainedColumns = "application_id,project,environment,name,status,error"

func scanRetained(row pgx.Row) (RetainedApplication, error) {
	var v RetainedApplication
	err := row.Scan(&v.ApplicationID, &v.Project, &v.Environment, &v.Name, &v.Status, &v.Error)
	return v, err
}

func (s *Store) RetainedApplications(ctx context.Context, p Principal, project, environment string) ([]RetainedApplication, error) {
	if !p.CanManageApplication(project, environment, "") {
		return nil, ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, "SELECT "+retainedColumns+",COALESCE((SELECT sum(size_gib) FROM storage_reservations r WHERE r.application_id=d.application_id),0) FROM retained_application_data d WHERE project=$1 AND environment=$2 AND status<>'deleted' ORDER BY name,application_id LIMIT 100", project, environment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RetainedApplication{}
	for rows.Next() {
		var v RetainedApplication
		if err = rows.Scan(&v.ApplicationID, &v.Project, &v.Environment, &v.Name, &v.Status, &v.Error, &v.ReservedGiB); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) RequestRetainedCleanup(ctx context.Context, p Principal, id, confirmation string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	v, err := scanRetained(tx.QueryRow(ctx, "SELECT "+retainedColumns+" FROM retained_application_data WHERE application_id=$1 FOR UPDATE", id))
	if err != nil {
		return err
	}
	if !p.CanManageApplication(v.Project, v.Environment, v.Name) {
		return ErrForbidden
	}
	if v.Name != confirmation {
		return fmt.Errorf("%w: confirm the deleted application name to permanently erase its retained data", ErrInput)
	}
	var active bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM applications WHERE id=$1)", id).Scan(&active); err != nil {
		return err
	}
	if active {
		return fmt.Errorf("%w: delete the empty application before reclaiming data", ErrConflict)
	}
	if v.Status == "deleted" {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, "UPDATE retained_application_data SET status='deleting',requested_key_id=$2,requested_at=now(),error='' WHERE application_id=$1", id, p.KeyID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'application.data-delete-request',$3)", p.ID, p.KeyID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type RetainedCleanupClaim struct {
	mu    sync.Mutex
	conn  *pgxpool.Conn
	store *Store
	Data  RetainedApplication
	key   string
}

func (c *RetainedCleanupClaim) Check(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.Conn().IsClosed() {
		return ErrClaimLost
	}
	if err := c.conn.Ping(ctx); err != nil {
		return err
	}
	p, err := c.store.KeyPrincipal(ctx, c.key)
	if err != nil {
		return err
	}
	if !p.CanManageApplication(c.Data.Project, c.Data.Environment, c.Data.Name) {
		return ErrForbidden
	}
	if c.store.AuthorizeRetainedCleanup != nil {
		return c.store.AuthorizeRetainedCleanup(ctx, p, c.Data.Project, c.Data.Environment)
	}
	return nil
}
func (c *RetainedCleanupClaim) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		releaseClaimConnection(c.conn)
		c.conn = nil
	}
}
func (s *Store) ClaimRetainedCleanup(ctx context.Context) (*RetainedCleanupClaim, error) {
	rows, err := s.Pool.Query(ctx, "SELECT application_id FROM retained_application_data WHERE status='deleting' ORDER BY attempted_at NULLS FIRST,requested_at LIMIT 16")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		conn, err := s.Pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		var locked bool
		if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,3))", id).Scan(&locked); err != nil || !locked {
			conn.Release()
			if err != nil {
				return nil, err
			}
			continue
		}
		v, err := scanRetained(conn.QueryRow(ctx, "UPDATE retained_application_data SET attempted_at=now() WHERE application_id=$1 AND status='deleting' AND NOT EXISTS(SELECT 1 FROM applications WHERE id=$1) RETURNING "+retainedColumns, id))
		if err != nil {
			releaseClaimConnection(conn)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return nil, err
		}
		var key string
		if err = conn.QueryRow(ctx, "SELECT requested_key_id FROM retained_application_data WHERE application_id=$1", id).Scan(&key); err != nil {
			releaseClaimConnection(conn)
			return nil, err
		}
		return &RetainedCleanupClaim{conn: conn, store: s, Data: v, key: key}, nil
	}
	return nil, nil
}
func (c *RetainedCleanupClaim) Finish(ctx context.Context, cleanupError string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.Conn().IsClosed() {
		return ErrClaimLost
	}
	if cleanupError != "" {
		_, err := c.conn.Exec(ctx, "UPDATE retained_application_data SET error=$2 WHERE application_id=$1", c.Data.ApplicationID, cleanupError)
		return err
	}
	tx, err := c.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,42))", c.Data.Project+":"+c.Data.Environment); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM storage_reservations WHERE application_id=$1", c.Data.ApplicationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE retained_application_data SET status='deleted',error='' WHERE application_id=$1", c.Data.ApplicationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,action,resource) VALUES('system','application.data-deleted',$1)", c.Data.ApplicationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
