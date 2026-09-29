package store

import (
	"context"
	"encoding/json"
	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	"time"
)

type ActionsJob struct {
	EncryptedProviderIntent []byte               `json:"-"`
	Provider                actions.Provider     `json:"provider,omitempty"`
	ProviderRunnerID        string               `json:"provider_runner_id,omitempty"`
	NativeJob               *actions.ProviderJob `json:"native_job,omitempty"`
	DiscoveryState          string               `json:"discovery_state,omitempty"`
	ProviderRemoved         bool                 `json:"-"`
	CanCancel               bool                 `json:"can_cancel,omitempty"`
	ProviderConfig          *spec.Actions        `json:"-"`
	CheckedAt               time.Time            `json:"-"`
	SlotID                  string               `json:"slot_id"`
	RunnerID                int64                `json:"runner_id"`
	Observation             actions.Observation  `json:"observation"`
	Job                     *actions.Job         `json:"job"`
	CreatedAt               time.Time            `json:"created_at"`
	UpdatedAt               time.Time            `json:"updated_at"`
}

func (s *Store) RecordActionsJob(ctx context.Context, slot ActionsSlot, o actions.Observation) error {
	if slot.Config.Actions == nil || !o.Valid(slot.Config.Actions.Target()) {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO actions_jobs(slot_id,application_id,service,runner_id,observation,provider_config) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(slot_id) DO NOTHING`, slot.ID, slot.ApplicationID, slot.Service, slot.RunnerID, JSON(o), JSON(slot.Config.Actions))
	if err != nil {
		return err
	}
	// Retention covers metadata only. Never copy workflow logs into PostgreSQL.
	_, err = s.Pool.Exec(ctx, `DELETE FROM actions_jobs WHERE application_id=$1 AND service=$2 AND (created_at<now()-interval '30 days' OR slot_id IN (SELECT slot_id FROM actions_jobs WHERE application_id=$1 AND service=$2 ORDER BY created_at DESC,slot_id OFFSET 1000))`, slot.ApplicationID, slot.Service)
	return err
}
func (s *Store) ActionsJobs(ctx context.Context, app, service string) ([]ActionsJob, error) {
	rows, err := s.Pool.Query(ctx, `SELECT slot_id,runner_id,observation,provider_job,created_at,updated_at,checked_at,provider_config,provider_runner_id,native_job,discovery_state,provider_removed FROM actions_jobs WHERE application_id=$1 AND service=$2 AND created_at>=now()-interval '30 days' ORDER BY created_at DESC,slot_id LIMIT 100`, app, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ActionsJob{}
	for rows.Next() {
		var item ActionsJob
		var observation, job, config, native []byte
		if err = rows.Scan(&item.SlotID, &item.RunnerID, &observation, &job, &item.CreatedAt, &item.UpdatedAt, &item.CheckedAt, &config, &item.ProviderRunnerID, &native, &item.DiscoveryState, &item.ProviderRemoved); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(observation, &item.Observation); err != nil {
			return nil, err
		}
		if len(config) > 0 {
			if err = json.Unmarshal(config, &item.ProviderConfig); err != nil {
				return nil, err
			}
		}
		if len(job) > 0 {
			if err = json.Unmarshal(job, &item.Job); err != nil {
				return nil, err
			}
		}
		if item.ProviderRunnerID != "" && item.ProviderConfig != nil {
			item.Provider = item.ProviderConfig.Provider.Effective()
		}
		if len(native) > 0 {
			if err = json.Unmarshal(native, &item.NativeJob); err != nil {
				return nil, err
			}
		}
		item.CanCancel = item.Provider == actions.ProviderGitLab && !item.ProviderRemoved && item.NativeJob != nil && item.NativeJob.Status != "completed"
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *Store) UpdateActionsJob(ctx context.Context, app, service, slot string, job actions.Job) error {
	_, err := s.Pool.Exec(ctx, `UPDATE actions_jobs SET provider_job=$4,updated_at=now() WHERE application_id=$1 AND service=$2 AND slot_id=$3`, app, service, slot, JSON(job))
	return err
}

// A database lease prevents concurrent dashboard readers from multiplying
// provider requests. Failed/pending jobs rejoin the refresh rotation too.
func (s *Store) ClaimActionsJobRefresh(ctx context.Context, app, service, slot string) (bool, error) {
	result, err := s.Pool.Exec(ctx, `UPDATE actions_jobs SET checked_at=now() WHERE application_id=$1 AND service=$2 AND slot_id=$3 AND checked_at<now()-interval '15 seconds'`, app, service, slot)
	return err == nil && result.RowsAffected() == 1, err
}
