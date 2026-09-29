package api

import (
	"context"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"testing"
)

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
