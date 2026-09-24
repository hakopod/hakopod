package store

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ServiceVolumeClaims derives the deletion set from the reviewed old and new
// specifications. Callers cannot nominate arbitrary PVCs or shared volumes.
func ServiceVolumeClaims(old, next spec.Application, services []string) ([]string, error) {
	if len(services) > 20 {
		return nil, fmt.Errorf("at most 20 removed services may request volume cleanup")
	}
	claims := map[string]bool{}
	seen := map[string]bool{}
	for _, name := range services {
		svc, exists := old.Services[name]
		if !exists || seen[name] {
			return nil, fmt.Errorf("volume cleanup requires a unique existing service: %s", name)
		}
		seen[name] = true
		if _, exists = next.Services[name]; exists {
			return nil, fmt.Errorf("remove service %s before deleting its volumes", name)
		}
		if svc.Volume != nil {
			claims[name+"-data"] = true
		}
		for _, m := range svc.Mounts {
			used := false
			for _, other := range next.Services {
				for _, mount := range other.Mounts {
					used = used || mount.Volume == m.Volume
				}
			}
			_, defined := next.Volumes[m.Volume]
			if !used && !defined {
				claims["hakopod-volume-"+m.Volume] = true
			}
		}
	}
	if len(services) > 0 && len(claims) == 0 {
		return nil, fmt.Errorf("there are no unused persistent volumes to delete")
	}
	out := []string{}
	for claim := range claims {
		out = append(out, claim)
	}
	sort.Strings(out)
	return out, nil
}

type VolumeCleanup struct {
	Claims []string `json:"claims"`
	Status string   `json:"status"`
	Error  string   `json:"error"`
}

func (s *Store) VolumeCleanup(ctx context.Context, id string) (*VolumeCleanup, error) {
	rows, err := s.Pool.Query(ctx, "SELECT c.claims,c.completed,c.error,d.status FROM deployment_volume_cleanup c JOIN deployments d ON d.id=c.deployment_id WHERE d.id=$1", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	v := VolumeCleanup{}
	var done bool
	var status string
	if err = rows.Scan(&v.Claims, &done, &v.Error, &status); err != nil {
		return nil, err
	}
	v.Status = "waiting"
	if done {
		v.Status = "deleted"
	} else if status == "succeeded" {
		v.Status = "reclaiming"
	} else if status == "failed" || status == "cancelled" {
		v.Status = "retained"
	}
	return &v, nil
}

type ServiceVolumeCleanupClaim struct {
	mu           sync.Mutex
	conn         *pgxpool.Conn
	store        *Store
	DeploymentID string
	App          Application
	Claims       []string
	key          string
}

func (s *Store) ClaimServiceVolumeCleanup(ctx context.Context) (*ServiceVolumeCleanupClaim, error) {
	rows, err := s.Pool.Query(ctx, "SELECT d.id,d.application_id FROM deployment_volume_cleanup c JOIN deployments d ON d.id=c.deployment_id WHERE NOT c.completed AND d.status='succeeded' ORDER BY c.attempted_at NULLS FIRST,d.created_at LIMIT 16")
	if err != nil {
		return nil, err
	}
	type item struct{ id, app string }
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.app); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, v := range items {
		conn, err := s.Pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		var locked bool
		if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,3))", v.app).Scan(&locked); err != nil || !locked {
			conn.Release()
			if err != nil {
				return nil, err
			}
			continue
		}
		c := &ServiceVolumeCleanupClaim{conn: conn, store: s, DeploymentID: v.id}
		var completed bool
		var status string
		err = conn.QueryRow(ctx, "SELECT c.claims,c.key_id,c.completed,d.status FROM deployment_volume_cleanup c JOIN deployments d ON d.id=c.deployment_id WHERE d.id=$1", v.id).Scan(&c.Claims, &c.key, &completed, &status)
		if err != nil || completed || status != "succeeded" {
			c.Release()
			if err != nil {
				return nil, err
			}
			continue
		}
		c.App, err = scanApp(conn.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id=$1", v.app))
		if err == nil {
			_, err = conn.Exec(ctx, "UPDATE deployment_volume_cleanup SET attempted_at=now() WHERE deployment_id=$1", v.id)
		}
		if err != nil {
			c.Release()
			return nil, err
		}
		return c, nil
	}
	return nil, nil
}
func (c *ServiceVolumeCleanupClaim) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		releaseClaimConnection(c.conn)
		c.conn = nil
	}
}
func (c *ServiceVolumeCleanupClaim) Check(ctx context.Context) error {
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
	if !p.CanManageApplication(c.App.Project, c.App.Environment, c.App.Name) {
		return ErrForbidden
	}
	if c.store.AuthorizeRetainedCleanup != nil {
		if err = c.store.AuthorizeRetainedCleanup(ctx, p, c.App.Project, c.App.Environment); err != nil {
			return err
		}
	}
	var valid bool
	err = c.conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM applications a JOIN deployments d ON d.application_id=a.id WHERE a.id=$1 AND d.id=$2 AND a.revision=d.revision AND d.status='succeeded')", c.App.ID, c.DeploymentID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	return nil
}
func (c *ServiceVolumeCleanupClaim) Finish(ctx context.Context, message string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.Conn().IsClosed() {
		return ErrClaimLost
	}
	if message != "" {
		_, err := c.conn.Exec(ctx, "UPDATE deployment_volume_cleanup SET error=$2 WHERE deployment_id=$1", c.DeploymentID, message)
		return err
	}
	tx, err := c.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,42))", c.App.Project+":"+c.App.Environment); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM storage_reservations WHERE application_id=$1 AND claim=ANY($2)", c.App.ID, c.Claims); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE deployment_volume_cleanup SET completed=true,error='' WHERE deployment_id=$1", c.DeploymentID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,action,resource,metadata) VALUES('system','service.volumes-deleted',$1,$2)", c.App.ID, JSON(map[string]any{"claims": c.Claims, "deployment_id": c.DeploymentID})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RetryServiceVolumeCleanup(ctx context.Context, p Principal, id string) error {
	d, err := s.Deployment(ctx, id)
	if err != nil {
		return err
	}
	a, err := s.Application(ctx, d.ApplicationID)
	if err != nil {
		return err
	}
	if !p.CanManageApplication(a.Project, a.Environment, a.Name) {
		return ErrForbidden
	}
	if d.Status != "succeeded" || a.Revision != d.Revision {
		return ErrConflict
	}
	result, err := s.Pool.Exec(ctx, "UPDATE deployment_volume_cleanup SET key_id=$2,error='' WHERE deployment_id=$1 AND NOT completed", id, p.KeyID)
	if err == nil && result.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}
