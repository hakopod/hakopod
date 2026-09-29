package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/jackc/pgx/v5"
)

func providerLifecycleSlot(slot ActionsSlot) bool {
	return slot.Config.Actions != nil && slot.Config.Actions.Provider.Effective() != actions.ProviderGitHub
}

// MarkActionsProviderLaunch records the attempt before calling Kubernetes. Even
// a lost response must never make a one-job registration eligible for replay.
func (s *Store) MarkActionsProviderLaunch(ctx context.Context, slot ActionsSlot) (ActionsSlot, error) {
	if !providerLifecycleSlot(slot) || slot.Phase != "starting" || slot.ManagerLaunchAttempted || !actions.ValidProviderID(slot.ProviderRunnerID) || len(slot.EncryptedRegistration) == 0 {
		return ActionsSlot{}, fmt.Errorf("%w: runner launch is not eligible", ErrConflict)
	}
	return s.actionsProviderTransition(ctx, slot, "starting", true, "", false)
}

// MarkActionsProviderCleanup closes the launch path and persists the first
// deletion cursor. Later passes use the original target and stable runner ID.
func (s *Store) MarkActionsProviderCleanup(ctx context.Context, slot ActionsSlot) (ActionsSlot, error) {
	if !providerLifecycleSlot(slot) || slot.Phase == "cleanup" {
		return ActionsSlot{}, fmt.Errorf("%w: runner cleanup changed", ErrConflict)
	}
	return s.actionsProviderTransition(ctx, slot, "cleanup", slot.ManagerLaunchAttempted, slot.ProviderRunnerID, false)
}

// AdvanceActionsProviderCleanup persists one bounded provider operation. The
// stable registration ID and ciphertext remain intact until final deletion.
func (s *Store) AdvanceActionsProviderCleanup(ctx context.Context, slot ActionsSlot, runnerID string, confirmed bool) (ActionsSlot, error) {
	if !providerLifecycleSlot(slot) || slot.Phase != "cleanup" || slot.ProviderCleanupConfirmed || (runnerID != "" && !actions.ValidProviderID(runnerID)) || (confirmed && (runnerID != "" || slot.ProviderCleanupRunnerID != "")) {
		return ActionsSlot{}, fmt.Errorf("%w: runner cleanup changed", ErrConflict)
	}
	return s.actionsProviderTransition(ctx, slot, "cleanup", slot.ManagerLaunchAttempted, runnerID, confirmed)
}

func (s *Store) actionsProviderTransition(ctx context.Context, slot ActionsSlot, phase string, launched bool, cleanupID string, confirmed bool) (ActionsSlot, error) {
	current := slot
	err := s.Pool.QueryRow(ctx, `WITH eligible_pool AS (
 SELECT provider_hold_reason FROM actions_pools WHERE application_id=$2 AND service=$3 FOR UPDATE
)
	, transitioned AS (UPDATE actions_slots SET phase=$10,manager_launch_attempted=$11,provider_cleanup_runner_id=$12,provider_cleanup_confirmed=$13,updated_at=clock_timestamp()
 WHERE id=$1 AND application_id=$2 AND service=$3 AND config=$4 AND provider_runner_id=$5 AND phase=$6 AND updated_at=$7
 AND manager_launch_attempted=$8 AND provider_cleanup_runner_id=$9 AND provider_cleanup_confirmed=$14
 AND EXISTS(SELECT 1 FROM eligible_pool WHERE NOT $11::boolean OR $8::boolean OR provider_hold_reason='')
 RETURNING phase,updated_at,manager_launch_attempted,provider_cleanup_runner_id,provider_cleanup_confirmed
), preserved AS (
 UPDATE actions_jobs SET provider_removed=true,provider_history_version=provider_history_version+1
 WHERE $13::boolean AND EXISTS(SELECT 1 FROM transitioned) AND slot_id=$1 AND application_id=$2 AND service=$3 AND provider_runner_id=$5 AND provider_config=$4::jsonb->'actions'
)
 SELECT phase,updated_at,manager_launch_attempted,provider_cleanup_runner_id,provider_cleanup_confirmed FROM transitioned`,
		slot.ID, slot.ApplicationID, slot.Service, JSON(slot.Config), slot.ProviderRunnerID, slot.Phase, slot.UpdatedAt, slot.ManagerLaunchAttempted, slot.ProviderCleanupRunnerID,
		phase, launched, cleanupID, confirmed, slot.ProviderCleanupConfirmed).Scan(&current.Phase, &current.UpdatedAt, &current.ManagerLaunchAttempted, &current.ProviderCleanupRunnerID, &current.ProviderCleanupConfirmed)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActionsSlot{}, fmt.Errorf("%w: runner slot changed", ErrConflict)
	}
	return current, err
}

// DeleteActionsProviderSlot follows confirmed provider and Kubernetes absence.
// A stale controller cannot erase a newer registration or cleanup operation.
func (s *Store) DeleteActionsProviderSlot(ctx context.Context, slot ActionsSlot) error {
	if !providerLifecycleSlot(slot) || slot.Phase != "cleanup" || !slot.ProviderCleanupConfirmed || slot.ProviderCleanupRunnerID != "" {
		return fmt.Errorf("%w: runner cleanup is incomplete", ErrConflict)
	}
	result, err := s.Pool.Exec(ctx, `DELETE FROM actions_slots WHERE id=$1 AND application_id=$2 AND service=$3 AND config=$4 AND provider_runner_id=$5
 AND phase='cleanup' AND updated_at=$6 AND provider_cleanup_confirmed AND provider_cleanup_runner_id='' AND manager_launch_attempted=$7`,
		slot.ID, slot.ApplicationID, slot.Service, JSON(slot.Config), slot.ProviderRunnerID, slot.UpdatedAt, slot.ManagerLaunchAttempted)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("%w: runner slot changed", ErrConflict)
	}
	return nil
}
