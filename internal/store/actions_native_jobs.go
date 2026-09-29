package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
)

type ActionsNativeHistory struct {
	Job              ActionsJob
	Paused, Finished bool
	Attempts         int
	StartedAt        *time.Time
	Version          int64
}

func nativeJobBound(config *spec.Actions, runnerID, slotID string, job *actions.ProviderJob) bool {
	if config == nil || config.Provider.Effective() != actions.ProviderGitLab || !actions.ValidProviderID(runnerID) || job == nil {
		return false
	}
	target, err := config.ProviderTarget().Canonical()
	identity := job.Identity
	if err != nil || identity.RunnerID != runnerID || identity.RunnerName != "hakopod-"+slotID || identity.Attempt != 0 || !actions.ValidProviderID(identity.Repository) || !actions.ValidProviderID(identity.RunID) || !actions.ValidProviderID(identity.JobID) || len(job.Name) > 512 || len(job.Steps) > 1000 || len(JSON(job)) > 32<<10 {
		return false
	}
	if target.GitLab.ProjectID > 0 && identity.Repository != strconv.FormatInt(target.GitLab.ProjectID, 10) {
		return false
	}
	return job.Status == "queued" || job.Status == "in_progress" || job.Status == "completed"
}

func (s *Store) ActionsProviderPoolHeld(ctx context.Context, app, service string) (bool, error) {
	var held bool
	err := s.Pool.QueryRow(ctx, `SELECT provider_hold_reason<>'' FROM actions_pools WHERE application_id=$1 AND service=$2`, app, service).Scan(&held)
	return held, err
}

func (s *Store) EnsureActionsNativeHistory(ctx context.Context, slot ActionsSlot) (ActionsNativeHistory, error) {
	if slot.Config.Actions == nil || slot.Config.Actions.Provider.Effective() != actions.ProviderGitLab || !slot.ManagerLaunchAttempted || !actions.ValidProviderID(slot.ProviderRunnerID) {
		return ActionsNativeHistory{}, ErrInput
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO actions_jobs(slot_id,application_id,service,runner_id,observation,provider_config,provider_runner_id,discovery_state,encrypted_provider_intent)
 SELECT id,application_id,service,0,'{}',config->'actions',provider_runner_id,'pending',encrypted_provider_intent FROM actions_slots
 WHERE id=$1 AND application_id=$2 AND service=$3 AND config=$4 AND provider_runner_id=$5 AND manager_launch_attempted AND phase=$6 AND updated_at=$7
 ON CONFLICT(slot_id) DO NOTHING`, slot.ID, slot.ApplicationID, slot.Service, JSON(slot.Config), slot.ProviderRunnerID, slot.Phase, slot.UpdatedAt)
	if err != nil {
		return ActionsNativeHistory{}, err
	}
	if _, err = s.Pool.Exec(ctx, `DELETE FROM actions_jobs j WHERE application_id=$1 AND service=$2
 AND NOT EXISTS(SELECT 1 FROM actions_slots s WHERE s.id=j.slot_id)
 AND (created_at<now()-interval '30 days' OR slot_id IN (SELECT slot_id FROM actions_jobs WHERE application_id=$1 AND service=$2 ORDER BY created_at DESC,slot_id OFFSET 1000))`, slot.ApplicationID, slot.Service); err != nil {
		return ActionsNativeHistory{}, err
	}
	history, err := s.ActionsNativeHistory(ctx, slot.ApplicationID, slot.Service, slot.ID)
	if err != nil {
		return history, err
	}
	if history.Job.ProviderRunnerID != slot.ProviderRunnerID || !reflect.DeepEqual(history.Job.ProviderConfig, slot.Config.Actions) || !bytes.Equal(history.Job.EncryptedProviderIntent, slot.EncryptedProviderIntent) {
		return ActionsNativeHistory{}, fmt.Errorf("%w: native history binding changed", ErrConflict)
	}
	return history, nil
}

func (s *Store) ActionsNativeHistory(ctx context.Context, app, service, slot string) (ActionsNativeHistory, error) {
	h := ActionsNativeHistory{}
	var config, job []byte
	err := s.Pool.QueryRow(ctx, `SELECT slot_id,provider_runner_id,provider_config,native_job,discovery_state,created_at,updated_at,checked_at,cleanup_paused,discovery_finished,discovery_attempts,discovery_started_at,provider_history_version,provider_removed,encrypted_provider_intent
 FROM actions_jobs WHERE application_id=$1 AND service=$2 AND slot_id=$3 AND provider_runner_id<>''`, app, service, slot).Scan(&h.Job.SlotID, &h.Job.ProviderRunnerID, &config, &job, &h.Job.DiscoveryState, &h.Job.CreatedAt, &h.Job.UpdatedAt, &h.Job.CheckedAt, &h.Paused, &h.Finished, &h.Attempts, &h.StartedAt, &h.Version, &h.Job.ProviderRemoved, &h.Job.EncryptedProviderIntent)
	if err != nil {
		return h, err
	}
	if err = json.Unmarshal(config, &h.Job.ProviderConfig); err != nil {
		return h, err
	}
	if h.Job.ProviderConfig != nil {
		h.Job.Provider = h.Job.ProviderConfig.Provider.Effective()
	}
	if len(job) > 0 {
		err = json.Unmarshal(job, &h.Job.NativeJob)
	}
	h.Job.CanCancel = !h.Job.ProviderRemoved && h.Job.NativeJob != nil && h.Job.NativeJob.Status != "completed"
	return h, err
}

func (s *Store) MarkActionsNativePaused(ctx context.Context, slot ActionsSlot, h ActionsNativeHistory) error {
	if h.Paused || h.Job.ProviderRunnerID != slot.ProviderRunnerID || !reflect.DeepEqual(h.Job.ProviderConfig, slot.Config.Actions) {
		return ErrConflict
	}
	result, err := s.Pool.Exec(ctx, `UPDATE actions_jobs SET cleanup_paused=true,discovery_started_at=COALESCE(discovery_started_at,clock_timestamp()),provider_history_version=provider_history_version+1
 WHERE slot_id=$1 AND application_id=$2 AND service=$3 AND provider_runner_id=$4 AND provider_config=$5 AND provider_history_version=$6 AND NOT cleanup_paused`, slot.ID, slot.ApplicationID, slot.Service, slot.ProviderRunnerID, JSON(slot.Config.Actions), h.Version)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

// RecordActionsNativeJob refreshes metadata without replacing its first verified
// identity. Terminal observations cannot regress when concurrent readers finish.
func (s *Store) RecordActionsNativeJob(ctx context.Context, app, service string, h ActionsNativeHistory, job actions.ProviderJob) error {
	if !nativeJobBound(h.Job.ProviderConfig, h.Job.ProviderRunnerID, h.Job.SlotID, &job) {
		return ErrInput
	}
	result, err := s.Pool.Exec(ctx, `UPDATE actions_jobs SET native_job=$7,discovery_state='observed',updated_at=clock_timestamp(),provider_history_version=provider_history_version+1
 WHERE slot_id=$1 AND application_id=$2 AND service=$3 AND provider_runner_id=$4 AND provider_config=$5 AND provider_history_version=$6 AND discovery_state<>'reuse_detected'
 AND (native_job IS NULL OR native_job->'identity'=$8::jsonb) AND (native_job IS NULL OR native_job->>'status'<>'completed' OR $7::jsonb->>'status'='completed')`, h.Job.SlotID, app, service, h.Job.ProviderRunnerID, JSON(h.Job.ProviderConfig), h.Version, JSON(job), JSON(job.Identity))
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

// FinishActionsNativeDiscovery bounds cleanup's read-credential dependency. An
// empty/failed scan becomes unavailable after three attempts or sixty seconds;
// it never becomes a claim that the runner processed no job.
func (s *Store) FinishActionsNativeDiscovery(ctx context.Context, slot ActionsSlot, h ActionsNativeHistory, job *actions.ProviderJob, reuse []actions.ProviderJob) error {
	if !h.Paused || h.Finished || h.Attempts >= 3 || h.Job.ProviderRunnerID != slot.ProviderRunnerID || !reflect.DeepEqual(h.Job.ProviderConfig, slot.Config.Actions) {
		return ErrConflict
	}
	if job != nil && !nativeJobBound(h.Job.ProviderConfig, h.Job.ProviderRunnerID, h.Job.SlotID, job) {
		return ErrInput
	}
	if len(reuse) != 0 && len(reuse) != 2 {
		return ErrInput
	}
	for _, item := range reuse {
		if !nativeJobBound(h.Job.ProviderConfig, h.Job.ProviderRunnerID, h.Job.SlotID, &item) {
			return ErrInput
		}
	}
	if len(reuse) == 2 && reuse[0].Identity == reuse[1].Identity {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Pool first matches launch/cleanup transitions and hold recovery. A reader
	// detecting reuse must not invert the lifecycle's pool-to-history lock order.
	var poolExists string
	if err = tx.QueryRow(ctx, `SELECT application_id FROM actions_pools WHERE application_id=$1 AND service=$2 FOR UPDATE`, slot.ApplicationID, slot.Service).Scan(&poolExists); err != nil {
		return err
	}
	var payload, evidence any
	if job != nil {
		payload = JSON(job)
	}
	if len(reuse) > 0 {
		evidence = JSON(reuse)
	}
	result, err := tx.Exec(ctx, `UPDATE actions_jobs SET native_job=CASE WHEN native_job->>'status'='completed' AND $7::jsonb->>'status'<>'completed' THEN native_job ELSE COALESCE($7::jsonb,native_job) END,provider_reuse_evidence=COALESCE($8::jsonb,provider_reuse_evidence),
 discovery_attempts=discovery_attempts+1,
 discovery_finished=(discovery_state='reuse_detected' OR $7 IS NOT NULL OR $8 IS NOT NULL OR discovery_attempts>=2 OR discovery_started_at<=now()-interval '60 seconds'),
 discovery_state=CASE WHEN discovery_state='reuse_detected' OR $8 IS NOT NULL THEN 'reuse_detected' WHEN $7 IS NOT NULL THEN 'observed' WHEN discovery_attempts>=2 OR discovery_started_at<=now()-interval '60 seconds' THEN CASE WHEN native_job IS NULL THEN 'unavailable' ELSE 'observed' END ELSE discovery_state END,
 updated_at=clock_timestamp(),provider_history_version=provider_history_version+1
 WHERE slot_id=$1 AND application_id=$2 AND service=$3 AND provider_runner_id=$4 AND provider_config=$5 AND provider_history_version=$6 AND cleanup_paused AND NOT discovery_finished
 AND ($7::jsonb IS NULL OR native_job IS NULL OR native_job->'identity'=$7::jsonb->'identity')`, slot.ID, slot.ApplicationID, slot.Service, slot.ProviderRunnerID, JSON(slot.Config.Actions), h.Version, payload, evidence)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	if len(reuse) > 0 {
		if err = recordActionsProviderHold(ctx, tx, slot.ApplicationID, slot.Service, h, reuse); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) HoldActionsProviderReuse(ctx context.Context, app, service string, h ActionsNativeHistory, reuse []actions.ProviderJob) error {
	if len(reuse) != 2 {
		return ErrInput
	}
	for _, job := range reuse {
		if !nativeJobBound(h.Job.ProviderConfig, h.Job.ProviderRunnerID, h.Job.SlotID, &job) {
			return ErrInput
		}
	}
	if reuse[0].Identity == reuse[1].Identity {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var poolExists string
	if err = tx.QueryRow(ctx, `SELECT application_id FROM actions_pools WHERE application_id=$1 AND service=$2 FOR UPDATE`, app, service).Scan(&poolExists); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE actions_jobs SET discovery_state='reuse_detected',provider_reuse_evidence=$7,updated_at=clock_timestamp(),provider_history_version=provider_history_version+1
 WHERE slot_id=$1 AND application_id=$2 AND service=$3 AND provider_runner_id=$4 AND provider_config=$5 AND provider_history_version=$6`, h.Job.SlotID, app, service, h.Job.ProviderRunnerID, JSON(h.Job.ProviderConfig), h.Version, JSON(reuse))
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	if err = recordActionsProviderHold(ctx, tx, app, service, h, reuse); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
