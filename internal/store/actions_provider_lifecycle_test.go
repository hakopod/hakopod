package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestProviderLifecycleCASFencesLaunchCleanupAndDeletion(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	app := Application{ID: NewID(), Project: "development-fixture", Environment: "development", Name: "native"}
	config := spec.Service{Actions: &spec.Actions{Provider: actions.ProviderGitLab, GitLab: &actions.GitLabTarget{URL: "https://gitlab.com", ProjectID: 12}, Credential: "fixture-management"}}
	if err := db.SyncActions(ctx, app, spec.Application{Name: app.Name, Services: map[string]spec.Service{"runner": config}}, 1); err != nil {
		t.Fatal(err)
	}
	pool, err := db.ActionsPool(ctx, app.ID, "runner")
	if err != nil || pool == nil {
		t.Fatal(err)
	}
	slot, err := db.NewActionsSlot(ctx, *pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkActionsProviderLaunch(ctx, slot); !errors.Is(err, ErrConflict) {
		t.Fatal("intent launched without registration", err)
	}
	// Ciphertext shape is synthetic in this store-only test; crypto binding has
	// separate authenticated-envelope tests.
	if err = db.SaveActionsProviderRegistration(ctx, slot, "41", bytes.Repeat([]byte{9}, 32)); err != nil {
		t.Fatal(err)
	}
	slots, err := db.ActionsSlots(ctx, app.ID, "runner")
	if err != nil || len(slots) != 1 {
		t.Fatal(err)
	}
	slot = slots[0]
	launched, err := db.MarkActionsProviderLaunch(ctx, slot)
	if err != nil || !launched.ManagerLaunchAttempted {
		t.Fatal("launch fence missing", err)
	}
	if _, err = db.MarkActionsProviderLaunch(ctx, slot); !errors.Is(err, ErrConflict) {
		t.Fatal("stale launch replayed", err)
	}
	if _, err = db.MarkActionsProviderLaunch(ctx, launched); !errors.Is(err, ErrConflict) {
		t.Fatal("launch marker was reused", err)
	}
	cleanup, err := db.MarkActionsProviderCleanup(ctx, launched)
	if err != nil || cleanup.ProviderCleanupRunnerID != "41" || !cleanup.ManagerLaunchAttempted {
		t.Fatal("cleanup lost original launch identity", err)
	}
	if _, err = db.MarkActionsProviderCleanup(ctx, launched); !errors.Is(err, ErrConflict) {
		t.Fatal("stale cleanup overwrote cursor", err)
	}
	if err = db.DeleteActionsProviderSlot(ctx, cleanup); !errors.Is(err, ErrConflict) {
		t.Fatal("unconfirmed cleanup deleted slot", err)
	}
	deleted, err := db.AdvanceActionsProviderCleanup(ctx, cleanup, "", false)
	if err != nil || deleted.ProviderRunnerID != "41" {
		t.Fatal("cleanup changed stable provider identity", err)
	}
	if _, err = db.AdvanceActionsProviderCleanup(ctx, cleanup, "", false); !errors.Is(err, ErrConflict) {
		t.Fatal("stale provider result overwrote cleanup", err)
	}
	confirmed, err := db.AdvanceActionsProviderCleanup(ctx, deleted, "", true)
	if err != nil || !confirmed.ProviderCleanupConfirmed {
		t.Fatal("absence not recorded", err)
	}
	wrong := confirmed
	wrong.ApplicationID = NewID()
	if err = db.DeleteActionsProviderSlot(ctx, wrong); !errors.Is(err, ErrConflict) {
		t.Fatal("cleanup crossed application", err)
	}
	public, err := json.Marshal(confirmed)
	if err != nil || bytes.Contains(public, []byte("manager_launch")) || bytes.Contains(public, []byte("provider_cleanup")) || bytes.Contains(public, []byte("registration")) {
		t.Fatal("private lifecycle fields exposed", err)
	}
	if err = db.DeleteActionsProviderSlot(ctx, confirmed); err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteActionsProviderSlot(ctx, confirmed); !errors.Is(err, ErrConflict) {
		t.Fatal("stale deletion was accepted", err)
	}
}
