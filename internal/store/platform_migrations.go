package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// PlatformMigration is one Railway or Vercel project being moved into a
// Hakopod application. The sealed token and variables never leave the store
// through this type; PlatformMigrationSecrets reads them for the worker.
type PlatformMigration struct {
	ID                  string          `json:"id"`
	IdentityID          string          `json:"-"`
	Project             string          `json:"project"`
	Environment         string          `json:"environment"`
	ApplicationName     string          `json:"application_name"`
	Provider            string          `json:"provider"`
	SourceProjectID     string          `json:"source_project_id"`
	SourceProjectName   string          `json:"source_project_name"`
	SourceEnvironmentID string          `json:"source_environment_id,omitempty"`
	SourceTeamID        string          `json:"source_team_id,omitempty"`
	Proposal            json.RawMessage `json:"proposal"`
	Status              string          `json:"status"`
	Steps               json.RawMessage `json:"steps"`
	Error               string          `json:"error,omitempty"`
	ApplicationID       string          `json:"application_id,omitempty"`
	CancelRequested     bool            `json:"cancel_requested"`
	HasToken            bool            `json:"has_token"`
	CreatedAt           time.Time       `json:"created_at"`
	StartedAt           *time.Time      `json:"started_at,omitempty"`
	FinishedAt          *time.Time      `json:"finished_at,omitempty"`
	ExpiresAt           time.Time       `json:"expires_at"`
	Lease               string          `json:"-"`
}

// A migration keeps its sealed token for at most this long.
const PlatformMigrationTokenTTL = 24 * time.Hour

const platformMigrationCols = "id,identity_id,project,environment,application_name,provider,source_project_id,source_project_name,source_environment_id,source_team_id,proposal,status,steps,error,application_id,cancel_requested,token_sealed IS NOT NULL,created_at,started_at,finished_at,expires_at,lease"

func scanPlatformMigration(row scanner) (PlatformMigration, error) {
	var m PlatformMigration
	err := row.Scan(&m.ID, &m.IdentityID, &m.Project, &m.Environment, &m.ApplicationName, &m.Provider,
		&m.SourceProjectID, &m.SourceProjectName, &m.SourceEnvironmentID, &m.SourceTeamID, &m.Proposal,
		&m.Status, &m.Steps, &m.Error, &m.ApplicationID, &m.CancelRequested, &m.HasToken,
		&m.CreatedAt, &m.StartedAt, &m.FinishedAt, &m.ExpiresAt, &m.Lease)
	return m, err
}

// A migration acts in the background as the identity that planned it, through
// an integration key that shares the migration's ID (see KeyPrincipal). The
// key carries only deployment permissions, in the migration's project and
// environment, narrowed to the planner's own application scope, and is
// revoked when the migration ends. It lives long enough for a migration that
// starts near the end of its 24 hours and then copies large databases.
const platformMigrationGrantTTL = 7 * 24 * time.Hour

const revokePlatformMigrationGrant = "UPDATE api_keys SET revoked_at=now() WHERE id=$1 AND kind='integration' AND revoked_at IS NULL"

// CreatePlatformMigration saves a planned migration with its sealed source
// token, and its grant. application narrows the grant like the planner's key.
func (s *Store) CreatePlatformMigration(ctx context.Context, m PlatformMigration, tokenSealed []byte, application ...string) (PlatformMigration, error) {
	if m.ID == "" || m.IdentityID == "" || len(tokenSealed) == 0 || len(m.Proposal) == 0 {
		return PlatformMigration{}, ErrInput
	}
	digest := make([]byte, 32)
	if _, err := rand.Read(digest); err != nil {
		return PlatformMigration{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return PlatformMigration{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "INSERT INTO api_keys(id,identity_id,name,digest,prefix,project,environment,application,permissions,expires_at,kind) VALUES($1,$2,'Platform migration grant',$3,'internal',$4,$5,$6,ARRAY['deployments:read','deployments:write'],$7,'integration')",
		m.ID, m.IdentityID, digest, m.Project, m.Environment, append(application, "")[0], time.Now().Add(platformMigrationGrantTTL)); err != nil {
		return PlatformMigration{}, err
	}
	m, err = scanPlatformMigration(tx.QueryRow(ctx, "INSERT INTO platform_migrations(id,identity_id,project,environment,application_name,provider,source_project_id,source_project_name,source_environment_id,source_team_id,token_sealed,proposal,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING "+platformMigrationCols,
		m.ID, m.IdentityID, m.Project, m.Environment, m.ApplicationName, m.Provider, m.SourceProjectID, m.SourceProjectName,
		m.SourceEnvironmentID, m.SourceTeamID, tokenSealed, m.Proposal, time.Now().Add(PlatformMigrationTokenTTL)))
	if err != nil {
		return m, err
	}
	return m, tx.Commit(ctx)
}

func (s *Store) PlatformMigration(ctx context.Context, id string) (PlatformMigration, error) {
	return scanPlatformMigration(s.Pool.QueryRow(ctx, "SELECT "+platformMigrationCols+" FROM platform_migrations WHERE id=$1", id))
}

// PlatformMigrations lists the migrations of one project environment, newest first.
func (s *Store) PlatformMigrations(ctx context.Context, project, environment string) ([]PlatformMigration, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+platformMigrationCols+" FROM platform_migrations WHERE project=$1 AND environment=$2 ORDER BY created_at DESC LIMIT 50", project, environment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlatformMigration
	for rows.Next() {
		m, err := scanPlatformMigration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PlatformMigrationSecrets returns the sealed token and variables for the
// worker. Both are nil once erased.
func (s *Store) PlatformMigrationSecrets(ctx context.Context, id string) (token, variables []byte, err error) {
	err = s.Pool.QueryRow(ctx, "SELECT token_sealed,variables_sealed FROM platform_migrations WHERE id=$1", id).Scan(&token, &variables)
	return token, variables, err
}

// UpdatePlatformMigrationPlan replaces the proposal and the sealed values the
// user supplied, while the migration is still only planned.
func (s *Store) UpdatePlatformMigrationPlan(ctx context.Context, id string, proposal json.RawMessage, variablesSealed []byte) (PlatformMigration, error) {
	m, err := scanPlatformMigration(s.Pool.QueryRow(ctx, "UPDATE platform_migrations SET proposal=$2,variables_sealed=$3 WHERE id=$1 AND status='planned' AND token_sealed IS NOT NULL RETURNING "+platformMigrationCols, id, proposal, variablesSealed))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrConflict
	}
	return m, err
}

// QueuePlatformMigration starts a planned migration. Repeating the call with
// the same idempotency key returns the same migration.
func (s *Store) QueuePlatformMigration(ctx context.Context, id, idempotencyKey string) (PlatformMigration, error) {
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return PlatformMigration{}, ErrInput
	}
	m, err := scanPlatformMigration(s.Pool.QueryRow(ctx, "UPDATE platform_migrations SET status='queued',idempotency_key=$2 WHERE id=$1 AND status='planned' AND token_sealed IS NOT NULL AND expires_at>now() RETURNING "+platformMigrationCols, id, idempotencyKey))
	if !errors.Is(err, pgx.ErrNoRows) {
		return m, err
	}
	var key string
	if err = s.Pool.QueryRow(ctx, "SELECT idempotency_key FROM platform_migrations WHERE id=$1", id).Scan(&key); err != nil {
		return PlatformMigration{}, err
	}
	if key != idempotencyKey {
		return PlatformMigration{}, ErrConflict
	}
	return s.PlatformMigration(ctx, id)
}

// ClaimPlatformMigration leases the oldest queued migration, or one whose
// worker stopped heartbeating. A migration is resumable: each step records
// what it created, so a new worker continues instead of starting over.
func (s *Store) ClaimPlatformMigration(ctx context.Context, lease string) (PlatformMigration, error) {
	return scanPlatformMigration(s.Pool.QueryRow(ctx, "UPDATE platform_migrations SET status='running',lease=$1,lease_until=now()+interval '60 seconds',started_at=COALESCE(started_at,now()) WHERE id=(SELECT id FROM platform_migrations WHERE status='queued' OR (status='running' AND lease_until<now()) ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING "+platformMigrationCols, lease))
}

// HeartbeatPlatformMigration extends the lease. It reports false when the lease
// was lost or the user asked to cancel, and the worker should stop.
func (s *Store) HeartbeatPlatformMigration(ctx context.Context, id, lease string) (bool, error) {
	var cancel bool
	err := s.Pool.QueryRow(ctx, "UPDATE platform_migrations SET lease_until=now()+interval '60 seconds' WHERE id=$1 AND lease=$2 AND status='running' RETURNING cancel_requested", id, lease).Scan(&cancel)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil && !cancel, err
}

// RecordPlatformMigrationSteps saves progress and, once known, the application.
func (s *Store) RecordPlatformMigrationSteps(ctx context.Context, id, lease string, steps json.RawMessage, applicationID string) error {
	tag, err := s.Pool.Exec(ctx, "UPDATE platform_migrations SET steps=$3,application_id=CASE WHEN $4='' THEN application_id ELSE $4 END WHERE id=$1 AND lease=$2 AND status='running'", id, lease, steps, applicationID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return err
}

// FinishPlatformMigration records the outcome and erases the sealed token and
// variables in the same statement.
func (s *Store) FinishPlatformMigration(ctx context.Context, id, lease, status, message string) error {
	if status != "succeeded" && status != "failed" && status != "cancelled" {
		return ErrInput
	}
	if len(message) > 2048 {
		message = message[:2048]
	}
	tag, err := s.Pool.Exec(ctx, "UPDATE platform_migrations SET status=$3,error=$4,finished_at=now(),lease='',lease_until=NULL,token_sealed=NULL,variables_sealed=NULL WHERE id=$1 AND lease=$2 AND status='running'", id, lease, status, message)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrConflict
	}
	if err == nil {
		_, err = s.Pool.Exec(ctx, revokePlatformMigrationGrant, id)
	}
	return err
}

// CancelPlatformMigration cancels a migration that has not started, erasing its
// secrets, or asks a running one to stop at its next step.
func (s *Store) CancelPlatformMigration(ctx context.Context, id string) (PlatformMigration, error) {
	m, err := scanPlatformMigration(s.Pool.QueryRow(ctx, "UPDATE platform_migrations SET status='cancelled',finished_at=now(),token_sealed=NULL,variables_sealed=NULL WHERE id=$1 AND status IN ('planned','queued') RETURNING "+platformMigrationCols, id))
	if err == nil {
		_, err = s.Pool.Exec(ctx, revokePlatformMigrationGrant, id)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return m, err
	}
	m, err = scanPlatformMigration(s.Pool.QueryRow(ctx, "UPDATE platform_migrations SET cancel_requested=true WHERE id=$1 AND status='running' RETURNING "+platformMigrationCols, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrConflict
	}
	return m, err
}

// ExpirePlatformMigrations erases sealed tokens and variables past their
// expiry, and fails migrations that were planned but never started in time.
func (s *Store) ExpirePlatformMigrations(ctx context.Context) error {
	if _, err := s.Pool.Exec(ctx, "UPDATE platform_migrations SET status='failed',error='The migration was not started within 24 hours. Start a new one.',finished_at=now(),token_sealed=NULL,variables_sealed=NULL WHERE status='planned' AND expires_at<now()"); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE platform_migrations SET token_sealed=NULL,variables_sealed=NULL WHERE expires_at<now() AND status NOT IN ('queued','running') AND (token_sealed IS NOT NULL OR variables_sealed IS NOT NULL)"); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE kind='integration' AND revoked_at IS NULL AND id IN (SELECT id FROM platform_migrations WHERE status IN ('succeeded','failed','cancelled'))")
	return err
}
