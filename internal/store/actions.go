package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
	ID            string       `json:"id"`
	ApplicationID string       `json:"application_id"`
	Service       string       `json:"service"`
	Config        spec.Service `json:"-"`
	RunnerID      int64        `json:"runner_id"`
	Phase         string       `json:"phase"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
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
	return s.ActionsPoolsPage(ctx, application, "", "")
}

func (s *Store) ActionsPoolsPage(ctx context.Context, application, afterApplication, afterService string) ([]ActionsPool, error) {
	rows, err := s.Pool.Query(ctx, `SELECT application_id,service,project,environment,application_name,revision,config,removed,message,updated_at FROM actions_pools WHERE ($1='' OR application_id=$1) AND (application_id,service)>($2,$3) ORDER BY application_id,service LIMIT 200`, application, afterApplication, afterService)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ActionsPool{}
	for rows.Next() {
		var p ActionsPool
		var data []byte
		if err = rows.Scan(&p.ApplicationID, &p.Service, &p.Project, &p.Environment, &p.ApplicationName, &p.Revision, &data, &p.Removed, &p.Message, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &p.Config); err != nil {
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
	rows, err := s.Pool.Query(ctx, `SELECT id,application_id,service,config,runner_id,phase,created_at,updated_at FROM actions_slots WHERE application_id=$1 AND service=$2 ORDER BY created_at,id LIMIT 21`, app, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ActionsSlot{}
	for rows.Next() {
		var v ActionsSlot
		var data []byte
		if err = rows.Scan(&v.ID, &v.ApplicationID, &v.Service, &data, &v.RunnerID, &v.Phase, &v.CreatedAt, &v.UpdatedAt); err != nil {
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
	if _, err = tx.Exec(ctx, `UPDATE actions_pools SET removed=true,updated_at=now() WHERE application_id=$1`, app.ID); err != nil {
		return err
	}
	for name, svc := range next.Services {
		if svc.Actions == nil {
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO actions_pools(application_id,service,project,environment,application_name,revision,config) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(application_id,service) DO UPDATE SET revision=EXCLUDED.revision,config=EXCLUDED.config,removed=false,updated_at=now()`, app.ID, name, app.Project, app.Environment, app.Name, revision, JSON(svc)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *Store) NewActionsSlot(ctx context.Context, p ActionsPool) (ActionsSlot, error) {
	v := ActionsSlot{ID: NewID(), ApplicationID: p.ApplicationID, Service: p.Service, Config: p.Config, Phase: "intent"}
	err := s.Pool.QueryRow(ctx, `INSERT INTO actions_slots(id,application_id,service,config) SELECT $1,$2,$3,$4 WHERE (SELECT count(*) FROM actions_slots WHERE application_id=$2 AND service=$3)<10 RETURNING created_at,updated_at`, v.ID, v.ApplicationID, v.Service, JSON(v.Config)).Scan(&v.CreatedAt, &v.UpdatedAt)
	return v, err
}
func (s *Store) UpdateActionsSlot(ctx context.Context, id string, runner int64, phase string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE actions_slots SET runner_id=$2,phase=$3,updated_at=now() WHERE id=$1`, id, runner, phase)
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
	_, err := s.Pool.Exec(ctx, `DELETE FROM actions_pools WHERE application_id=$1 AND service=$2 AND removed AND NOT EXISTS(SELECT 1 FROM actions_slots WHERE application_id=$1 AND service=$2)`, p.ApplicationID, p.Service)
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

// Keep the provider credential until both desired pools and old registrations
// have stopped referencing it. Removing a service must not strand cleanup.
func (s *Store) ActionsCredentialRequired(ctx context.Context, project, environment, application, name string) (bool, error) {
	var required bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM actions_pools p WHERE p.project=$1 AND p.environment=$2 AND p.application_name=$3 AND ((NOT p.removed AND p.config->'actions'->>'credential'=$4) OR EXISTS(SELECT 1 FROM actions_slots v WHERE v.application_id=p.application_id AND v.service=p.service AND v.config->'actions'->>'credential'=$4))) OR EXISTS(SELECT 1 FROM applications a CROSS JOIN LATERAL jsonb_each(a.spec->'services') AS svc WHERE a.project=$1 AND a.environment=$2 AND a.name=$3 AND svc.value->'actions'->>'credential'=$4)`, project, environment, application, name).Scan(&required)
	return required, err
}
