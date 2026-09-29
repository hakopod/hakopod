package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/jackc/pgx/v5"
)

// ActionsProviderHold retains the first verified incident independently of job
// history retention. Its evidence contains provider metadata, never credentials.
type ActionsProviderHold struct {
	ID          string                `json:"id"`
	Reason      string                `json:"reason"`
	ObservedAt  time.Time             `json:"observed_at"`
	SlotID      string                `json:"slot_id"`
	Provider    actions.Provider      `json:"provider"`
	InstanceURL string                `json:"instance_url"`
	Jobs        []actions.ProviderJob `json:"jobs"`
}

type ActionsProviderHoldState struct {
	Hold        *ActionsProviderHold `json:"hold"`
	ActiveSlots int                  `json:"active_slots"`
}

type actionsProviderHoldEvidence struct {
	SlotID      string                `json:"slot_id"`
	Provider    actions.Provider      `json:"provider"`
	InstanceURL string                `json:"instance_url"`
	Jobs        []actions.ProviderJob `json:"jobs"`
}

// Recovery is an infrequent operator action. Serialize it per process so waiting
// transactions cannot exhaust the database pool before live reauthorization gets
// a connection. PostgreSQL's pool-row lock still coordinates other API processes.
var actionsProviderHoldReleaseGate = make(chan struct{}, 1)

func newActionsProviderHoldID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}

func validActionsProviderHoldID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	b, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil && len(b) == 16 && b[6]>>4 == 4 && b[8]>>6 == 2
}

// The caller holds the pool row before changing job history. Repeated detection
// preserves the original incident, its acknowledgement ID and its observation time.
func recordActionsProviderHold(ctx context.Context, tx pgx.Tx, app, service string, h ActionsNativeHistory, reuse []actions.ProviderJob) error {
	if len(reuse) != 2 || reuse[0].Identity == reuse[1].Identity {
		return ErrInput
	}
	for _, job := range reuse {
		if !nativeJobBound(h.Job.ProviderConfig, h.Job.ProviderRunnerID, h.Job.SlotID, &job) {
			return ErrInput
		}
	}
	target, err := h.Job.ProviderConfig.ProviderTarget().Canonical()
	if err != nil || target.GitLab == nil {
		return ErrInput
	}
	evidence := JSON(actionsProviderHoldEvidence{SlotID: h.Job.SlotID, Provider: target.Provider, InstanceURL: target.GitLab.URL, Jobs: reuse})
	if len(evidence) > 128<<10 {
		return ErrInput
	}
	id, err := newActionsProviderHoldID()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE actions_pools SET provider_hold_id=$3,provider_hold_reason='runner_reuse',provider_hold_observed_at=clock_timestamp(),provider_hold_evidence=$4,
 message='A one-job runner processed multiple jobs. The pool is held for operator inspection.'
 WHERE application_id=$1 AND service=$2 AND provider_hold_reason=''`, app, service, id, evidence)
	return err
}

const actionsProviderHoldColumns = `COALESCE(p.provider_hold_id::text,''),p.provider_hold_reason,p.provider_hold_observed_at,p.provider_hold_evidence,a.project,a.environment,a.name`

func scanActionsProviderHold(row pgx.Row) (ActionsProviderHoldState, string, string, string, error) {
	state := ActionsProviderHoldState{}
	var id, reason, project, environment, application string
	var observed *time.Time
	var data []byte
	if err := row.Scan(&id, &reason, &observed, &data, &project, &environment, &application, &state.ActiveSlots); err != nil {
		return state, project, environment, application, err
	}
	if reason == "" {
		return state, project, environment, application, nil
	}
	var evidence actionsProviderHoldEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		return state, project, environment, application, err
	}
	if !validActionsProviderHoldID(id) || observed == nil || len(evidence.Jobs) != 2 {
		return state, project, environment, application, errors.New("incomplete provider hold evidence")
	}
	state.Hold = &ActionsProviderHold{ID: id, Reason: reason, ObservedAt: *observed, SlotID: evidence.SlotID, Provider: evidence.Provider, InstanceURL: evidence.InstanceURL, Jobs: evidence.Jobs}
	return state, project, environment, application, nil
}

func (s *Store) ActionsProviderHold(ctx context.Context, p Principal, app, service string) (ActionsProviderHoldState, error) {
	state, project, environment, application, err := scanActionsProviderHold(s.Pool.QueryRow(ctx, `SELECT `+actionsProviderHoldColumns+`,
 (SELECT count(*) FROM actions_slots s WHERE s.application_id=p.application_id AND s.service=p.service)
 FROM actions_pools p JOIN applications a ON a.id=p.application_id WHERE p.application_id=$1 AND p.service=$2`, app, service))
	if err != nil {
		return ActionsProviderHoldState{}, err
	}
	if !p.Allows("deployments:read", project, environment, application) {
		return ActionsProviderHoldState{}, ErrForbidden
	}
	live, err := s.KeyPrincipal(ctx, p.KeyID)
	if err != nil || live.ID != p.ID || !live.Allows("deployments:read", project, environment, application) {
		return ActionsProviderHoldState{}, ErrForbidden
	}
	return state, nil
}

func (s *Store) ReleaseActionsProviderHold(ctx context.Context, p Principal, app, service, holdID string, acknowledge bool) error {
	if !acknowledge || !validActionsProviderHoldID(holdID) {
		return fmt.Errorf("%w: acknowledge the inspected hold ID before releasing this pool", ErrInput)
	}
	select {
	case actionsProviderHoldReleaseGate <- struct{}{}:
		defer func() { <-actionsProviderHoldReleaseGate }()
	default:
		return fmt.Errorf("%w: another runner pool recovery is in progress; retry shortly", ErrConflict)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	state, project, environment, application, err := scanActionsProviderHold(tx.QueryRow(ctx, `SELECT `+actionsProviderHoldColumns+`,0
 FROM actions_pools p JOIN applications a ON a.id=p.application_id WHERE p.application_id=$1 AND p.service=$2 FOR UPDATE OF p`, app, service))
	if err != nil {
		return err
	}
	// Recheck after waiting for the pool lock. A cached request principal cannot
	// release a hold after its key is revoked or its application access changes.
	live, err := s.KeyPrincipal(ctx, p.KeyID)
	if err != nil || live.ID != p.ID || !p.Allows("deployments:write", project, environment, application) || !live.Allows("deployments:write", project, environment, application) {
		return ErrForbidden
	}
	if state.Hold == nil || !strings.EqualFold(state.Hold.ID, holdID) {
		return fmt.Errorf("%w: this hold has changed; inspect it again before releasing the pool", ErrConflict)
	}
	// Launch also locks the pool first. Read slots only after acquiring that lock,
	// so a concurrent launch cannot appear between this check and the release.
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM actions_slots WHERE application_id=$1 AND service=$2`, app, service).Scan(&state.ActiveSlots); err != nil {
		return err
	}
	if state.ActiveSlots != 0 {
		return fmt.Errorf("%w: wait for every runner slot to finish cleanup before releasing this pool", ErrConflict)
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'actions.provider_hold.release',$3,$4)`, live.ID, live.KeyID, app, JSON(map[string]any{"service": service, "hold": state.Hold}))
	if err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE actions_pools SET provider_hold_id=NULL,provider_hold_reason='',provider_hold_observed_at=NULL,provider_hold_evidence=NULL,message='',updated_at=clock_timestamp()
 WHERE application_id=$1 AND service=$2 AND provider_hold_id=$3`, app, service, state.Hold.ID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}
