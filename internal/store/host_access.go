package store

import (
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/util/validation"
	"time"
)

type HostPermission struct {
	Node       string `json:"node"`
	Permission string `json:"permission"`
}
type HostGrant struct {
	IdentityID string    `json:"identity_id"`
	Node       string    `json:"node"`
	Permission string    `json:"permission"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func (p Principal) IsSuperAdmin() bool {
	return p.Owner && p.IsAdmin() && p.IsHuman() && p.CredentialType == "browser"
}
func (p Principal) CanHostTerminal(node string) bool {
	if !p.IsHuman() || p.CredentialType != "browser" || p.Project != "" || p.Environment != "" || p.Application != "" || p.IdentityProject != "" || p.IdentityEnvironment != "" {
		return false
	}
	if p.IsSuperAdmin() {
		return true
	}
	for _, grant := range p.HostPermissions {
		if grant.Permission == "nodes:terminal" && (grant.Node == node || grant.Node == "*") {
			return true
		}
	}
	return false
}
func (s *Store) hostPermissions(ctx context.Context, id string) ([]HostPermission, error) {
	rows, err := s.Pool.Query(ctx, "SELECT node,permission FROM host_access WHERE identity_id=$1 AND expires_at>now() ORDER BY node LIMIT 100", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HostPermission{}
	for rows.Next() {
		var v HostPermission
		if err = rows.Scan(&v.Node, &v.Permission); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) HostGrants(ctx context.Context, p Principal) ([]HostGrant, error) {
	if !p.IsHuman() || p.CredentialType != "browser" {
		return nil, ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, "SELECT identity_id,node,permission,expires_at FROM host_access WHERE expires_at>now() AND ($1 OR identity_id=$2) ORDER BY identity_id,node LIMIT 500", p.IsSuperAdmin(), p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HostGrant{}
	for rows.Next() {
		var v HostGrant
		if err = rows.Scan(&v.IdentityID, &v.Node, &v.Permission, &v.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func validHostNode(node string) bool {
	return node == "*" || (node != "" && len(validation.IsDNS1123Subdomain(node)) == 0)
}
func (s *Store) SetHostGrant(ctx context.Context, p Principal, g HostGrant) (HostGrant, error) {
	if !p.IsSuperAdmin() {
		return HostGrant{}, ErrForbidden
	}
	if !validHostNode(g.Node) || g.Permission != "nodes:terminal" || !g.ExpiresAt.After(time.Now().Add(time.Minute)) || g.ExpiresAt.After(time.Now().Add(30*24*time.Hour)) {
		return HostGrant{}, fmt.Errorf("%w: select a node and an expiry within 30 days", ErrInput)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return g, err
	}
	defer tx.Rollback(ctx)
	var allowed, eligible bool
	err = tx.QueryRow(ctx, "SELECT owner AND admin AND NOT disabled FROM identities WHERE id=$1 FOR SHARE", p.ID).Scan(&allowed)
	if err != nil {
		return g, err
	}
	if !allowed {
		return g, ErrForbidden
	}
	err = tx.QueryRow(ctx, "SELECT email IS NOT NULL AND NOT disabled AND NOT owner FROM identities WHERE id=$1 FOR SHARE", g.IdentityID).Scan(&eligible)
	if err != nil {
		return g, err
	}
	if !eligible {
		return g, ErrForbidden
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044219)"); err != nil {
		return g, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM host_access WHERE identity_id=$1 AND expires_at>now() AND node<>$2", g.IdentityID, g.Node).Scan(&count); err != nil {
		return g, err
	}
	if count >= 100 {
		return g, fmt.Errorf("%w: remove an existing host grant first", ErrInput)
	}
	_, err = tx.Exec(ctx, "INSERT INTO host_access(identity_id,node,permission,expires_at,granted_by) VALUES($1,$2,$3,$4,$5) ON CONFLICT(identity_id,node,permission) DO UPDATE SET expires_at=EXCLUDED.expires_at,granted_by=EXCLUDED.granted_by,created_at=now()", g.IdentityID, g.Node, g.Permission, g.ExpiresAt, p.ID)
	if err != nil {
		return g, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'host_access.grant',$3,$4)", p.ID, p.KeyID, g.IdentityID, JSON(g))
	if err != nil {
		return g, err
	}
	return g, tx.Commit(ctx)
}
func (s *Store) RevokeHostGrant(ctx context.Context, p Principal, user, node string) error {
	if !p.IsSuperAdmin() {
		return ErrForbidden
	}
	if !validHostNode(node) {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "DELETE FROM host_access WHERE identity_id=$1 AND node=$2", user, node)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'host_access.revoke',$3,$4)", p.ID, p.KeyID, user, JSON(map[string]string{"node": node}))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
