package store

import (
	"context"
	"time"
)

// RotateKey preserves the owning identity and scopes. Creation and the previous
// key's bounded overlap commit together, so a database error cannot leave an
// unknown replacement credential active alongside an unbounded original.
func (s *Store) RotateKey(ctx context.Context, p Principal, oldID string, expires time.Time) (Key, string, error) {
	if !p.IsAdmin() {
		return Key{}, "", ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Key{}, "", err
	}
	defer tx.Rollback(ctx)
	var in KeyInput
	var owner string
	err = tx.QueryRow(ctx, "SELECT identity_id,name,project,environment,application,permissions FROM api_keys WHERE id=$1 AND revoked_at IS NULL AND expires_at>now() FOR UPDATE", oldID).Scan(&owner, &in.Name, &in.Project, &in.Environment, &in.Application, &in.Permissions)
	if err != nil {
		return Key{}, "", err
	}
	in.ExpiresAt = expires
	if err = validKeyInput(in); err != nil {
		return Key{}, "", err
	}
	id, raw, digest := makeKey()
	k := Key{ID: id, IdentityID: owner, Name: in.Name, Prefix: "hp_" + id[:8], Project: in.Project, Environment: in.Environment, Application: in.Application, Permissions: in.Permissions, ExpiresAt: expires, CreatedAt: time.Now().UTC()}
	_, err = tx.Exec(ctx, "INSERT INTO api_keys(id,identity_id,name,digest,prefix,project,environment,application,permissions,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)", id, owner, in.Name, digest, k.Prefix, in.Project, in.Environment, in.Application, in.Permissions, expires)
	if err != nil {
		return Key{}, "", err
	}
	if _, err = tx.Exec(ctx, "UPDATE api_keys SET expires_at=LEAST(expires_at,now()+interval '15 minutes') WHERE id=$1", oldID); err != nil {
		return Key{}, "", err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'key.rotate',$3,$4)", p.ID, p.KeyID, oldID, JSON(map[string]any{"replacement_id": id, "overlap_seconds": 900})); err != nil {
		return Key{}, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return Key{}, "", err
	}
	return k, raw, nil
}
