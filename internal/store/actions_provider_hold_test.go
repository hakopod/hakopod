package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
)

func providerHoldFixture(t *testing.T, db *Store) (Principal, ActionsPool, ActionsSlot, ActionsProviderHold) {
	t.Helper()
	owner := bootstrapPrincipal(t, db)
	pool, slot, h, first := nativeHistoryFixture(t, db)
	second := first
	second.Identity.JobID += "-second"
	if err := db.HoldActionsProviderReuse(context.Background(), pool.ApplicationID, pool.Service, h, []actions.ProviderJob{first, second}); err != nil {
		t.Fatal(err)
	}
	state, err := db.ActionsProviderHold(context.Background(), owner, pool.ApplicationID, pool.Service)
	if err != nil || state.Hold == nil || state.ActiveSlots != 1 {
		t.Fatal("verified reuse did not retain a hold", err)
	}
	return owner, pool, slot, *state.Hold
}

func removeProviderHoldSlots(t *testing.T, db *Store, pool ActionsPool) {
	t.Helper()
	// This store fixture models completed cleanup; it is not provider acceptance.
	if _, err := db.Pool.Exec(context.Background(), `DELETE FROM actions_slots WHERE application_id=$1 AND service=$2`, pool.ApplicationID, pool.Service); err != nil {
		t.Fatal(err)
	}
}

func TestActionsProviderHoldRetainsOriginalEvidenceAcrossHistoryAndConfigChanges(t *testing.T) {
	db := isolatedDatabase(t)
	owner, pool, slot, hold := providerHoldFixture(t, db)
	ctx := context.Background()
	if !validActionsProviderHoldID(hold.ID) || hold.Reason != "runner_reuse" || hold.SlotID != slot.ID || hold.Provider != actions.ProviderGitLab || hold.InstanceURL != "https://gitlab.com" || len(hold.Jobs) != 2 {
		t.Fatal("hold omitted durable provider identity")
	}
	changed := *pool.Config.Actions
	changed.GitLab = &actions.GitLabTarget{URL: "https://gitlab.example.invalid/team", ProjectID: 99, TrustPolicy: "example"}
	changed.Credential, changed.JobsCredential = "replacement-manager", "replacement-reader"
	pool.Config.Actions = &changed
	if err := db.SyncActions(ctx, Application{ID: pool.ApplicationID, Project: pool.Project, Environment: pool.Environment, Name: pool.ApplicationName}, spec.Application{Name: pool.ApplicationName, Services: map[string]spec.Service{pool.Service: pool.Config}}, pool.Revision+1); err != nil {
		t.Fatal(err)
	}
	// A repeated scan cannot rotate the acknowledged incident or overwrite its
	// original evidence while an operator is inspecting it.
	h := readNativeHistory(t, db, slot)
	repeated := append([]actions.ProviderJob(nil), hold.Jobs...)
	repeated[1].Name = "later observation"
	if err := db.HoldActionsProviderReuse(ctx, pool.ApplicationID, pool.Service, h, repeated); err != nil {
		t.Fatal(err)
	}
	removeProviderHoldSlots(t, db, pool)
	if _, err := db.Pool.Exec(ctx, `DELETE FROM actions_jobs WHERE slot_id=$1`, slot.ID); err != nil {
		t.Fatal(err)
	}
	state, err := db.ActionsProviderHold(ctx, owner, pool.ApplicationID, pool.Service)
	if err != nil || state.Hold == nil || state.ActiveSlots != 0 || !reflect.DeepEqual(*state.Hold, hold) {
		t.Fatal("reconfiguration or history retention erased the original hold", err)
	}
	data := string(JSON(state))
	for _, forbidden := range []string{"original-management", "original-jobs", "replacement-manager", "replacement-reader", "provider_config"} {
		if strings.Contains(data, forbidden) {
			t.Fatal("hold leaked a credential reference")
		}
	}
	if !strings.Contains(data, `"runner_id":"90071992547409931234"`) {
		t.Fatal("hold converted an opaque provider identifier")
	}
	if _, err := db.NewActionsSlot(ctx, pool); err == nil {
		t.Fatal("retained hold allowed replenishment")
	}
}

func TestActionsProviderHoldReleaseRequiresCurrentPermissionAcknowledgementAndCleanup(t *testing.T) {
	db := isolatedDatabase(t)
	owner, pool, _, hold := providerHoldFixture(t, db)
	ctx := context.Background()
	if err := db.ReleaseActionsProviderHold(ctx, owner, pool.ApplicationID, pool.Service, hold.ID, true); !errors.Is(err, ErrConflict) {
		t.Fatal("hold released while a runner slot remained", err)
	}
	removeProviderHoldSlots(t, db, pool)
	for _, input := range []struct {
		id          string
		acknowledge bool
	}{
		{hold.ID, false}, {"", true}, {"not-a-uuid", true},
	} {
		if err := db.ReleaseActionsProviderHold(ctx, owner, pool.ApplicationID, pool.Service, input.id, input.acknowledge); !errors.Is(err, ErrInput) {
			t.Fatal("missing acknowledgement accepted", err)
		}
	}
	stale, err := newActionsProviderHoldID()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReleaseActionsProviderHold(ctx, owner, pool.ApplicationID, pool.Service, stale, true); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong incident acknowledgement accepted", err)
	}
	for _, permission := range []string{"deployments:read", "deployments:write"} {
		metadata, raw, err := db.CreateKey(ctx, owner, KeyInput{Name: permission, Project: pool.Project, Environment: pool.Environment, Application: pool.ApplicationName, Permissions: []string{permission}, ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		principal, err := db.Authenticate(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		if permission == "deployments:write" {
			if _, err := db.Pool.Exec(ctx, `UPDATE api_keys SET revoked_at=clock_timestamp() WHERE id=$1`, metadata.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.ReleaseActionsProviderHold(ctx, principal, pool.ApplicationID, pool.Service, hold.ID, true); !errors.Is(err, ErrForbidden) {
			t.Fatal("insufficient or revoked access released a hold", err)
		}
	}
	wrongScope := owner
	wrongScope.Application = "another-application"
	if _, err := db.ActionsProviderHold(ctx, wrongScope, pool.ApplicationID, pool.Service); !errors.Is(err, ErrForbidden) {
		t.Fatal("hold inspection crossed application scope", err)
	}
	if err := db.ReleaseActionsProviderHold(ctx, wrongScope, pool.ApplicationID, pool.Service, hold.ID, true); !errors.Is(err, ErrForbidden) {
		t.Fatal("hold release exceeded the request principal's application scope", err)
	}
	state, err := db.ActionsProviderHold(ctx, owner, pool.ApplicationID, pool.Service)
	if err != nil || state.Hold == nil || state.Hold.ID != hold.ID {
		t.Fatal("failed release changed the hold", err)
	}
	var audits int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='actions.provider_hold.release' AND resource=$1`, pool.ApplicationID).Scan(&audits); err != nil || audits != 0 {
		t.Fatal("failed release wrote a success audit", err)
	}
}

func TestActionsProviderHoldConcurrentReleaseIsAuditedOnceAndCannotClearNewIncident(t *testing.T) {
	db := isolatedDatabase(t)
	owner, pool, slot, hold := providerHoldFixture(t, db)
	removeProviderHoldSlots(t, db, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- db.ReleaseActionsProviderHold(ctx, owner, pool.ApplicationID, pool.Service, hold.ID, true)
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("concurrent release did not preserve a single acknowledgement")
	}
	var audits int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='actions.provider_hold.release' AND resource=$1`, pool.ApplicationID).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("release was not audited exactly once", err)
	}
	var metadata []byte
	if err := db.Pool.QueryRow(ctx, `SELECT metadata FROM audit_events WHERE action='actions.provider_hold.release' AND resource=$1`, pool.ApplicationID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	var audit struct {
		Service string              `json:"service"`
		Hold    ActionsProviderHold `json:"hold"`
	}
	if err := json.Unmarshal(metadata, &audit); err != nil || audit.Service != pool.Service || string(JSON(audit.Hold)) != string(JSON(hold)) {
		t.Fatal("release audit lost the acknowledged evidence", err)
	}
	state, err := db.ActionsProviderHold(ctx, owner, pool.ApplicationID, pool.Service)
	if err != nil || state.Hold != nil || state.ActiveSlots != 0 {
		t.Fatal("release did not atomically clear the hold", err)
	}
	if _, err := db.NewActionsSlot(ctx, pool); err != nil {
		t.Fatal("released pool cannot replenish", err)
	}
	removeProviderHoldSlots(t, db, pool)
	// A later verified detection is a new incident even if a stale operator
	// request still carries the previously acknowledged UUID.
	h := readNativeHistory(t, db, slot)
	if err := db.HoldActionsProviderReuse(ctx, pool.ApplicationID, pool.Service, h, hold.Jobs); err != nil {
		t.Fatal(err)
	}
	state, err = db.ActionsProviderHold(ctx, owner, pool.ApplicationID, pool.Service)
	if err != nil || state.Hold == nil || state.Hold.ID == hold.ID {
		t.Fatal("new incident reused an acknowledged hold ID", err)
	}
	if err := db.ReleaseActionsProviderHold(ctx, owner, pool.ApplicationID, pool.Service, hold.ID, true); !errors.Is(err, ErrConflict) {
		t.Fatal("stale acknowledgement cleared a newer hold", err)
	}
}

func TestActionsProviderHoldReleaseRollsBackWithItsAudit(t *testing.T) {
	db := isolatedDatabase(t)
	owner, pool, _, hold := providerHoldFixture(t, db)
	removeProviderHoldSlots(t, db, pool)
	ctx := context.Background()
	// Force the final mutation to fail after the audit insertion. Neither may
	// commit independently, even when PostgreSQL rejects the release update.
	if _, err := db.Pool.Exec(ctx, `ALTER TABLE actions_pools ADD CONSTRAINT development_fixture_reject_release CHECK(provider_hold_id IS NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := db.ReleaseActionsProviderHold(ctx, owner, pool.ApplicationID, pool.Service, hold.ID, true); err == nil {
		t.Fatal("fixture release unexpectedly succeeded")
	}
	state, err := db.ActionsProviderHold(ctx, owner, pool.ApplicationID, pool.Service)
	if err != nil || state.Hold == nil || state.Hold.ID != hold.ID {
		t.Fatal("failed mutation erased the hold", err)
	}
	var audits int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='actions.provider_hold.release' AND resource=$1`, pool.ApplicationID).Scan(&audits); err != nil || audits != 0 {
		t.Fatal("failed mutation committed its release audit", err)
	}
}

func TestActionsProviderHoldReleaseContentionIsBoundedBeforeDatabaseAccess(t *testing.T) {
	// A concurrent recovery receives a retryable conflict without opening a
	// transaction or consuming another database connection.
	actionsProviderHoldReleaseGate <- struct{}{}
	defer func() { <-actionsProviderHoldReleaseGate }()
	var db Store
	err := db.ReleaseActionsProviderHold(context.Background(), Principal{}, "app", "runner", "2e1c207b-c23a-4c57-8a7b-d6fa09b17053", true)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "retry shortly") {
		t.Fatal("recovery contention did not fail promptly", err)
	}
}
