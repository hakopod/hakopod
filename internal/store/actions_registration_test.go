package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestProviderRegistrationSurvivesRestartAndCannotMoveBetweenSlots(t *testing.T) {
	db := isolatedDatabase(t)
	principal := bootstrapPrincipal(t, db)
	ctx := context.Background()
	deployment, err := db.Accept(ctx, principal, "demo", "development", emptyTestSpec(), 0, "provider-registration-fixture")
	if err != nil {
		t.Fatal(err)
	}
	app, err := db.Application(ctx, deployment.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	// This database-only fixture bypasses deployment normalization. It never
	// registers a provider runner or enables an unqualified runtime.
	config := spec.Service{Image: "example.invalid/runner@sha256:" + strings.Repeat("a", 64), Actions: &spec.Actions{Provider: actions.ProviderGitLab, GitLab: &spec.GitLabTarget{URL: "https://gitlab.com", ProjectID: 42}, Credential: "management"}}
	if err = db.SyncActions(ctx, app, spec.Application{Name: app.Name, Services: map[string]spec.Service{"runner": config}}, 1); err != nil {
		t.Fatal(err)
	}
	pool, err := db.ActionsPool(ctx, app.ID, "runner")
	if err != nil || pool == nil {
		t.Fatal("missing fixture pool", err)
	}
	slot, err := db.NewActionsSlot(ctx, *pool)
	if err != nil {
		t.Fatal(err)
	}
	if slot.Config.Image != config.Image {
		t.Fatal("native provider slot inherited the GitHub image")
	}
	slot.ProviderRunnerID = "42"
	binding, err := slot.RegistrationBinding()
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{4}, 32)
	registration := actions.ProviderRegistration{Runner: actions.ProviderRunner{ID: "42", Name: "hakopod-" + slot.ID, Status: "starting"}, ManagerConfig: []byte("synthetic-private-manager"), CleanupCredential: []byte("synthetic-private-cleanup")}
	sealed, err := actions.SealProviderRegistration(key, binding, registration)
	if err != nil {
		t.Fatal(err)
	}
	wrong := slot
	wrong.ApplicationID = NewID()
	if err = db.SaveActionsProviderRegistration(ctx, wrong, "42", sealed); !errors.Is(err, ErrConflict) {
		t.Fatal("registration crossed application ownership", err)
	}
	if err = db.SaveActionsProviderRegistration(ctx, slot, "42", sealed); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveActionsProviderRegistration(ctx, slot, "43", sealed); !errors.Is(err, ErrConflict) {
		t.Fatal("persisted registration was overwritten", err)
	}
	loaded, err := db.ActionsSlots(ctx, app.ID, "runner")
	if err != nil || len(loaded) != 1 || loaded[0].ProviderRunnerID != "42" || loaded[0].Phase != "starting" {
		t.Fatal("registration did not survive rereading the database", err)
	}
	public, err := json.Marshal(loaded[0])
	if err != nil || bytes.Contains(public, []byte("synthetic-private")) || bytes.Contains(public, []byte("encrypted_registration")) {
		t.Fatal("registration material appeared in API output", err)
	}
	binding, _ = loaded[0].RegistrationBinding()
	opened, err := actions.OpenProviderRegistration(key, binding, loaded[0].EncryptedRegistration)
	if err != nil || !bytes.Equal(opened.ManagerConfig, registration.ManagerConfig) || !bytes.Equal(opened.CleanupCredential, registration.CleanupCredential) {
		t.Fatal("private registration could not be recovered", err)
	}
	binding.RunnerID = "43"
	if _, err = actions.OpenProviderRegistration(key, binding, loaded[0].EncryptedRegistration); err == nil {
		t.Fatal("database identity tampering bypassed registration authentication")
	}
	observed := time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)
	registration.Runner.ObservedAt = observed
	if err = db.ObserveActionsProviderSlot(ctx, loaded[0], registration.Runner, "online"); err != nil {
		t.Fatal(err)
	}
	if err = db.ObserveActionsProviderSlot(ctx, loaded[0], registration.Runner, "busy"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale reconciliation overwrote a newer observation", err)
	}
	loaded, err = db.ActionsSlots(ctx, app.ID, "runner")
	if err != nil || !loaded[0].UpdatedAt.Equal(observed) {
		t.Fatal("cached provider observation acquired a fresh timestamp", err)
	}
	if err = db.UpdateActionsSlot(ctx, slot.ID, 0, "cleanup"); err != nil {
		t.Fatal(err)
	}
	if err = db.ObserveActionsProviderSlot(ctx, loaded[0], registration.Runner, "online"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale observation revived a retiring runner", err)
	}
	if err = db.DeleteActionsSlot(ctx, slot.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err = db.ActionsSlots(ctx, app.ID, "runner")
	if err != nil || len(loaded) != 0 {
		t.Fatal("cleaned runner retained encrypted registration", err)
	}
}
