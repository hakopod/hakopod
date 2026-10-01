package store

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// CreateBoundKey is for trusted product issuers. The caller supplies an already
// authorized workspace binding; ordinary API key creation never reaches it.
// Only the Cloud issuer may set allowNeverExpires after its workspace checks.
func (s *Store) CreateBoundKey(ctx context.Context, p Principal, in BoundKeyInput, scope, binding, rotate string, allowNeverExpires bool) (Key, string, error) {
	if p.CredentialType != "browser" || !p.IsHuman() || p.MFARequired {
		return Key{}, "", ErrForbidden
	}
	if err := validBoundKeyInput(in, allowNeverExpires); err != nil {
		return Key{}, "", fmt.Errorf("%w: %s", ErrInput, err)
	}
	if scope == "" || len(scope) > 128 || binding == "" || len(binding) > 512 || in.Application != "" || slices.Contains(in.Permissions, "admin") || len(in.Permissions) > 6 {
		return Key{}, "", ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Key{}, "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,793044300))", scope); err != nil {
		return Key{}, "", err
	}
	if rotate != "" {
		var old Key
		var oldBinding string
		err = tx.QueryRow(ctx, `SELECT k.identity_id,k.name,k.project,k.environment,k.permissions,s.binding FROM api_keys k JOIN automation_key_scopes s ON s.key_id=k.id WHERE k.id=$1 AND s.scope_id=$2 AND k.revoked_at IS NULL AND (k.expires_at>now() OR k.never_expires) FOR UPDATE`, rotate, scope).Scan(&old.IdentityID, &old.Name, &old.Project, &old.Environment, &old.Permissions, &oldBinding)
		if err != nil {
			return Key{}, "", err
		}
		if old.IdentityID != p.ID || old.Name != in.Name || old.Project != in.Project || old.Environment != in.Environment || !slices.Equal(old.Permissions, in.Permissions) || oldBinding != binding {
			return Key{}, "", ErrConflict
		}
		if _, err = tx.Exec(ctx, "UPDATE api_keys SET expires_at=CASE WHEN expires_at IS NULL THEN now()+interval '15 minutes' ELSE LEAST(expires_at,now()+interval '15 minutes') END,never_expires=false WHERE id=$1", rotate); err != nil {
			return Key{}, "", err
		}
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM automation_key_scopes s JOIN api_keys k ON k.id=s.key_id WHERE s.scope_id=$1 AND k.revoked_at IS NULL AND (k.expires_at>now() OR k.never_expires)", scope).Scan(&count); err != nil {
		return Key{}, "", err
	}
	if count >= 50 {
		return Key{}, "", ErrBusy
	}
	id, raw, digest := makeKey()
	var expires *time.Time
	if !in.NeverExpires {
		value := in.ExpiresAt.UTC()
		expires = &value
	}
	k := Key{ID: id, IdentityID: p.ID, Name: in.Name, Prefix: "hp_" + id[:8], Project: in.Project, Environment: in.Environment, Permissions: in.Permissions, ExpiresAt: expires, NeverExpires: in.NeverExpires, CreatedAt: time.Now().UTC()}
	_, err = tx.Exec(ctx, `INSERT INTO api_keys(id,identity_id,name,digest,prefix,project,environment,permissions,expires_at,never_expires,mfa_verified) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, p.ID, in.Name, digest, k.Prefix, in.Project, in.Environment, in.Permissions, expires, in.NeverExpires, p.MFAVerified)
	if err != nil {
		return Key{}, "", err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO automation_key_scopes(key_id,scope_id,binding) VALUES($1,$2,$3)", id, scope, binding); err != nil {
		return Key{}, "", err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'automation-key.create',$3)", p.ID, p.KeyID, id); err != nil {
		return Key{}, "", err
	}
	return k, raw, tx.Commit(ctx)
}

func (s *Store) BoundKeys(ctx context.Context, scope string) ([]Key, error) {
	rows, err := s.Pool.Query(ctx, `SELECT k.id,k.identity_id,k.name,k.prefix,k.project,k.environment,k.application,k.permissions,k.expires_at,k.never_expires,k.revoked_at,k.last_used_at,k.created_at FROM api_keys k JOIN automation_key_scopes s ON s.key_id=k.id WHERE s.scope_id=$1 ORDER BY (k.revoked_at IS NULL AND (k.expires_at>now() OR k.never_expires)) DESC,k.created_at DESC LIMIT 100`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Key{}
	for rows.Next() {
		var k Key
		if err = rows.Scan(&k.ID, &k.IdentityID, &k.Name, &k.Prefix, &k.Project, &k.Environment, &k.Application, &k.Permissions, &k.ExpiresAt, &k.NeverExpires, &k.RevokedAt, &k.LastUsedAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) RevokeBoundKey(ctx context.Context, p Principal, scope, id string) error {
	if p.CredentialType != "browser" || !p.IsHuman() || p.MFARequired {
		return ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, "UPDATE api_keys SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1 AND id IN (SELECT key_id FROM automation_key_scopes WHERE scope_id=$2)", id, scope)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'automation-key.revoke',$3)", p.ID, p.KeyID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
