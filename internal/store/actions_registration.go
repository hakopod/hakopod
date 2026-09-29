package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/hakopod/hakopod/internal/actions"
)

func (v ActionsSlot) RegistrationBinding() (actions.RegistrationBinding, error) {
	if v.Config.Actions == nil {
		return actions.RegistrationBinding{}, errors.New("runner slot configuration is unavailable")
	}
	return actions.RegistrationBinding{ApplicationID: v.ApplicationID, Service: v.Service, SlotID: v.ID, RunnerID: v.ProviderRunnerID, Target: v.Config.Actions.ProviderTarget()}, nil
}

// SaveActionsProviderRegistration atomically records the opaque provider ID and
// its encrypted material before any manager starts. A lost write response is
// recovered by rereading the slot, never by overwriting a saved registration.
func (s *Store) SaveActionsProviderRegistration(ctx context.Context, slot ActionsSlot, runnerID string, sealed []byte) error {
	if slot.Config.Actions == nil || slot.Config.Actions.Provider.Effective() == actions.ProviderGitHub || !actions.ValidProviderID(runnerID) || len(sealed) < 28 || len(sealed) > actions.MaxSealedRegistrationBytes {
		return errors.New("provider registration is invalid or exceeds its storage bound")
	}
	result, err := s.Pool.Exec(ctx, `UPDATE actions_slots SET provider_runner_id=$4,encrypted_registration=$5,phase='starting',updated_at=now()
 WHERE id=$1 AND application_id=$2 AND service=$3 AND config=$6 AND phase='intent' AND provider_runner_id='' AND encrypted_registration IS NULL`,
		slot.ID, slot.ApplicationID, slot.Service, runnerID, sealed, JSON(slot.Config))
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("%w: runner registration changed; reread the slot", ErrConflict)
	}
	return nil
}

func (s *Store) ObserveActionsProviderSlot(ctx context.Context, slot ActionsSlot, runner actions.ProviderRunner, phase string) error {
	if runner.ID == "" || runner.ID != slot.ProviderRunnerID || runner.Name != "hakopod-"+slot.ID || (phase != "starting" && phase != "online" && phase != "busy") {
		return errors.New("provider observation does not match its recorded slot")
	}
	var observed any
	if !runner.ObservedAt.IsZero() {
		observed = runner.ObservedAt
	}
	result, err := s.Pool.Exec(ctx, `UPDATE actions_slots SET phase=$4,updated_at=COALESCE($5,now())
 WHERE id=$1 AND application_id=$2 AND provider_runner_id=$3 AND service=$6 AND config=$7
 AND phase IN ('starting','online','busy') AND phase=$8 AND updated_at=$9`, slot.ID, slot.ApplicationID, runner.ID, phase, observed, slot.Service, JSON(slot.Config), slot.Phase, slot.UpdatedAt)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("%w: runner slot changed", ErrConflict)
	}
	return nil
}
