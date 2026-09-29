package api

import (
	"context"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"testing"
)

func actionsRetiredHistory(t *testing.T, db *store.Store, application, service string) {
	t.Helper()
	// Artificial rename history exceeds the bounded page and sorts before runner.
	_, err := db.Pool.Exec(context.Background(), `INSERT INTO actions_pools(application_id,service,project,environment,application_name,revision,config,removed) SELECT application_id,'archived-'||n,project,environment,application_name,revision,config,true FROM actions_pools CROSS JOIN generate_series(1,201) n WHERE application_id=$1 AND service=$2`, application, service)
	if err != nil {
		t.Fatal(err)
	}
}

func TestActionsObservationAndListingSurviveRetiredHistory(t *testing.T) {
	s, _, p, _ := actionsHarness(t)
	ctx := context.Background()
	actionsRetiredHistory(t, s.Store, p.ApplicationID, p.Service)
	if _, err := s.Store.Pool.Exec(ctx, `UPDATE actions_pools SET message='Current pool' WHERE application_id=$1 AND service=$2`, p.ApplicationID, p.Service); err != nil {
		t.Fatal(err)
	}
	result, err := s.observeActions(ctx, actionsTarget(p), p.Service, p.Config)
	if err != nil || result.Status == "missing" || result.Message != "Current pool" {
		t.Fatal("retired history hid the current service observation", result, err)
	}
	pools, err := s.Store.ActionsPools(ctx, p.ApplicationID)
	if err != nil || len(pools) != 200 || pools[0].Service != p.Service || pools[0].Removed {
		t.Fatal("retired history hid the active pool or exceeded the list bound", len(pools), err)
	}
	page, err := s.Store.ActionsPoolsPage(ctx, p.ApplicationID, "", "")
	if err != nil || len(page) != 200 || page[0].Service == p.Service {
		t.Fatal("application listing changed fleet keyset order", len(page), err)
	}
}

func (f *actionsFake) ActionsPodImages(_ context.Context, _ cluster.Target, _ string) ([]string, error) {
	for _, exists := range f.pods {
		if exists {
			return []string{spec.ActionsRunnerImage}, nil
		}
	}
	return []string{}, nil
}

func TestActionsImageObservationDoesNotUseIntentOrCleanupConfig(t *testing.T) {
	s, f, p, _ := actionsHarness(t)
	ctx := context.Background()
	// Registration intent is durable before the first pod exists.
	if _, err := s.Store.NewActionsSlot(ctx, p); err != nil {
		t.Fatal(err)
	}
	result, err := s.observeActions(ctx, actionsTarget(p), p.Service, p.Config)
	if err != nil {
		t.Fatal(err)
	}
	if result.Image != "" || len(result.Images) != 0 {
		t.Fatal("intent reported an image without a pod", result)
	}
	// A leftover cleanup record with no container must stay absent as well.
	slots, err := s.Store.ActionsSlots(ctx, p.ApplicationID, p.Service)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.UpdateActionsSlot(ctx, slots[0].ID, 0, "cleanup"); err != nil {
		t.Fatal(err)
	}
	f.pods = map[string]bool{}
	result, err = s.observeActions(ctx, actionsTarget(p), p.Service, p.Config)
	if err != nil || result.Image != "" || len(result.Images) != 0 {
		t.Fatal("cleanup reported a removed image", result, err)
	}
}
