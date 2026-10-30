package store

import (
	"context"
	"time"
)

// RotateKey preserves the owning identity and scopes. Creation and the previous
// key's bounded overlap commit together, so a database error cannot leave an
// unknown replacement credential active alongside an unbounded original.
func (s *Store) RotateKey(ctx context.Context, p Principal, oldID string, expires time.Time, neverExpires bool) (Key, string, error) {
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
	err = tx.QueryRow(ctx, "SELECT identity_id,name,project,environment,application,permissions FROM api_keys WHERE id=$1 AND revoked_at IS NULL AND (expires_at>now() OR never_expires) AND NOT EXISTS(SELECT 1 FROM automation_key_scopes WHERE key_id=api_keys.id) FOR UPDATE", oldID).Scan(&owner, &in.Name, &in.Project, &in.Environment, &in.Application, &in.Permissions)
	if err != nil {
		return Key{}, "", err
	}
	in.ExpiresAt, in.NeverExpires = expires, neverExpires
	if err = validKeyInput(in, s.allowNeverExpiringKey(p)); err != nil {
		return Key{}, "", err
	}
	id, raw, digest := makeKey()
	var expiresAt *time.Time
	if !neverExpires {
		at := expires.UTC()
		expiresAt = &at
	}
	k := Key{ID: id, IdentityID: owner, Name: in.Name, Prefix: "hp_" + id[:8], Project: in.Project, Environment: in.Environment, Application: in.Application, Permissions: in.Permissions, ExpiresAt: expiresAt, NeverExpires: neverExpires, CreatedAt: time.Now().UTC()}
	_, err = tx.Exec(ctx, "INSERT INTO api_keys(id,identity_id,name,digest,prefix,project,environment,application,permissions,expires_at,never_expires,admin_never_expires) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)", id, owner, in.Name, digest, k.Prefix, in.Project, in.Environment, in.Application, in.Permissions, expiresAt, neverExpires)
	if err != nil {
		return Key{}, "", err
	}
	if _, err = tx.Exec(ctx, "UPDATE api_keys SET expires_at=COALESCE(LEAST(expires_at,now()+interval '15 minutes'),now()+interval '15 minutes'),never_expires=false,admin_never_expires=false WHERE id=$1", oldID); err != nil {
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
