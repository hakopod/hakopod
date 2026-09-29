package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

type ActionsPool struct {
	ApplicationID   string       `json:"application_id"`
	Service         string       `json:"service"`
	Project         string       `json:"project"`
	Environment     string       `json:"environment"`
	ApplicationName string       `json:"application_name"`
	Revision        int64        `json:"revision"`
	Config          spec.Service `json:"config"`
	Removed         bool         `json:"removed"`
	Message         string       `json:"message"`
	UpdatedAt       time.Time    `json:"updated_at"`
}
type ActionsSlot struct {
	ManagerLaunchAttempted   bool         `json:"-"`
	ProviderCleanupRunnerID  string       `json:"-"`
	ProviderCleanupConfirmed bool         `json:"-"`
	ProviderRunnerID         string       `json:"provider_runner_id,omitempty"`
	EncryptedRegistration    []byte       `json:"-"`
	ID                       string       `json:"id"`
	ApplicationID            string       `json:"application_id"`
	Service                  string       `json:"service"`
	Config                   spec.Service `json:"-"`
	RunnerID                 int64        `json:"runner_id"`
	Phase                    string       `json:"phase"`
	CreatedAt                time.Time    `json:"created_at"`
	UpdatedAt                time.Time    `json:"updated_at"`
}

func (s *Store) RequireActions(ctx context.Context, project, env string) error {
	if s.ManagedCloud {
		if s.ActionsAccess == nil {
			return ErrLicenseRequired
		}
		return s.ActionsAccess(ctx, project, env)
	}
	return s.RequireFeatures(ctx, "managed_actions")
}

func (s *Store) ActionsPools(ctx context.Context, application string) ([]ActionsPool, error) {
	if application == "" {
		return s.ActionsPoolsPage(ctx, application, "", "")
	}
	// Current services must remain visible when failed cleanup accumulates more
	// retired pools than the bounded history window. Fleet keyset order is separate.
	rows, err := s.Pool.Query(ctx, `SELECT application_id,service,project,environment,application_name,revision,config,removed,message,updated_at FROM actions_pools WHERE application_id=$1 ORDER BY removed,service LIMIT 200`, application)
	if err != nil {
		return nil, err
	}
	return scanActionsPools(rows)
}

// ActionsPool rereads one leased service without depending on inventory pages.
// Historical removed pools may outnumber a single bounded page.
func (s *Store) ActionsPool(ctx context.Context, application, service string) (*ActionsPool, error) {
	p := &ActionsPool{}
	var data []byte
	err := s.Pool.QueryRow(ctx, `SELECT application_id,service,project,environment,application_name,revision,config,removed,message,updated_at FROM actions_pools WHERE application_id=$1 AND service=$2`, application, service).Scan(&p.ApplicationID, &p.Service, &p.Project, &p.Environment, &p.ApplicationName, &p.Revision, &data, &p.Removed, &p.Message, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &p.Config); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Store) ActionsPoolsPage(ctx context.Context, application, afterApplication, afterService string) ([]ActionsPool, error) {
	rows, err := s.Pool.Query(ctx, `SELECT application_id,service,project,environment,application_name,revision,config,removed,message,updated_at FROM actions_pools WHERE ($1='' OR application_id=$1) AND (application_id,service)>($2,$3) ORDER BY application_id,service LIMIT 200`, application, afterApplication, afterService)
	if err != nil {
		return nil, err
	}
	return scanActionsPools(rows)
}

func scanActionsPools(rows pgx.Rows) ([]ActionsPool, error) {
	defer rows.Close()
	out := []ActionsPool{}
	for rows.Next() {
		var p ActionsPool
		var data []byte
		if err := rows.Scan(&p.ApplicationID, &p.Service, &p.Project, &p.Environment, &p.ApplicationName, &p.Revision, &data, &p.Removed, &p.Message, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &p.Config); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if len(out) > 200 {
		return nil, fmt.Errorf("Managed Actions pool inventory exceeds its bound")
	}
	return out, rows.Err()
}
func (s *Store) ActionsSlots(ctx context.Context, app, service string) ([]ActionsSlot, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,application_id,service,config,runner_id,phase,created_at,updated_at,provider_runner_id,encrypted_registration,manager_launch_attempted,provider_cleanup_runner_id,provider_cleanup_confirmed FROM actions_slots WHERE application_id=$1 AND service=$2 ORDER BY created_at,id LIMIT 21`, app, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ActionsSlot{}
	for rows.Next() {
		var v ActionsSlot
		var data []byte
		if err = rows.Scan(&v.ID, &v.ApplicationID, &v.Service, &data, &v.RunnerID, &v.Phase, &v.CreatedAt, &v.UpdatedAt, &v.ProviderRunnerID, &v.EncryptedRegistration, &v.ManagerLaunchAttempted, &v.ProviderCleanupRunnerID, &v.ProviderCleanupConfirmed); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &v.Config); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if len(out) > 20 {
		return nil, fmt.Errorf("Managed Actions slot inventory exceeds its bound")
	}
	return out, rows.Err()
}

// Called while the deployment owns the application's runtime advisory lock.
func (s *Store) SyncActions(ctx context.Context, app Application, next spec.Application, revision int64) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE actions_pools SET removed=true,updated_at=now(),next_reconcile_at=now() WHERE application_id=$1`, app.ID); err != nil {
		return err
	}
	for name, svc := range next.Services {
		if svc.Actions == nil {
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO actions_pools(application_id,service,project,environment,application_name,revision,config) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(application_id,service) DO UPDATE SET revision=EXCLUDED.revision,config=EXCLUDED.config,removed=false,updated_at=now(),next_reconcile_at=now()`, app.ID, name, app.Project, app.Environment, app.Name, revision, JSON(svc)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *Store) NewActionsSlot(ctx context.Context, p ActionsPool) (ActionsSlot, error) {
	v := ActionsSlot{ID: NewID(), ApplicationID: p.ApplicationID, Service: p.Service, Config: p.Config, Phase: "intent"}
	if v.Config.Actions != nil && v.Config.Actions.Provider.Effective() == actions.ProviderGitHub {
		v.Config.Image = spec.ActionsRunnerImage
	}
	if v.Config.Actions != nil && v.Config.Actions.Provider.Effective() != actions.ProviderGitHub {
		// A durable pool hold and a new launch serialize through the pool row.
		// Cleanup remains eligible after a hold; replenishment does not.
		err := s.Pool.QueryRow(ctx, `WITH eligible_pool AS (
 SELECT 1 FROM actions_pools WHERE application_id=$2 AND service=$3 AND provider_hold_reason='' FOR UPDATE
)
 INSERT INTO actions_slots(id,application_id,service,config) SELECT $1,$2,$3,$4
 WHERE EXISTS(SELECT 1 FROM eligible_pool) AND (SELECT count(*) FROM actions_slots WHERE application_id=$2 AND service=$3)<10
 RETURNING created_at,updated_at`, v.ID, v.ApplicationID, v.Service, JSON(v.Config)).Scan(&v.CreatedAt, &v.UpdatedAt)
		return v, err
	}
	err := s.Pool.QueryRow(ctx, `INSERT INTO actions_slots(id,application_id,service,config) SELECT $1,$2,$3,$4 WHERE (SELECT count(*) FROM actions_slots WHERE application_id=$2 AND service=$3)<10 RETURNING created_at,updated_at`, v.ID, v.ApplicationID, v.Service, JSON(v.Config)).Scan(&v.CreatedAt, &v.UpdatedAt)
	return v, err
}
func (s *Store) UpdateActionsSlot(ctx context.Context, id string, runner int64, phase string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE actions_slots SET runner_id=$2,phase=$3,updated_at=now() WHERE id=$1`, id, runner, phase)
	return err
}

// Cached provider observations retain their original timestamp. Reading a
// snapshot again must never turn old GitHub data into a fresh health report.
func (s *Store) ObserveActionsSlot(ctx context.Context, id string, runner int64, phase string, observedAt time.Time) error {
	if observedAt.IsZero() {
		return s.UpdateActionsSlot(ctx, id, runner, phase)
	}
	_, err := s.Pool.Exec(ctx, `UPDATE actions_slots SET runner_id=$2,phase=$3,updated_at=$4 WHERE id=$1`, id, runner, phase, observedAt)
	return err
}
func (s *Store) DeleteActionsSlot(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM actions_slots WHERE id=$1`, id)
	return err
}
func (s *Store) ActionsMessage(ctx context.Context, p ActionsPool, message string) error {
	if len(message) > 512 {
		message = message[:512]
	}
	_, err := s.Pool.Exec(ctx, `UPDATE actions_pools SET message=$3,updated_at=now() WHERE application_id=$1 AND service=$2`, p.ApplicationID, p.Service, message)
	return err
}
func (s *Store) DeleteRetiredActionsPool(ctx context.Context, p ActionsPool) error {
	_, err := s.Pool.Exec(ctx, `WITH expired AS (
 DELETE FROM actions_jobs WHERE application_id=$1 AND service=$2 AND created_at<now()-interval '30 days'
)
 DELETE FROM actions_pools WHERE application_id=$1 AND service=$2 AND removed AND provider_hold_reason=''
 AND NOT EXISTS(SELECT 1 FROM actions_slots WHERE application_id=$1 AND service=$2)
 AND NOT EXISTS(SELECT 1 FROM actions_jobs WHERE application_id=$1 AND service=$2 AND created_at>=now()-interval '30 days')`, p.ApplicationID, p.Service)
	return err
}

// Failed releases still need registration cleanup. This lock shares the ordinary
// deployment/maintenance key but does not require a successful revision.
func (s *Store) ClaimActions(ctx context.Context, application string) (*RuntimeClaim, error) {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,3))`, application).Scan(&locked); err != nil {
		releaseClaimConnection(conn)
		return nil, err
	}
	if !locked {
		conn.Release()
		return nil, nil
	}
	var pending bool
	if err = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployments WHERE application_id=$1 AND status IN ('queued','running'))`, application).Scan(&pending); err != nil || pending {
		releaseClaimConnection(conn)
		return nil, err
	}
	return &RuntimeClaim{conn: conn, application: application, actions: true}, nil
}

func (s *Store) requireActionsTx(ctx context.Context, tx pgx.Tx, project, env string, next spec.Application) error {
	if !spec.HasActiveActions(next) {
		return nil
	}
	if s.ManagedCloud {
		return s.RequireActions(ctx, project, env)
	}
	return s.requireFeaturesTx(ctx, tx, "managed_actions")
}

// Keep credentials while desired pools, active slots or retained job history need
// them. Job history retains only its effective read credential, not extra access.
func (s *Store) ActionsCredentialRequired(ctx context.Context, project, environment, application, name string) (bool, error) {
	var required bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM actions_pools p WHERE p.project=$1 AND p.environment=$2 AND p.application_name=$3
 AND ((NOT p.removed AND $4 IN (p.config->'actions'->>'credential',p.config->'actions'->>'jobs_credential'))
 OR EXISTS(SELECT 1 FROM actions_slots v WHERE v.application_id=p.application_id AND v.service=p.service
 AND $4 IN (v.config->'actions'->>'credential',v.config->'actions'->>'jobs_credential')))
) OR EXISTS(
 SELECT 1 FROM applications a CROSS JOIN LATERAL jsonb_each(a.spec->'services') AS svc
 WHERE a.project=$1 AND a.environment=$2 AND a.name=$3
 AND $4 IN (svc.value->'actions'->>'credential',svc.value->'actions'->>'jobs_credential')
) OR EXISTS(
 SELECT 1 FROM actions_jobs j JOIN applications a ON a.id=j.application_id
 WHERE a.project=$1 AND a.environment=$2 AND a.name=$3 AND j.created_at>=now()-interval '30 days'
 AND (COALESCE(NULLIF(j.provider_config->>'jobs_credential',''),j.provider_config->>'credential')=$4
 OR (j.provider_runner_id<>'' AND j.native_job IS NOT NULL AND j.native_job->>'status'<>'completed' AND j.provider_config->>'credential'=$4))
)`, project, environment, application, name).Scan(&required)
	return required, err
}
