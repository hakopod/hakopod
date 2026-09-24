package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

type CustomRole struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	Revision    int64    `json:"revision"`
}

// Custom roles grant only existing project capabilities, never installation or
// membership administration. A missing or expired role grants no authority.
func validRolePermissions(permissions []string) bool {
	if len(permissions) == 0 || len(permissions) > 3 {
		return false
	}
	seen := map[string]bool{}
	for _, permission := range permissions {
		if seen[permission] || !contains([]string{"deployments:read", "deployments:write", "logs:read"}, permission) {
			return false
		}
		seen[permission] = true
	}
	return !seen["deployments:write"] || seen["deployments:read"]
}

func (r ProjectRole) permissions() []string {
	if strings.HasPrefix(r.Role, "custom:") {
		return r.Permissions
	}
	return rolePermissions(r.Role)
}

func (s *Store) CustomRoles(ctx context.Context, p Principal) ([]CustomRole, error) {
	if p.CredentialType != "browser" {
		return nil, ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, "SELECT id,name,permissions,revision FROM custom_roles ORDER BY lower(name),id LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CustomRole{}
	for rows.Next() {
		var role CustomRole
		if err = rows.Scan(&role.ID, &role.Name, &role.Permissions, &role.Revision); err != nil {
			return nil, err
		}
		out = append(out, role)
	}
	return out, rows.Err()
}

func (s *Store) SaveCustomRole(ctx context.Context, p Principal, id, name string, permissions []string, revision int64) (CustomRole, error) {
	if p.CredentialType != "browser" || !p.IsAdmin() {
		return CustomRole{}, ErrForbidden
	}
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 80 || !validRolePermissions(permissions) || revision < 0 {
		return CustomRole{}, ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return CustomRole{}, err
	}
	defer tx.Rollback(ctx)
	if err = s.requireFeaturesTx(ctx, tx, "custom_roles"); err != nil {
		return CustomRole{}, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044240)"); err != nil {
		return CustomRole{}, err
	}
	var duplicate bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM custom_roles WHERE lower(name)=lower($1) AND id<>$2)", name, id).Scan(&duplicate); err != nil {
		return CustomRole{}, err
	}
	if duplicate {
		return CustomRole{}, fmt.Errorf("%w: role name is already in use", ErrConflict)
	}
	permissions = slices.Clone(permissions)
	slices.Sort(permissions)
	if id == "" {
		if revision != 0 {
			return CustomRole{}, ErrConflict
		}
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM custom_roles").Scan(&count); err != nil {
			return CustomRole{}, err
		}
		if count >= 100 {
			return CustomRole{}, ErrBusy
		}
		id = "custom:" + NewID()
		_, err = tx.Exec(ctx, "INSERT INTO custom_roles(id,name,permissions) VALUES($1,$2,$3)", id, name, permissions)
	} else {
		var current int64
		if err = tx.QueryRow(ctx, "SELECT revision FROM custom_roles WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
			return CustomRole{}, err
		}
		if current != revision {
			return CustomRole{}, ErrConflict
		}
		_, err = tx.Exec(ctx, "UPDATE custom_roles SET name=$2,permissions=$3,revision=revision+1 WHERE id=$1", id, name, permissions)
	}
	if err != nil {
		return CustomRole{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'custom_role.save',$3,$4)", p.ID, p.KeyID, id, JSON(map[string]any{"name": name, "permissions": permissions})); err != nil {
		return CustomRole{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CustomRole{}, err
	}
	return CustomRole{ID: id, Name: name, Permissions: permissions, Revision: revision + 1}, nil
}

func (s *Store) DeleteCustomRole(ctx context.Context, p Principal, id string, revision int64) error {
	if p.CredentialType != "browser" || !p.IsAdmin() {
		return ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current int64
	if err = tx.QueryRow(ctx, "SELECT revision FROM custom_roles WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
		return err
	}
	if current != revision {
		return ErrConflict
	}
	// Removing an assignment is always possible, including after license expiry.
	for _, query := range []string{"DELETE FROM project_members WHERE role=$1", "DELETE FROM project_teams WHERE role=$1", "DELETE FROM invites WHERE role=$1 AND accepted_at IS NULL", "DELETE FROM custom_roles WHERE id=$1"} {
		if _, err = tx.Exec(ctx, query, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'custom_role.delete',$3)", p.ID, p.KeyID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) checkProjectRoleTx(ctx context.Context, tx pgx.Tx, role string) error {
	if role == "" || contains([]string{"admin", "developer", "viewer"}, role) {
		return nil
	}
	if !strings.HasPrefix(role, "custom:") {
		return ErrInput
	}
	if err := s.requireFeaturesTx(ctx, tx, "custom_roles"); err != nil {
		return err
	}
	var id string
	err := tx.QueryRow(ctx, "SELECT id FROM custom_roles WHERE id=$1 FOR SHARE", role).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInput
	}
	return err
}
