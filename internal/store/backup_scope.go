package store

import (
	"context"
	"github.com/hakopod/hakopod/internal/backup"
	"github.com/jackc/pgx/v5"
)

type backupPrincipalKey struct{}

// WithBackupPrincipal restricts repository reads at the authenticated API boundary.
// Worker calls use their own durable authority, never an HTTP context.
func WithBackupPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, backupPrincipalKey{}, p)
}
func backupUnscoped(ctx context.Context) bool {
	p, ok := ctx.Value(backupPrincipalKey{}).(Principal)
	return !ok || p.IsAdmin()
}
func backupProject(ctx context.Context) string {
	p, _ := ctx.Value(backupPrincipalKey{}).(Principal)
	return p.Project
}
func backupEnvironment(ctx context.Context) string {
	p, _ := ctx.Value(backupPrincipalKey{}).(Principal)
	return p.Environment
}
func (p Principal) CanManageBackups() bool {
	return p.IsAdmin() || (p.Project != "" && p.Environment != "" && p.Application == "" && p.Allows("deployments:write", p.Project, p.Environment, ""))
}
func backupAuthority(p Principal) *backup.Authority {
	if p.IsAdmin() {
		return nil
	}
	return &backup.Authority{Project: p.Project, Environment: p.Environment}
}

type backupQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func authorizeBackupSource(ctx context.Context, q backupQuerier, p Principal, source backup.Source) error {
	if !p.CanManageBackups() {
		return ErrForbidden
	}
	if p.IsAdmin() {
		return nil
	}
	if source.Kind != "database" {
		return backup.ErrNotFound
	}
	var allowed bool
	err := q.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM applications WHERE id=$1 AND project=$2 AND environment=$3)", source.ApplicationID, p.Project, p.Environment).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return backup.ErrNotFound
	}
	return nil
}
func (s *Store) AuthorizeBackupSource(ctx context.Context, p Principal, source backup.Source) error {
	return authorizeBackupSource(ctx, s.Pool, p, source)
}
func authorizeBackupJob(ctx context.Context, q backupQuerier, p Principal, j backup.Job) error {
	if j.Kind == "restore" && !p.IsAdmin() {
		if !p.CanManageBackups() || j.Source.Kind != "database" || j.Target == nil {
			return ErrForbidden
		}
		// An immutable artifact retains its workspace even if the original
		// application was deleted. Recovery must not require that source to live.
		var valid bool
		if err := q.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM backup_artifacts WHERE id=$1 AND destination_id=$2 AND source=$3::jsonb AND deleted_at IS NULL AND NOT deletion_pending)", j.ArtifactID, j.DestinationID, JSON(j.Source)).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return backup.ErrNotFound
		}
	} else if err := authorizeBackupSource(ctx, q, p, j.Source); err != nil {
		return err
	}
	if j.Target != nil {
		if err := authorizeBackupSource(ctx, q, p, j.Target.Source); err != nil {
			return err
		}
	}
	if p.IsAdmin() {
		return nil
	}
	var allowed bool
	err := q.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM backup_destinations WHERE id=$1 AND config->>'project'=$2 AND config->>'environment'=$3)", j.DestinationID, p.Project, p.Environment).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return backup.ErrNotFound
	}
	return nil
}
func (s *Store) authorizeBackupSchedule(ctx context.Context, p Principal, id string) error {
	if !p.CanManageBackups() {
		return ErrForbidden
	}
	if p.IsAdmin() {
		return nil
	}
	var allowed bool
	err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM backup_schedules WHERE id=$1 AND authority->>'project'=$2 AND authority->>'environment'=$3)", id, p.Project, p.Environment).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return backup.ErrNotFound
	}
	return nil
}
func (s *Store) backupWorkerPrincipal(ctx context.Context, id, key string, a *backup.Authority) (Principal, error) {
	if a == nil || a.Project == "" || a.Environment == "" {
		return Principal{}, ErrForbidden
	}
	p := Principal{ID: id, Project: a.Project, Environment: a.Environment, Permissions: []string{"deployments:write"}}
	if key != "" {
		fresh, err := s.KeyPrincipal(ctx, key)
		if err != nil {
			return p, err
		}
		if fresh.ID != id || !fresh.Allows("deployments:write", a.Project, a.Environment, "") {
			return p, ErrForbidden
		}
		p = fresh
		p.Project = a.Project
		p.Environment = a.Environment
	} else {
		if err := s.Pool.QueryRow(ctx, "SELECT COALESCE(email,''),permissions,project,environment FROM identities WHERE id=$1 AND NOT disabled", id).Scan(&p.Email, &p.IdentityPermissions, &p.IdentityProject, &p.IdentityEnvironment); err != nil {
			return p, err
		}
	}
	// Installation operators must retain explicit membership in this project.
	p.Admin = false
	p.Owner = false
	roles, err := s.projectRoles(ctx, id)
	if err != nil {
		return p, err
	}
	p.ProjectRoles = roles
	if !p.CanManageBackups() {
		return p, ErrForbidden
	}
	if s.AuthorizeBackup != nil {
		if err = s.AuthorizeBackup(ctx, id, a.Project, a.Environment); err != nil {
			return p, err
		}
	}
	return p, nil
}
func (s *Store) authorizeWorkerBackup(ctx context.Context, j backup.Job) error {
	p, err := s.backupWorkerPrincipal(ctx, j.IdentityID, j.KeyID, j.Authority)
	if err != nil {
		return err
	}
	return authorizeBackupJob(ctx, s.Pool, p, j)
}

// BackupManagementAllowed excludes installation recovery from scoped catalogs.
func BackupManagementAllowed(ctx context.Context) bool { return backupUnscoped(ctx) }
