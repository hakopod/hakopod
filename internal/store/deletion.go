package store

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// DeleteEmptyApplication only removes metadata after a successful empty release.
// The worker's lock fences runtime maintenance. PVCs and their namespace remain
// available for separately confirmed durable reclamation.
func (s *Store) DeleteEmptyApplication(ctx context.Context, p Principal, id string, expected int64, confirmation string, deleteData ...bool) error {
	a, err := s.Application(ctx, id)
	if err != nil {
		return err
	}
	if !p.CanManageApplication(a.Project, a.Environment, a.Name) {
		return ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,2))", a.Project+":"+a.Environment+":"+a.Name); err != nil {
		return err
	}
	var locked bool
	if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended($1,3))", id).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("%w: application has active runtime work; wait and try again", ErrConflict)
	}
	a, err = scanApp(tx.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return err
	}
	if a.Name != confirmation || a.Revision != expected {
		return fmt.Errorf("%w: application changed; review it again and confirm its name", ErrConflict)
	}
	var preview bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM previews WHERE (application_id=$1 OR parent_id=$1) AND state<>'deleted')", a.ID).Scan(&preview); err != nil {
		return err
	}
	if preview {
		return fmt.Errorf("%w: delete previews through their expiry controls first", ErrConflict)
	}
	if len(a.Spec.Services) != 0 {
		return fmt.Errorf("%w: remove all services through a reviewed deployment before deleting this application", ErrConflict)
	}
	var actionsPending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM actions_pools WHERE application_id=$1 AND NOT removed)
 OR EXISTS(SELECT 1 FROM actions_slots WHERE application_id=$1)`, id).Scan(&actionsPending); err != nil {
		return err
	}
	if actionsPending {
		return fmt.Errorf("%w: wait for Managed Actions runner and registration cleanup", ErrConflict)
	}
	var ready bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployments WHERE application_id=$1 AND revision=$2 AND status='succeeded')
 AND NOT EXISTS(SELECT 1 FROM deployments WHERE application_id=$1 AND status IN ('queued','running'))`, id, expected).Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("%w: wait for the empty application revision to deploy successfully", ErrConflict)
	}
	// Manual build dispatch locks its configuration before creating a run.
	rows, err := tx.Query(ctx, `SELECT id FROM build_configs WHERE application_id=$1 OR (project=$2 AND environment=$3 AND name=$4) ORDER BY id FOR UPDATE`, id, a.Project, a.Environment, a.Name)
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var busy bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_jobs WHERE application_id=$1 AND status IN ('queued','running'))
 OR EXISTS(SELECT 1 FROM build_runs r JOIN build_configs c ON c.id=r.build_id WHERE (c.application_id=$1 OR (c.project=$2 AND c.environment=$3 AND c.name=$4)) AND (r.status NOT IN ('completed','failed','cancelled') OR r.auto_status IN ('queued','processing')))
 OR EXISTS(SELECT 1 FROM backup_jobs WHERE status IN ('queued','running') AND (source->>'application_id'=$1 OR target->>'application_id'=$1))
 OR EXISTS(SELECT 1 FROM deployment_volume_cleanup c JOIN deployments d ON d.id=c.deployment_id WHERE d.application_id=$1 AND NOT c.completed AND d.status IN ('queued','running','succeeded'))
 OR EXISTS(SELECT 1 FROM volume_resizes WHERE application_id=$1 AND phase NOT IN ('completed','cancelled'))
 OR EXISTS(SELECT 1 FROM backup_schedules WHERE enabled AND source->>'application_id'=$1)`, id, a.Project, a.Environment, a.Name).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("%w: stop active builds, source imports and backup schedules before deletion", ErrConflict)
	}
	// Retained job metadata does not represent pending provider cleanup. The
	// application and runtime locks protect the completed slot check above.
	if _, err = tx.Exec(ctx, `DELETE FROM actions_pools WHERE application_id=$1 AND removed
 AND NOT EXISTS(SELECT 1 FROM actions_slots WHERE application_id=$1)`, id); err != nil {
		return err
	}
	if err = deleteApplicationMetadata(ctx, tx, a); err != nil {
		return err
	}
	if len(deleteData) > 0 && deleteData[0] {
		if _, err = tx.Exec(ctx, "UPDATE retained_application_data SET status='deleting',requested_key_id=$2,requested_at=now() WHERE application_id=$1", id, p.KeyID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'application.delete',$3,$4)`, p.ID, p.KeyID, id, JSON(map[string]any{"project": a.Project, "environment": a.Environment, "name": a.Name, "revision": expected})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteEmptyProject(ctx context.Context, p Principal, name, confirmation string) error {
	if !p.IsAdmin() {
		return ErrForbidden
	}
	if name != confirmation {
		return fmt.Errorf("%w: confirm the project ID", ErrConflict)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,73))", name); err != nil {
		return err
	}
	var locked string
	if err = tx.QueryRow(ctx, "SELECT name FROM projects WHERE name=$1 FOR UPDATE", name).Scan(&locked); err != nil {
		return err
	}
	// Acceptance locks the environment before creating its first application.
	rows, err := tx.Query(ctx, "SELECT name FROM environments WHERE project=$1 ORDER BY name FOR UPDATE", name)
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if name == "demo" {
		var ownerExists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM identities WHERE owner AND NOT disabled)").Scan(&ownerExists); err != nil {
			return err
		}
		if !ownerExists {
			return fmt.Errorf("%w: complete first-time administrator setup before deleting the default project", ErrConflict)
		}
	}
	if err = emptyScope(ctx, tx, name, ""); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO retired_resource_names(kind,project,name,resource_id) VALUES('project',$1,$1,$1)`, name); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE api_keys SET revoked_at=COALESCE(revoked_at,now()) WHERE project=$1`, name); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM environments WHERE project=$1", name); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM projects WHERE name=$1", name); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'project.delete',$3)`, p.ID, p.KeyID, name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Retired names stay unavailable even after their parent metadata is deleted.
func (s *Store) RetiredName(ctx context.Context, kind, project, environment, name string) (bool, error) {
	var retired bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM retired_resource_names WHERE kind=$1 AND project=$2 AND environment=$3 AND name=$4)`, kind, project, environment, name).Scan(&retired)
	return retired, err
}

func deleteApplicationMetadata(ctx context.Context, tx pgx.Tx, a Application) error {
	if err := fenceSessionsForDeployment(ctx, tx, a.ID); err != nil {
		return err
	}
	id := a.ID
	var err error
	if _, err = tx.Exec(ctx, `INSERT INTO retired_resource_names(kind,project,environment,name,resource_id) VALUES('application',$1,$2,$3,$4)`, a.Project, a.Environment, a.Name, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO retained_application_data(application_id,project,environment,name) VALUES($1,$2,$3,$4) ON CONFLICT(application_id) DO NOTHING", id, a.Project, a.Environment, a.Name); err != nil {
		return err
	}
	// Revoke all resource grants before detaching callbacks and their history.
	if _, err = tx.Exec(ctx, `UPDATE api_keys SET revoked_at=COALESCE(revoked_at,now()) WHERE (project=$2 AND environment=$3 AND application=$4) OR id IN (SELECT grant_id FROM application_sources WHERE application_id=$1) OR id IN (SELECT grant_id FROM build_configs WHERE application_id=$1 OR (project=$2 AND environment=$3 AND name=$4))`, id, a.Project, a.Environment, a.Name); err != nil {
		return err
	}
	for _, query := range []string{
		`DELETE FROM source_jobs WHERE application_id=$1`,
		`DELETE FROM application_sources WHERE application_id=$1`,
		`DELETE FROM build_runs WHERE build_id IN (SELECT id FROM build_configs WHERE application_id=$1 OR (project=$2 AND environment=$3 AND name=$4))`,
		`DELETE FROM build_configs WHERE application_id=$1 OR (project=$2 AND environment=$3 AND name=$4)`,
		`DELETE FROM sandbox_sessions WHERE application_id=$1 AND status='closed' AND NOT cleanup_pending`,
		`DELETE FROM deployment_events WHERE deployment_id IN (SELECT id FROM deployments WHERE application_id=$1)`,
		`DELETE FROM deployments WHERE application_id=$1`,
		`DELETE FROM applications WHERE id=$1`,
	} {
		args := []any{id}
		if query == `DELETE FROM build_runs WHERE build_id IN (SELECT id FROM build_configs WHERE application_id=$1 OR (project=$2 AND environment=$3 AND name=$4))` || query == `DELETE FROM build_configs WHERE application_id=$1 OR (project=$2 AND environment=$3 AND name=$4)` {
			args = append(args, a.Project, a.Environment, a.Name)
		}
		if _, err = tx.Exec(ctx, query, args...); err != nil {
			return err
		}
	}

	return nil
}

// DeleteEmptyEnvironment removes scope metadata only. Resource cleanup must finish first.
func (s *Store) DeleteEmptyEnvironment(ctx context.Context, p Principal, project, environment, confirmation string) error {
	if !p.CanManageProject(project) || !p.Allows("deployments:write", project, environment, "") {
		return ErrForbidden
	}
	if environment != confirmation {
		return fmt.Errorf("%w: confirm the environment ID", ErrConflict)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var locked string
	// The parent lock serializes environment creation and project deletion.
	if err = tx.QueryRow(ctx, "SELECT name FROM projects WHERE name=$1 FOR UPDATE", project).Scan(&locked); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, "SELECT name FROM environments WHERE project=$1 AND name=$2 FOR UPDATE", project, environment).Scan(&locked); err != nil {
		return err
	}
	if project == "demo" && environment == "development" {
		var protected bool
		if err = tx.QueryRow(ctx, "SELECT NOT EXISTS(SELECT 1 FROM identities WHERE owner AND NOT disabled)").Scan(&protected); err != nil {
			return err
		}
		if protected {
			return fmt.Errorf("%w: complete first-time administrator setup before deleting the default development environment", ErrConflict)
		}
	}
	var personalDefault bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM personal_workspaces WHERE project=$1) AND $2='development'", project, environment).Scan(&personalDefault); err != nil {
		return err
	}
	if personalDefault {
		return fmt.Errorf("%w: the personal workspace development environment cannot be deleted", ErrConflict)
	}
	if err = emptyScope(ctx, tx, project, environment); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO retired_resource_names(kind,project,environment,name,resource_id) VALUES('environment',$1,$2,$2,$1||'/'||$2)`, project, environment); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE api_keys SET revoked_at=COALESCE(revoked_at,now()) WHERE project=$1 AND environment=$2`, project, environment); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM environments WHERE project=$1 AND name=$2", project, environment); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'environment.delete',$3,$4)`, p.ID, p.KeyID, project+"/"+environment, JSON(map[string]string{"project": project, "environment": environment})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func emptyScope(ctx context.Context, tx pgx.Tx, project, environment string) error {
	var personal bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM personal_workspaces WHERE project=$1)", project).Scan(&personal); err != nil {
		return err
	}
	if personal && environment == "" {
		return fmt.Errorf("%w: personal workspace scopes cannot be deleted", ErrConflict)
	}
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM applications WHERE project=$1 AND ($2='' OR environment=$2))
 OR EXISTS(SELECT 1 FROM build_configs WHERE project=$1 AND ($2='' OR environment=$2))
 OR EXISTS(SELECT 1 FROM runtime_resources WHERE project=$1 AND ($2='' OR environment=$2))
 OR EXISTS(SELECT 1 FROM retained_application_data WHERE project=$1 AND ($2='' OR environment=$2) AND status<>'deleted')
 OR EXISTS(SELECT 1 FROM secret_provider_scopes WHERE project=$1 AND ($2='' OR cardinality(environments)=0 OR $2=ANY(environments)))
 OR EXISTS(SELECT 1 FROM managed_databases WHERE project=$1 AND ($2='' OR environment=$2) AND deleted_at IS NULL)
 OR EXISTS(SELECT 1 FROM external_databases WHERE project=$1 AND ($2='' OR environment=$2) AND deleted_at IS NULL)
 OR EXISTS(SELECT 1 FROM managed_platforms WHERE project=$1 AND ($2='' OR environment=$2) AND deleted_at IS NULL)
 OR EXISTS(SELECT 1 FROM managed_capacity_scopes WHERE project=$1 AND ($2='' OR environment=$2))
 OR EXISTS(SELECT 1 FROM managed_platform_reviews WHERE project=$1 AND ($2='' OR environment=$2) AND consumed_at IS NULL AND expires_at>now())
 OR EXISTS(SELECT 1 FROM managed_platform_recovery_reviews WHERE project=$1 AND ($2='' OR environment=$2) AND consumed_at IS NULL AND expires_at>now())
 OR EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE project=$1 AND ($2='' OR environment=$2) AND (status IN ('queued','running') OR cleanup_required))
 OR EXISTS(SELECT 1 FROM managed_database_operations o JOIN managed_databases d ON d.id=o.database_id WHERE d.project=$1 AND ($2='' OR d.environment=$2) AND o.status IN ('queued','running'))
 OR EXISTS(SELECT 1 FROM external_database_operations o JOIN external_databases d ON d.id=o.database_id WHERE d.project=$1 AND ($2='' OR d.environment=$2) AND o.status IN ('queued','running'))
 OR EXISTS(SELECT 1 FROM managed_platform_operations o JOIN managed_platforms p ON p.id=o.platform_id WHERE p.project=$1 AND ($2='' OR p.environment=$2) AND o.status IN ('queued','running'))
 OR EXISTS(SELECT 1 FROM managed_platform_maintenance m JOIN managed_platforms p ON p.id=m.platform_id WHERE p.project=$1 AND ($2='' OR p.environment=$2) AND m.status='running')
 OR EXISTS(SELECT 1 FROM managed_platform_runtime_mutations m JOIN managed_platforms p ON p.id=m.platform_id WHERE p.project=$1 AND ($2='' OR p.environment=$2) AND m.completed_at IS NULL)`, project, environment).Scan(&used); err != nil {
		return err
	}
	if used {
		return fmt.Errorf("%w: remove resources, reclaim retained data, remove scope grants and finish pending reviews or operations before deleting this scope", ErrConflict)
	}
	return nil
}
