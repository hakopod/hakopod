package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ActionsSlot reads one slot for the VM runtime, which is addressed by slot ID.
func (s *Store) ActionsSlot(ctx context.Context, id string) (*ActionsSlot, error) {
	v, err := scanActionsSlot(s.Pool.QueryRow(ctx, `SELECT `+actionsSlotColumns+` FROM actions_slots WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// ActionsVMSlot is a slot as the reaper sees it, with its pool's state.
type ActionsVMSlot struct {
	ActionsSlot
	PoolRemoved bool
}

// ActionsVMSlots returns the recorded slots among ids, keyed by slot ID.
func (s *Store) ActionsVMSlots(ctx context.Context, ids []string) (map[string]ActionsVMSlot, error) {
	out := map[string]ActionsVMSlot{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT v.`+strings.ReplaceAll(actionsSlotColumns, ",", ",v.")+`,p.removed FROM actions_slots v JOIN actions_pools p USING(application_id,service) WHERE v.id=ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ActionsVMSlot
		if item.ActionsSlot, err = scanActionsSlot(rows, &item.PoolRemoved); err != nil {
			return nil, err
		}
		out[item.ID] = item
	}
	return out, rows.Err()
}

// SaveActionsVMConfig keeps the sealed single-job configuration on the slot
// until its instance is launched. A repeated save keeps the first one.
func (s *Store) SaveActionsVMConfig(ctx context.Context, id string, sealed []byte) error {
	if len(sealed) < 28 || len(sealed) > actions.MaxSealedRegistrationBytes {
		return ErrInput
	}
	_, err := s.Pool.Exec(ctx, `UPDATE actions_slots SET encrypted_registration=COALESCE(encrypted_registration,$2) WHERE id=$1 AND instance_id=''`, id, sealed)
	return err
}

// ActionsCapacityInUse counts slots holding a place under a capacity's
// max_instances: reserved before launch and released when the slot is deleted.
func (s *Store) ActionsCapacityInUse(ctx context.Context, capacity string) (int64, error) {
	var n int64
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM actions_slots WHERE config->'actions'->>'capacity'=$1 AND instance_reserved_at IS NOT NULL`, capacity).Scan(&n)
	return n, err
}

// ReserveActionsInstance takes a place under max_instances for one slot. The
// count and the reservation serialize per capacity, so concurrent pools cannot
// overshoot the ceiling. An already reserved slot keeps its place.
func (s *Store) ReserveActionsInstance(ctx context.Context, slot, capacity string, max int64) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('actions-capacity:'||$1,0))`, capacity); err != nil {
		return false, err
	}
	var reserved bool
	var used int64
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM actions_slots WHERE id=$1 AND instance_reserved_at IS NOT NULL),
 (SELECT count(*) FROM actions_slots WHERE config->'actions'->>'capacity'=$2 AND instance_reserved_at IS NOT NULL)`, slot, capacity).Scan(&reserved, &used); err != nil {
		return false, err
	}
	if reserved {
		return true, nil
	}
	if used >= max {
		return false, nil
	}
	result, err := tx.Exec(ctx, `UPDATE actions_slots SET instance_reserved_at=now() WHERE id=$1 AND config->'actions'->>'capacity'=$2`, slot, capacity)
	if err != nil || result.RowsAffected() != 1 {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// RecordActionsInstance records the launched instance and drops the sealed
// configuration, which now lives only in the instance's user data. source is
// cold or standby; an adopted instance keeps the recorded source.
func (s *Store) RecordActionsInstance(ctx context.Context, slot, instanceID, instanceType, lifecycle, source string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE actions_slots SET instance_id=$2,instance_type=$3,instance_lifecycle=$4,encrypted_registration=NULL,
 instance_source=CASE WHEN $5='' THEN instance_source ELSE $5 END WHERE id=$1`, slot, instanceID, instanceType, lifecycle, source)
	return err
}

// ClaimActionsStandby records a standby as the slot's instance before it is
// started. It returns false when another slot claimed that standby first.
// The reservation time becomes the claim time, which the phase reads.
func (s *Store) ClaimActionsStandby(ctx context.Context, slot, instanceID, instanceType string) (bool, error) {
	result, err := s.Pool.Exec(ctx, `UPDATE actions_slots SET instance_id=$2,instance_type=$3,instance_lifecycle='on-demand',encrypted_registration=NULL,instance_source='standby',
 instance_reserved_at=now() WHERE id=$1 AND instance_id=''`, slot, instanceID, instanceType)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return false, nil
	}
	return err == nil && result.RowsAffected() == 1, err
}

// ActionsCapacityImage is a capacity's hakopod-minimal image. ImageID is the
// image launches use; a rebuild keeps it until BuildingImageID is available.
type ActionsCapacityImage struct {
	Capacity        string    `json:"capacity"`
	State           string    `json:"state"`
	BuilderID       string    `json:"builder_id,omitempty"`
	BuildingImageID string    `json:"building_image_id,omitempty"`
	ImageID         string    `json:"image_id,omitempty"`
	Message         string    `json:"message,omitempty"`
	RequestedAt     time.Time `json:"requested_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (s *Store) ActionsCapacityImages(ctx context.Context) (map[string]ActionsCapacityImage, error) {
	rows, err := s.Pool.Query(ctx, `SELECT capacity,state,builder_id,building_image_id,image_id,message,requested_at,updated_at FROM actions_capacity_images`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ActionsCapacityImage{}
	for rows.Next() {
		var v ActionsCapacityImage
		if err = rows.Scan(&v.Capacity, &v.State, &v.BuilderID, &v.BuildingImageID, &v.ImageID, &v.Message, &v.RequestedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out[v.Capacity] = v
	}
	return out, rows.Err()
}

// RequestActionsImageBuild starts a hakopod-minimal build for a capacity. A
// build already running is a conflict. The Actions worker does the work.
func (s *Store) RequestActionsImageBuild(ctx context.Context, p Principal, capacity string) error {
	result, err := s.Pool.Exec(ctx, `INSERT INTO actions_capacity_images(capacity,state) VALUES($1,'building')
 ON CONFLICT(capacity) DO UPDATE SET state='building',builder_id='',building_image_id='',message='',requested_at=now(),updated_at=now()
 WHERE actions_capacity_images.state<>'building'`, capacity)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("%w: an image for capacity %q is already being built", ErrConflict, capacity)
	}
	_, err = s.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'settings.actions_capacity.build_image','installation',$3)", p.ID, p.KeyID, JSON(map[string]string{"capacity": capacity}))
	return err
}

// SaveActionsCapacityImage records a build's progress.
func (s *Store) SaveActionsCapacityImage(ctx context.Context, v ActionsCapacityImage) error {
	if len(v.Message) > 512 {
		v.Message = v.Message[:512]
	}
	_, err := s.Pool.Exec(ctx, `UPDATE actions_capacity_images SET state=$2,builder_id=$3,building_image_id=$4,image_id=$5,message=$6,updated_at=now() WHERE capacity=$1`,
		v.Capacity, v.State, v.BuilderID, v.BuildingImageID, v.ImageID, v.Message)
	return err
}

// TakeActionsOnDemandLaunch spends one on-demand launch granted by a rerun.
// ponytail: the grant is spent even if the launch then fails; a lost grant
// only means one rerun may land on spot again.
func (s *Store) TakeActionsOnDemandLaunch(ctx context.Context, application, service string) (bool, error) {
	result, err := s.Pool.Exec(ctx, `UPDATE actions_pools SET on_demand_launches=on_demand_launches-1 WHERE application_id=$1 AND service=$2 AND on_demand_launches>0`, application, service)
	return err == nil && result.RowsAffected() == 1, err
}

type ActionsEviction struct {
	SlotID        string     `json:"slot_id"`
	ApplicationID string     `json:"-"`
	Service       string     `json:"-"`
	Capacity      string     `json:"capacity"`
	InstanceID    string     `json:"instance_id"`
	InstanceType  string     `json:"instance_type"`
	EvictedAt     time.Time  `json:"evicted_at"`
	Repository    string     `json:"repository,omitempty"`
	RunID         int64      `json:"run_id,omitempty"`
	RunAttempt    int64      `json:"run_attempt,omitempty"`
	RerunAt       *time.Time `json:"rerun_at,omitempty"`
	RerunError    string     `json:"rerun_error,omitempty"`
}

const actionsEvictionColumns = `slot_id,application_id,service,capacity,instance_id,instance_type,evicted_at,repository,run_id,run_attempt,rerun_at,rerun_error`

func scanActionsEvictions(rows pgx.Rows) ([]ActionsEviction, error) {
	defer rows.Close()
	out := []ActionsEviction{}
	for rows.Next() {
		var e ActionsEviction
		if err := rows.Scan(&e.SlotID, &e.ApplicationID, &e.Service, &e.Capacity, &e.InstanceID, &e.InstanceType, &e.EvictedAt, &e.Repository, &e.RunID, &e.RunAttempt, &e.RerunAt, &e.RerunError); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RecordActionsEviction notes a spot interruption once per slot. Rows are kept
// for 30 days: long enough for the rerun and the instance type cool-down.
func (s *Store) RecordActionsEviction(ctx context.Context, slot ActionsSlot, capacity, instanceID, instanceType string) error {
	_, err := s.Pool.Exec(ctx, `WITH expired AS (DELETE FROM actions_evictions WHERE evicted_at<now()-interval '30 days')
 INSERT INTO actions_evictions(slot_id,application_id,service,capacity,instance_id,instance_type) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(slot_id) DO NOTHING`,
		slot.ID, slot.ApplicationID, slot.Service, capacity, instanceID, instanceType)
	return err
}

// EvictedActionsInstanceTypes lists types of a capacity to skip: an instance
// type evicted three times within 30 minutes rests for an hour after the third.
func (s *Store) EvictedActionsInstanceTypes(ctx context.Context, capacity string) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT a.instance_type FROM actions_evictions a WHERE a.capacity=$1 AND a.evicted_at>now()-interval '1 hour'
 AND (SELECT count(*) FROM actions_evictions b WHERE b.capacity=a.capacity AND b.instance_type=a.instance_type AND b.evicted_at BETWEEN a.evicted_at-interval '30 minutes' AND a.evicted_at)>=3`, capacity)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ActionsEvictions lists a pool's recent interruptions for its status.
func (s *Store) ActionsEvictions(ctx context.Context, application, service string) ([]ActionsEviction, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+actionsEvictionColumns+` FROM actions_evictions WHERE application_id=$1 AND service=$2 ORDER BY evicted_at DESC,slot_id LIMIT 20`, application, service)
	if err != nil {
		return nil, err
	}
	return scanActionsEvictions(rows)
}

// RecordActionsEvictedJob attaches the run of a job that failed on an evicted
// runner. The delivering connection's scope bounds which pools it can reach,
// and the repository must belong to the pool's own GitHub target.
func (s *Store) RecordActionsEvictedJob(ctx context.Context, slot, repository string, runID, attempt int64, project, environment string) error {
	var config spec.Service
	err := s.Pool.QueryRow(ctx, `SELECT p.config FROM actions_evictions e JOIN actions_pools p USING(application_id,service)
 WHERE e.slot_id=$1 AND e.run_id=0 AND ($2='' OR (p.project=$2 AND p.environment=$3))`, slot, project, environment).Scan(&config)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	o := actions.Observation{Repository: repository, RunID: runID, Attempt: attempt}
	if config.Actions == nil || !o.Valid(config.Actions.Target()) {
		return nil
	}
	_, err = s.Pool.Exec(ctx, `UPDATE actions_evictions SET repository=$2,run_id=$3,run_attempt=$4 WHERE slot_id=$1 AND run_id=0`, slot, repository, runID, attempt)
	return err
}

// PendingActionsReruns lists interrupted jobs whose run is known and not yet
// rerun.
func (s *Store) PendingActionsReruns(ctx context.Context) ([]ActionsEviction, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+actionsEvictionColumns+` FROM actions_evictions WHERE run_id>0 AND rerun_at IS NULL ORDER BY evicted_at LIMIT 20`)
	if err != nil {
		return nil, err
	}
	return scanActionsEvictions(rows)
}

// ClaimActionsRerun decides once per run attempt whether to rerun it. It
// returns how many interrupted jobs the rerun covers, or zero when another
// worker already claimed the attempt or the pool's spot_retries are used up.
func (s *Store) ClaimActionsRerun(ctx context.Context, e ActionsEviction, retries int64) (int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('actions-rerun:'||$1||'/'||$2::bigint,0))`, e.Repository, e.RunID); err != nil {
		return 0, err
	}
	var claimed bool
	var used int64
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM actions_evictions WHERE repository=$1 AND run_id=$2 AND run_attempt=$3 AND rerun_at IS NOT NULL),
 (SELECT count(DISTINCT run_attempt) FROM actions_evictions WHERE application_id=$4 AND service=$5 AND repository=$1 AND run_id=$2 AND rerun_at IS NOT NULL AND rerun_error='')`,
		e.Repository, e.RunID, e.RunAttempt, e.ApplicationID, e.Service).Scan(&claimed, &used); err != nil {
		return 0, err
	}
	reason := ""
	switch {
	case claimed:
		reason = "Covered by the rerun of this run attempt."
	case used >= retries:
		reason = fmt.Sprintf("Not rerun: this run already used its %d spot retries.", retries)
	}
	result, err := tx.Exec(ctx, `UPDATE actions_evictions SET rerun_at=now(),rerun_error=$4 WHERE repository=$1 AND run_id=$2 AND run_attempt=$3 AND rerun_at IS NULL`,
		e.Repository, e.RunID, e.RunAttempt, reason)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil || reason != "" {
		return 0, err
	}
	return int(result.RowsAffected()), nil
}

// FinishActionsRerun records the rerun's outcome. A successful rerun grants
// the pool one on-demand launch per interrupted job it covers.
func (s *Store) FinishActionsRerun(ctx context.Context, e ActionsEviction, jobs int, failure string) error {
	if len(failure) > 512 {
		failure = failure[:512]
	}
	if failure != "" {
		_, err := s.Pool.Exec(ctx, `UPDATE actions_evictions SET rerun_error=$4 WHERE repository=$1 AND run_id=$2 AND run_attempt=$3`, e.Repository, e.RunID, e.RunAttempt, failure)
		return err
	}
	_, err := s.Pool.Exec(ctx, `UPDATE actions_pools SET on_demand_launches=LEAST(100,on_demand_launches+$3) WHERE application_id=$1 AND service=$2`, e.ApplicationID, e.Service, jobs)
	return err
}
