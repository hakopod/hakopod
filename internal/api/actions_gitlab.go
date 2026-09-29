package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type gitlabRunnerProvider interface {
	actions.ProviderClient
	actions.ProviderDrainClient
	GetOwned(context.Context, actions.ProviderTarget, string, string) (actions.ProviderRunner, error)
	DeleteOwned(context.Context, actions.ProviderTarget, string, string) error
}

type gitlabActionsRuntime interface {
	SaveGitLabActionsConfig(context.Context, cluster.Target, string, string, spec.Service, actions.GitLabManagerConfig, cluster.GitLabActionsRuntime) error
	StartGitLabActionsPod(context.Context, cluster.Target, string, string, spec.Service, actions.GitLabManagerConfig, cluster.GitLabActionsRuntime) error
	DeleteGitLabActionsConfig(context.Context, cluster.Target, string) error
	DeleteGitLabActionsPolicy(context.Context, cluster.Target, string) error
}

type gitlabActionsSlotStore interface {
	SaveActionsProviderRegistration(context.Context, store.ActionsSlot, string, []byte) error
	MarkActionsProviderLaunch(context.Context, store.ActionsSlot) (store.ActionsSlot, error)
	MarkActionsProviderCleanup(context.Context, store.ActionsSlot) (store.ActionsSlot, error)
	AdvanceActionsProviderCleanup(context.Context, store.ActionsSlot, string, bool) (store.ActionsSlot, error)
	DeleteActionsProviderSlot(context.Context, store.ActionsSlot) error
}

func (s *Server) gitlabSlotStore() gitlabActionsSlotStore {
	if s.actionsGitLabTestStore != nil {
		return s.actionsGitLabTestStore
	}
	return s.Store
}

func unsupportedActionsProvider(provider actions.Provider) error {
	return &actions.UnsupportedProviderError{Provider: provider, Reason: "managed runner execution is not qualified or configured for this provider"}
}

func actionsConfigProvider(config spec.Service) actions.Provider {
	if config.Actions == nil {
		return "invalid"
	}
	return config.Actions.Provider.Effective()
}

// Use a slot's original target and credential even after the pool changes. Keep
// the caller's application lock fence and network configuration intact.
func actionsSlotTarget(t cluster.Target, slot store.ActionsSlot) cluster.Target {
	t.Spec.Services = maps.Clone(t.Spec.Services)
	if t.Spec.Services == nil {
		t.Spec.Services = make(map[string]spec.Service)
	}
	t.Spec.Services[slot.Service] = slot.Config
	return t
}

func (s *Server) gitlabRunnerClient(ctx context.Context, t cluster.Target, config spec.Service) (gitlabRunnerProvider, error) {
	if actionsConfigProvider(config) != actions.ProviderGitLab || s.actionsGitLabClient == nil {
		return nil, unsupportedActionsProvider(actionsConfigProvider(config))
	}
	if _, err := config.Actions.ProviderTarget().Canonical(); err != nil {
		return nil, &actions.GitLabError{Kind: "scope"}
	}
	if err := actionsFence(ctx, t); err != nil {
		return nil, err
	}
	token, err := s.actionRuntime().ActionsCredential(ctx, t, config.Actions.Credential)
	if err != nil {
		return nil, err
	}
	client, err := s.actionsGitLabClient(ctx, t, config, token)
	if err == nil && (client == nil || client.Capabilities().Provider != actions.ProviderGitLab) {
		return nil, unsupportedActionsProvider(actions.ProviderGitLab)
	}
	return client, err
}

func (s *Server) gitlabRuntimeConfig(ctx context.Context, t cluster.Target, config spec.Service) (cluster.GitLabActionsRuntime, error) {
	if s.actionsGitLabRuntime == nil || actionsConfigProvider(config) != actions.ProviderGitLab {
		return cluster.GitLabActionsRuntime{}, unsupportedActionsProvider(actions.ProviderGitLab)
	}
	runtime, err := s.actionsGitLabRuntime(ctx, t, config)
	if err != nil {
		return runtime, err
	}
	if err = cluster.ValidateGitLabActionsRuntime(runtime); err != nil {
		return runtime, err
	}
	target, err := config.Actions.ProviderTarget().Canonical()
	if err != nil || runtime.TransportPolicy.CoordinatorURL != target.GitLab.URL || runtime.Images.Architecture != config.Architecture || runtime.Images.Manager != config.Image {
		return runtime, errors.New("GitLab runtime qualification does not match the configured service")
	}
	return runtime, nil
}

// This private envelope binds the qualified image pair and transport policy to
// the registration. It is only stored inside the authenticated ciphertext.
type gitlabSlotRegistration struct {
	Version      int                          `json:"version"`
	Registration actions.GitLabManagerConfig  `json:"registration"`
	Runtime      cluster.GitLabActionsRuntime `json:"runtime"`
}

func decodeGitLabPrivate(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("GitLab private registration is invalid")
	}
	return nil
}

func validateGitLabPrivate(slot store.ActionsSlot, native actions.GitLabManagerConfig) error {
	if actionsConfigProvider(slot.Config) != actions.ProviderGitLab {
		return unsupportedActionsProvider(actionsConfigProvider(slot.Config))
	}
	target, err := slot.Config.Actions.ProviderTarget().Canonical()
	if err != nil || target.Provider != actions.ProviderGitLab || native.SchemaVersion != 1 || native.URL != target.GitLab.URL || native.Name != "hakopod-"+slot.ID || native.RunnerID != slot.ProviderRunnerID || native.TimeoutSeconds != slot.Config.Actions.TimeoutMinutes*60 || !strings.HasPrefix(native.Token, "glrt-") || len(native.Token) < 16 || len(native.Token) > 4096 || strings.IndexFunc(native.Token, func(r rune) bool { return r <= ' ' || r >= 127 }) >= 0 {
		return errors.New("GitLab private registration does not match its original slot")
	}
	if native.ExpiresAt != nil && !native.ExpiresAt.After(time.Now()) {
		return errors.New("GitLab private registration has expired")
	}
	return nil
}

func (s *Server) openGitLabSlot(slot store.ActionsSlot) (gitlabSlotRegistration, error) {
	var saved gitlabSlotRegistration
	key := s.authEncryptionKey()
	defer clear(key)
	binding, err := slot.RegistrationBinding()
	if err != nil {
		return saved, err
	}
	registration, err := actions.OpenProviderRegistration(key, binding, slot.EncryptedRegistration)
	if err != nil {
		return saved, err
	}
	defer clear(registration.ManagerConfig)
	defer clear(registration.CleanupCredential)
	if err = decodeGitLabPrivate(registration.ManagerConfig, &saved); err != nil || saved.Version != 1 {
		return saved, errors.New("GitLab saved runtime registration is invalid")
	}
	if err = validateGitLabPrivate(slot, saved.Registration); err != nil {
		return saved, err
	}
	return saved, cluster.ValidateGitLabActionsRuntime(saved.Runtime)
}

func (s *Server) rereadGitLabSlot(ctx context.Context, slot store.ActionsSlot) (store.ActionsSlot, error) {
	slots, err := s.Store.ActionsSlots(ctx, slot.ApplicationID, slot.Service)
	if err != nil {
		return store.ActionsSlot{}, err
	}
	for _, current := range slots {
		if current.ID == slot.ID && reflect.DeepEqual(current.Config, slot.Config) {
			return current, nil
		}
	}
	return store.ActionsSlot{}, fmt.Errorf("%w: GitLab runner slot changed", store.ErrConflict)
}

func (s *Server) launchSavedGitLabSlot(ctx context.Context, t cluster.Target, slot store.ActionsSlot, saved gitlabSlotRegistration) error {
	if slot.Phase != "starting" || slot.ManagerLaunchAttempted {
		return fmt.Errorf("%w: GitLab manager launch already attempted", store.ErrConflict)
	}
	runtime, ok := s.actionRuntime().(gitlabActionsRuntime)
	if !ok {
		return unsupportedActionsProvider(actions.ProviderGitLab)
	}
	if err := actionsFence(ctx, t); err != nil {
		return err
	}
	if err := runtime.SaveGitLabActionsConfig(ctx, t, slot.Service, slot.ID, slot.Config, saved.Registration, saved.Runtime); err != nil {
		return err
	}
	if err := actionsFence(ctx, t); err != nil {
		return err
	}
	// Do not start after any uncertain database response. If the write committed,
	// the next pass retires a missing manager instead of replaying its token.
	launched, err := s.gitlabSlotStore().MarkActionsProviderLaunch(ctx, slot)
	if err != nil {
		return err
	}
	if err = actionsFence(ctx, t); err != nil {
		return err
	}
	if _, err = s.Store.EnsureActionsNativeHistory(ctx, launched); err != nil {
		return err
	}
	if err := actionsFence(ctx, t); err != nil {
		return err
	}
	return runtime.StartGitLabActionsPod(ctx, t, slot.Service, slot.ID, slot.Config, saved.Registration, saved.Runtime)
}

func (s *Server) startGitLabActionsSlot(ctx context.Context, t cluster.Target, p store.ActionsPool) error {
	held, err := s.Store.ActionsProviderPoolHeld(ctx, p.ApplicationID, p.Service)
	if err != nil {
		return err
	}
	if held {
		return &actions.RunnerReuseError{}
	}
	if err := s.Store.RequireActions(ctx, p.Project, p.Environment); err != nil {
		return err
	}
	if s.actionsGitLabClient == nil {
		return unsupportedActionsProvider(actions.ProviderGitLab)
	}
	runtime, err := s.gitlabRuntimeConfig(ctx, t, p.Config)
	if err != nil {
		return err
	}
	key := s.authEncryptionKey()
	defer clear(key)
	if len(key) != 32 {
		return errors.New("GitLab registration requires the persistent authentication encryption key")
	}
	if _, ok := s.actionRuntime().(gitlabActionsRuntime); !ok {
		return unsupportedActionsProvider(actions.ProviderGitLab)
	}
	if err = s.actionRuntime().ActionsPoolAvailable(ctx, t, p.Config); err != nil {
		return err
	}
	client, err := s.gitlabRunnerClient(ctx, t, p.Config)
	if err != nil {
		return err
	}
	if err = actionsFence(ctx, t); err != nil {
		return err
	}
	slot, err := s.Store.NewActionsSlot(ctx, p)
	if err != nil {
		return err
	}
	if err = actionsFence(ctx, t); err != nil {
		return err
	}
	registration, err := client.Register(ctx, slot.Config.Actions.ProviderTarget(), "hakopod-"+slot.ID, slot.Config.Actions.Labels)
	if err != nil {
		return err
	}
	defer clear(registration.ManagerConfig)
	defer clear(registration.CleanupCredential)
	if err = registration.Validate("hakopod-" + slot.ID); err != nil {
		return err
	}
	intent := slot
	slot.ProviderRunnerID = registration.Runner.ID
	var native actions.GitLabManagerConfig
	if err = decodeGitLabPrivate(registration.ManagerConfig, &native); err != nil {
		return err
	}
	if err = validateGitLabPrivate(slot, native); err != nil {
		return err
	}
	saved := gitlabSlotRegistration{Version: 1, Registration: native, Runtime: runtime}
	encoded, err := json.Marshal(saved)
	if err != nil {
		return errors.New("GitLab private registration could not be encoded")
	}
	defer clear(encoded)
	registration.ManagerConfig = encoded
	binding, err := slot.RegistrationBinding()
	if err != nil {
		return err
	}
	sealed, err := actions.SealProviderRegistration(key, binding, registration)
	if err != nil {
		return err
	}
	if err = actionsFence(ctx, t); err != nil {
		return err
	}
	saveErr := s.gitlabSlotStore().SaveActionsProviderRegistration(ctx, intent, slot.ProviderRunnerID, sealed)
	// Both successful and uncertain writes reread the authoritative row. Never
	// overwrite a registration or re-register a slot after a lost write response.
	current, err := s.rereadGitLabSlot(ctx, intent)
	if err != nil {
		return err
	}
	if current.Phase != "starting" || current.ProviderRunnerID != slot.ProviderRunnerID || !bytes.Equal(current.EncryptedRegistration, sealed) {
		if saveErr != nil {
			return saveErr
		}
		return fmt.Errorf("%w: GitLab registration changed", store.ErrConflict)
	}
	return s.launchSavedGitLabSlot(ctx, actionsSlotTarget(t, current), current, saved)
}

func validateGitLabOwned(runners []actions.ProviderRunner, slot store.ActionsSlot) error {
	if len(runners) > 2 {
		return &actions.GitLabError{Kind: "bounds"}
	}
	seen := map[string]bool{}
	for _, runner := range runners {
		if !actions.ValidProviderID(runner.ID) || runner.Name != "hakopod-"+slot.ID || seen[runner.ID] {
			return &actions.GitLabError{Kind: "identity"}
		}
		seen[runner.ID] = true
	}
	return nil
}

func (s *Server) cleanupGitLabActionsSlot(ctx context.Context, t cluster.Target, slot store.ActionsSlot, client gitlabRunnerProvider) (bool, error) {
	var err error
	if err = actionsFence(ctx, t); err != nil {
		return false, err
	}
	if slot.Phase != "cleanup" {
		slot, err = s.gitlabSlotStore().MarkActionsProviderCleanup(ctx, slot)
		if err != nil {
			return false, err
		}
	}
	if slot.ManagerLaunchAttempted && !slot.ProviderCleanupConfirmed {
		ready, e := s.prepareGitLabJobCleanup(ctx, t, slot, client)
		if e != nil || !ready {
			return false, e
		}
	}
	if !slot.ProviderCleanupConfirmed {
		if err = actionsFence(ctx, t); err != nil {
			return false, err
		}
		if slot.ProviderCleanupRunnerID != "" {
			// The adapter pauses acquisition, verifies owned identity and idle state,
			// then deletes. A busy or unknown runner remains paused for the next pass.
			err = client.DeleteOwned(ctx, slot.Config.Actions.ProviderTarget(), slot.ProviderCleanupRunnerID, "hakopod-"+slot.ID)
			if err != nil && !errors.Is(err, actions.ErrRunnerAbsent) {
				var failure *actions.GitLabError
				if errors.As(err, &failure) && failure.Kind == "busy" {
					if err = actionsFence(ctx, t); err != nil {
						return false, err
					}
					// Rotate this completed observation behind older slots. Otherwise
					// a busy runner can consume every shared-budget cleanup attempt.
					_, err = s.gitlabSlotStore().AdvanceActionsProviderCleanup(ctx, slot, slot.ProviderCleanupRunnerID, false)
					return false, err
				}
				return false, err
			}
			if err = actionsFence(ctx, t); err != nil {
				return false, err
			}
			_, err = s.gitlabSlotStore().AdvanceActionsProviderCleanup(ctx, slot, "", false)
			return false, err
		}
		runners, e := client.FindOwned(ctx, slot.Config.Actions.ProviderTarget(), "hakopod-"+slot.ID)
		if e != nil {
			return false, e
		}
		if err = validateGitLabOwned(runners, slot); err != nil {
			return false, err
		}
		if err = actionsFence(ctx, t); err != nil {
			return false, err
		}
		if len(runners) > 0 {
			_, err = s.gitlabSlotStore().AdvanceActionsProviderCleanup(ctx, slot, runners[0].ID, false)
			return false, err
		}
		slot, err = s.gitlabSlotStore().AdvanceActionsProviderCleanup(ctx, slot, "", true)
		if err != nil {
			return false, err
		}
	}
	if err = actionsFence(ctx, t); err != nil {
		return false, err
	}
	gone, err := s.actionRuntime().DeleteActionsPod(ctx, t, slot.ID)
	if err != nil || !gone {
		return false, err
	}
	if err = actionsFence(ctx, t); err != nil {
		return false, err
	}
	runtime, ok := s.actionRuntime().(gitlabActionsRuntime)
	if !ok {
		return false, unsupportedActionsProvider(actions.ProviderGitLab)
	}
	if err = runtime.DeleteGitLabActionsConfig(ctx, t, slot.ID); err != nil {
		return false, err
	}
	if err = actionsFence(ctx, t); err != nil {
		return false, err
	}
	if err = runtime.DeleteGitLabActionsPolicy(ctx, t, slot.ID); err != nil {
		return false, err
	}
	if err = actionsFence(ctx, t); err != nil {
		return false, err
	}
	return true, s.gitlabSlotStore().DeleteActionsProviderSlot(ctx, slot)
}

func (s *Server) reconcileGitLabActionsPool(ctx context.Context, t cluster.Target, p store.ActionsPool) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := actionsFence(ctx, t); err != nil {
		return err
	}
	slots, err := s.Store.ActionsSlots(ctx, p.ApplicationID, p.Service)
	if err != nil {
		return err
	}
	for _, slot := range slots {
		if actionsConfigProvider(slot.Config) != actions.ProviderGitLab {
			return &actions.UnsupportedProviderError{Provider: actionsConfigProvider(slot.Config), Reason: "preserve existing runners until their original provider cleanup completes"}
		}
	}
	sort.SliceStable(slots, func(i, j int) bool {
		a, b := slots[i], slots[j]
		cleanupA, cleanupB := a.Phase == "intent" || a.Phase == "cleanup", b.Phase == "intent" || b.Phase == "cleanup"
		if cleanupA != cleanupB {
			return cleanupA
		}
		return a.UpdatedAt.Before(b.UpdatedAt)
	})
	accessErr := s.Store.RequireActions(ctx, p.Project, p.Environment)
	if accessErr != nil && !errors.Is(accessErr, store.ErrLicenseRequired) {
		return accessErr
	}
	desired := int(p.Config.Replicas)
	held, err := s.Store.ActionsProviderPoolHeld(ctx, p.ApplicationID, p.Service)
	if err != nil {
		return err
	}
	if p.Removed || p.Config.Suspended || accessErr != nil || held {
		desired = 0
	}
	remaining, retained := len(slots), 0
	cleanupPending := false
	for _, slot := range slots {
		if slot.Phase == "intent" || slot.Phase == "cleanup" || !sameActionsConfig(slot.Config, p.Config) {
			cleanupPending = true
		}
	}
	// Fill an empty slot before polling an entire shared-credential inventory;
	// otherwise large pools can exhaust their budget before ever scaling up.
	if remaining < desired && !cleanupPending {
		if err = s.startGitLabActionsSlot(ctx, t, p); err != nil {
			return err
		}
		requestActionsWarmup(ctx)
		return s.Store.ActionsMessage(ctx, p, "")
	}
	cleanupPending = false
	for _, slot := range slots {
		target := actionsSlotTarget(t, slot)
		if err = actionsFence(ctx, target); err != nil {
			return err
		}
		var client gitlabRunnerProvider
		if !slot.ProviderCleanupConfirmed {
			client, err = s.gitlabRunnerClient(ctx, target, slot.Config)
			if err != nil {
				return err
			}
		}
		phase, e := s.actionRuntime().ActionsPodPhase(ctx, target, slot.ID)
		if e != nil {
			return e
		}
		retire := desired == 0 || !sameActionsConfig(slot.Config, p.Config) || retained >= desired || slot.Phase == "intent" || slot.Phase == "cleanup" || phase == "Succeeded" || phase == "Failed" || phase == "deleting" || (phase == "missing" && slot.ManagerLaunchAttempted) || (phase != "missing" && !slot.ManagerLaunchAttempted)
		var saved gitlabSlotRegistration
		if !retire {
			saved, err = s.openGitLabSlot(slot)
			if err != nil {
				// Expired or unreadable registrations cannot be relaunched. Cleanup
				// uses the original management credential, never the encrypted token.
				retire = true
			} else {
				current, e := s.gitlabRuntimeConfig(ctx, target, slot.Config)
				if e != nil {
					return e
				}
				retire = !reflect.DeepEqual(current, saved.Runtime)
			}
		}
		if retire {
			gone, e := s.cleanupGitLabActionsSlot(ctx, target, slot, client)
			if e != nil {
				return e
			}
			if gone {
				remaining--
			} else {
				cleanupPending = true
			}
			continue
		}
		if err = actionsFence(ctx, target); err != nil {
			return err
		}
		s.observeGitLabActionsJob(ctx, target, slot)
		runner, e := client.GetOwned(ctx, slot.Config.Actions.ProviderTarget(), slot.ProviderRunnerID, "hakopod-"+slot.ID)
		if errors.Is(e, actions.ErrRunnerAbsent) {
			if err = actionsFence(ctx, target); err != nil {
				return err
			}
			// Begin retirement; its exact-name scan and deletion run in later passes.
			_, err = s.gitlabSlotStore().MarkActionsProviderCleanup(ctx, slot)
			if err != nil {
				return err
			}
			cleanupPending = true
			continue
		}
		if e != nil {
			return e
		}
		if runner.ID != slot.ProviderRunnerID || runner.Name != "hakopod-"+slot.ID {
			return &actions.GitLabError{Kind: "identity"}
		}
		if phase == "missing" {
			if err = s.launchSavedGitLabSlot(ctx, target, slot, saved); err != nil {
				return err
			}
			requestActionsWarmup(ctx)
		} else {
			state := "starting"
			if runner.Status == "online" && phase == "Running" {
				state = "online"
				if runner.Busy {
					state = "busy"
				}
			}
			if err = actionsFence(ctx, target); err != nil {
				return err
			}
			if err = s.Store.ObserveActionsProviderSlot(ctx, slot, runner, state); err != nil {
				return err
			}
			if state == "starting" {
				requestActionsWarmup(ctx)
			}
		}
		retained++
	}
	if err = actionsFence(ctx, t); err != nil {
		return err
	}
	if p.Removed {
		if remaining == 0 {
			return s.Store.DeleteRetiredActionsPool(ctx, p)
		}
		return s.Store.ActionsMessage(ctx, p, "Removing runners and GitLab registrations")
	}
	if accessErr != nil {
		return s.Store.ActionsMessage(ctx, p, "Pro access is required. Busy jobs finish; no new runners are started.")
	}
	if p.Config.Suspended {
		return s.Store.ActionsMessage(ctx, p, "Paused. Busy jobs finish before their runners are removed.")
	}
	if held {
		return s.Store.ActionsMessage(ctx, p, "A one-job runner processed multiple jobs. The pool is held for operator inspection.")
	}
	// Ambiguous registrations and cleanup continue to consume pool capacity.
	// A pass creates at most one runner and never bypasses pending retirement.
	if remaining < desired && !cleanupPending {
		if err = s.startGitLabActionsSlot(ctx, t, p); err != nil {
			return err
		}
		requestActionsWarmup(ctx)
	}
	return s.Store.ActionsMessage(ctx, p, "")
}
