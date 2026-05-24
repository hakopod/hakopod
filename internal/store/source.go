package store

import (
	"context"
	"crypto/rand"
	"github.com/jackc/pgx/v5"
	"time"
)

// NewSourceGrant creates a non-login server grant; it cannot authenticate to the
// public API. Its owning person's current project roles are checked on every use.
func (s *Store) NewSourceGrant(ctx context.Context, tx pgx.Tx, p Principal, project, environment, app string) (string, error) {
	if !p.Allows("deployments:write", project, environment, app) {
		return "", ErrForbidden
	}
	id := NewID()
	digest := make([]byte, 32)
	if _, err := rand.Read(digest); err != nil {
		return "", err
	}
	_, err := tx.Exec(ctx, `INSERT INTO api_keys(id,identity_id,name,digest,prefix,project,environment,application,permissions,expires_at,kind) VALUES($1,$2,'GitHub deployment grant',$3,'internal',$4,$5,$6,ARRAY['deployments:read','deployments:write'], $7,'integration')`, id, p.ID, digest, project, environment, app, time.Now().Add(90*24*time.Hour))
	return id, err
}
