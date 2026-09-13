package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

const ShowcaseLock int64 = 724891018

type Showcase struct {
	Sample              bool             `json:"sample"`
	Name                string           `json:"name"`
	Project             string           `json:"project"`
	Environment         string           `json:"environment"`
	State               string           `json:"state"`
	Revision            int64            `json:"revision"`
	ApplicationID       string           `json:"application_id"`
	ApplicationRevision int64            `json:"application_revision"`
	DeploymentID        string           `json:"deployment_id"`
	DeploymentStatus    string           `json:"deployment_status"`
	Message             string           `json:"message"`
	Removable           bool             `json:"removable"`
	ID                  string           `json:"-"`
	GrantID             string           `json:"-"`
	Spec                spec.Application `json:"-"`
	RemoveRevision      int64            `json:"-"`
	Attempts            int              `json:"-"`
	Ready               bool             `json:"-"`
}

// ScheduleShowcase runs in the first-owner transaction. Existing installations,
// applications, and test stores without the explicit server setting are skipped.
func (s *Store) ScheduleShowcase(ctx context.Context, tx pgx.Tx, ownerID string) error {
	if !s.ShowcaseEnabled {
		return nil
	}
	var permitted bool
	if err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM applications) AND NOT EXISTS(SELECT 1 FROM showcase) AND EXISTS(SELECT 1 FROM identities WHERE id=$1 AND owner AND NOT disabled)`, ownerID).Scan(&permitted); err != nil || !permitted {
		return err
	}
	app, err := spec.Showcase()
	if err != nil {
		return err
	}
	p := Principal{ID: ownerID, Admin: true, Permissions: []string{"admin"}, IdentityPermissions: []string{"admin"}}
	grant, err := s.NewSourceGrant(ctx, tx, p, "demo", "development", "shop")
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE api_keys SET name='Sample shop deployment grant' WHERE id=$1", grant); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO showcase(id,identity_id,grant_id,spec) VALUES($1,$2,$3,$4)", NewID(), ownerID, grant, JSON(app))
	return err
}

func (s *Store) Showcase(ctx context.Context) (Showcase, error) {
	item := Showcase{Name: "shop", Project: "demo", Environment: "development", State: "absent", Message: "This installation has no bootstrap sample."}
	// Retry deadlines are written by PostgreSQL, so evaluate them on its clock.
	err := s.Pool.QueryRow(ctx, `SELECT x.id,x.grant_id,x.spec,x.state,x.revision,x.application_id,COALESCE(a.revision,0),x.deployment_id,COALESCE(d.status,''),x.message,x.remove_application_revision,x.attempts,x.next_attempt_at <= now() FROM showcase x LEFT JOIN applications a ON a.id=x.application_id LEFT JOIN deployments d ON d.id=x.deployment_id WHERE x.singleton`).Scan(&item.ID, &item.GrantID, &item.Spec, &item.State, &item.Revision, &item.ApplicationID, &item.ApplicationRevision, &item.DeploymentID, &item.DeploymentStatus, &item.Message, &item.RemoveRevision, &item.Attempts, &item.Ready)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, nil
	}
	item.Sample = err == nil
	item.Removable = item.Sample && item.State != "removed" && item.State != "skipped" && item.State != "removing"
	return item, err
}

// ErrShowcaseInactive lets a stale worker stop without changing a newer
// cancellation, accepted deployment, retry, or permanent removal marker.
var ErrShowcaseInactive = errors.New("the sample task changed or is no longer queued")

func (s *Store) AcceptShowcase(ctx context.Context, item Showcase) (Deployment, error) {
	p, err := s.KeyPrincipal(ctx, item.GrantID)
	if err != nil {
		return Deployment{}, err
	}
	return s.accept(ctx, p, "demo", "development", item.Spec, 0, "showcase-"+item.ID, nil, &item)
}

// Acceptance holds the same transaction lock as removal and commits its
// marker alongside the application and deployment. Losing this connection
// rolls all three back; there is no separately locked connection to fence.
func (s *Store) lockShowcaseAcceptance(ctx context.Context, tx pgx.Tx, p Principal, project, environment string, next spec.Application, item Showcase) error {
	if project != "demo" || environment != "development" || next.Name != "shop" {
		return ErrForbidden
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", ShowcaseLock); err != nil {
		return err
	}
	var state, grant string
	var revision int64
	var saved spec.Application
	err := tx.QueryRow(ctx, "SELECT state,grant_id,revision,spec FROM showcase WHERE id=$1 FOR UPDATE", item.ID).Scan(&state, &grant, &revision, &saved)
	if err != nil {
		return err
	}
	if state != "queued" || revision != item.Revision || grant != p.KeyID || !bytes.Equal(JSON(saved), JSON(next)) {
		return ErrShowcaseInactive
	}
	return nil
}
func (s *Store) recordShowcaseAcceptance(ctx context.Context, tx pgx.Tx, id, applicationID, deploymentID string) error {
	result, err := tx.Exec(ctx, "UPDATE showcase SET state='accepted',revision=revision+1,application_id=$2,deployment_id=$3,message='Sample shop accepted into the normal deployment queue.',attempts=0,updated_at=now() WHERE id=$1 AND state='queued'", id, applicationID, deploymentID)
	if err == nil && result.RowsAffected() != 1 {
		return ErrShowcaseInactive
	}
	return err
}

func (s *Store) RequestShowcaseRemoval(ctx context.Context, p Principal, expected int64, applicationID string, applicationRevision int64) error {
	if !p.IsAdmin() || p.CredentialType != "browser" {
		return ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Serialize with acceptance so cancelling an unstarted sample cannot race an
	// already-read seed into creating an untracked application.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", ShowcaseLock); err != nil {
		return err
	}
	var revision int64
	var appID, state, grant string
	if err = tx.QueryRow(ctx, "SELECT revision,application_id,state,grant_id FROM showcase WHERE singleton FOR UPDATE").Scan(&revision, &appID, &state, &grant); err != nil {
		return err
	}
	if expected != revision || appID != applicationID || state == "removed" || state == "skipped" || state == "removing" {
		return ErrConflict
	}
	var actual int64
	if appID != "" {
		err = tx.QueryRow(ctx, "SELECT revision FROM applications WHERE id=$1 FOR UPDATE", appID).Scan(&actual)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	if actual != applicationRevision {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, "UPDATE api_keys SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1", grant); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE deployments SET cancel_requested=true WHERE application_id=$1 AND status IN ('queued','running')", appID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE showcase SET state='removing',revision=revision+1,remove_application_revision=$1,message='Sample removal is queued; existing deployment work is being stopped.',attempts=0,next_attempt_at=now(),updated_at=now() WHERE singleton", applicationRevision); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'showcase.remove.request',$3,$4)", p.ID, p.KeyID, appID, JSON(map[string]any{"application_revision": applicationRevision})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DetachShowcase is called only while holding the same application advisory lock
// as the deployment worker. The task retains the namespace identity until owned
// Kubernetes cleanup succeeds, including across server restarts.
func (s *Store) DetachShowcase(ctx context.Context, item Showcase) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('demo:development:shop',2))"); err != nil {
		return err
	}
	var revision int64
	var project, environment, name string
	err = tx.QueryRow(ctx, "SELECT revision,project,environment,name FROM applications WHERE id=$1 FOR UPDATE", item.ApplicationID).Scan(&revision, &project, &environment, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if revision != item.RemoveRevision || project != "demo" || environment != "development" || name != "shop" {
		return fmt.Errorf("%w: sample changed after removal was reviewed", ErrConflict)
	}
	var attached bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM application_sources WHERE application_id=$1) OR EXISTS(SELECT 1 FROM source_jobs WHERE application_id=$1) OR EXISTS(SELECT 1 FROM build_configs WHERE application_id=$1 OR (project='demo' AND environment='development' AND name='shop'))", item.ApplicationID).Scan(&attached); err != nil {
		return err
	}
	if attached {
		return fmt.Errorf("%w: this sample now has source integrations; detach them before sample removal", ErrConflict)
	}
	if _, err = tx.Exec(ctx, "DELETE FROM deployment_events WHERE deployment_id IN (SELECT id FROM deployments WHERE application_id=$1)", item.ApplicationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM deployments WHERE application_id=$1", item.ApplicationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM applications WHERE id=$1", item.ApplicationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
