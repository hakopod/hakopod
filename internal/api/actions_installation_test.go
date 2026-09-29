package api

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
)

func TestGitLabOriginalTrustExistsBeforeExternalRegistration(t *testing.T) {
	s, f, pool := gitlabLifecycleHarness(t, actionsDatabase(t))
	checked := false
	f.beforeRegister = func() {
		slots, err := s.Store.ActionsSlots(context.Background(), pool.ApplicationID, pool.Service)
		if err != nil || len(slots) != 1 || slots[0].Phase != "intent" || slots[0].ProviderRunnerID != "" || len(slots[0].EncryptedProviderIntent) == 0 || len(slots[0].EncryptedRegistration) != 0 {
			t.Fatal("registration began without durable original trust", err)
		}
		runtime, err := s.openGitLabIntent(actionsTarget(pool), slots[0])
		if err != nil || !reflect.DeepEqual(runtime, f.runtime) {
			t.Fatal("original trust was not recoverable before registration", err)
		}
		checked = true
	}
	f.registerLost = true
	if err := s.startGitLabActionsSlot(context.Background(), actionsTarget(pool), pool); err == nil {
		t.Fatal("synthetic lost response was not surfaced")
	}
	if !checked {
		t.Fatal("provider was never called")
	}
	slots, _ := s.Store.ActionsSlots(context.Background(), pool.ApplicationID, pool.Service)
	if len(slots) != 1 || len(slots[0].EncryptedProviderIntent) == 0 {
		t.Fatal("ambiguous registration lost its original trust")
	}
	original := slots[0]
	changed := original
	changed.EncryptedProviderIntent = append([]byte(nil), original.EncryptedProviderIntent...)
	changed.EncryptedProviderIntent[0] ^= 1
	if err := s.Store.SaveActionsProviderRegistration(context.Background(), changed, "1", bytes.Repeat([]byte{9}, 32)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("registration overwrote a different original intent", err)
	}
	gitlabCleaned(t, s, f, pool)
}

func TestGitLabBindingRemovalRetiresWithoutStrandingOriginalCleanup(t *testing.T) {
	s, f, pool := gitlabLifecycleHarness(t, actionsDatabase(t))
	gitlabReconcile(t, s, pool)
	if len(f.starts) != 1 {
		t.Fatal("fixture did not launch")
	}
	slotID := f.starts[0]
	s.actionsGitLabRuntime = nil
	for pass := 0; pass < 10; pass++ {
		slots, err := s.Store.ActionsSlots(context.Background(), pool.ApplicationID, pool.Service)
		if err != nil {
			t.Fatal(err)
		}
		if len(slots) == 0 {
			break
		}
		if err := s.reconcileActionsPool(context.Background(), actionsTarget(pool), pool); err != nil {
			var unsupported *actions.UnsupportedProviderError
			remaining, readErr := s.Store.ActionsSlots(context.Background(), pool.ApplicationID, pool.Service)
			// The final cleanup pass may immediately try to fill the now-empty
			// desired pool. A missing binding must reject that new registration,
			// but never interrupt cleanup of the original runner.
			if !errors.As(err, &unsupported) || readErr != nil || len(remaining) != 0 || len(f.runners)+len(f.pods)+len(f.configs)+len(f.policies) != 0 {
				t.Fatal("binding removal interrupted original cleanup", err, readErr)
			}
		}
	}
	slots, err := s.Store.ActionsSlots(context.Background(), pool.ApplicationID, pool.Service)
	if err != nil || len(slots) != 0 || len(f.runners) != 0 || len(f.pods) != 0 {
		t.Fatal("removed binding stranded cleanup", err)
	}
	history, err := s.Store.ActionsNativeHistory(context.Background(), pool.ApplicationID, pool.Service, slotID)
	if err != nil || len(history.Job.EncryptedProviderIntent) == 0 {
		t.Fatal("cleanup lost historical transport trust", err)
	}
	if bytes.Contains(history.Job.EncryptedProviderIntent, []byte(gitlabLifecycleToken)) {
		t.Fatal("registration credential entered historical trust")
	}
	original, err := s.gitlabHistoryRuntime(pool, history.Job)
	if err != nil || original == nil || !reflect.DeepEqual(*original, f.runtime) {
		t.Fatal("history no longer opens original runtime after binding removal", err)
	}
	before := f.nextID
	if err := s.startGitLabActionsSlot(context.Background(), actionsTarget(pool), pool); err == nil || f.nextID != before {
		t.Fatal("removed binding admitted another registration", err)
	}
}

func TestGitLabOriginalTrustAuthenticatesCredentialReferencesAndScope(t *testing.T) {
	s, f, pool := gitlabLifecycleHarness(t, actionsDatabase(t))
	target := actionsTarget(pool)
	slot := store.ActionsSlot{ID: store.NewID(), ApplicationID: pool.ApplicationID, Service: pool.Service, Config: pool.Config}
	var err error
	slot.EncryptedProviderIntent, err = s.sealGitLabIntent(target, slot, f.runtime)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*cluster.Target, *store.ActionsSlot){
		func(t *cluster.Target, _ *store.ActionsSlot) { t.Environment = "other" },
		func(_ *cluster.Target, v *store.ActionsSlot) {
			config := *v.Config.Actions
			config.Credential = "different-management"
			v.Config.Actions = &config
		},
		func(_ *cluster.Target, v *store.ActionsSlot) { v.Service = "different-runner" },
	} {
		changedTarget, changedSlot := target, slot
		mutate(&changedTarget, &changedSlot)
		if _, err := s.openGitLabIntent(changedTarget, changedSlot); err == nil {
			t.Fatal("original trust accepted changed scope or credential reference")
		}
	}
}
