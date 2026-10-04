package cluster

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
)

type neonDeleteIntentStore struct {
	ManagedPlatformOperationStore
	op        store.ManagedPlatformOperation
	lookup    []string
	items     []store.PlatformResourceIntent
	cancelled store.PlatformResourceIntent
	older     bool
	err       error
}

func (s *neonDeleteIntentStore) NeonTimelineDeletionIntents(_ context.Context, op store.ManagedPlatformOperation, tenant, timeline, parent string) ([]store.PlatformResourceIntent, error) {
	s.op, s.lookup = op, []string{tenant, timeline, parent}
	return s.items, s.err
}

func (s *neonDeleteIntentStore) CancelNeonTimelineDeletionIntent(_ context.Context, op store.ManagedPlatformOperation, intent store.PlatformResourceIntent) error {
	s.op, s.cancelled, s.older = op, intent, true
	return s.err
}

func (s *neonDeleteIntentStore) CancelPlatformResourceIntent(_ context.Context, op store.ManagedPlatformOperation, intent store.PlatformResourceIntent) error {
	s.op, s.cancelled, s.older = op, intent, false
	return s.err
}

func TestNeonLifecycleAdapterPreservesOriginalDeletionIntents(t *testing.T) {
	confirmed := time.Now()
	state := &neonDeleteIntentStore{items: []store.PlatformResourceIntent{{ID: "parent", PlatformID: "platform", PlatformRevision: 1, Component: "tenant", Kind: "neon_tenant", ExternalKey: "tenant", OwnerOperationID: "create", ConfirmedAt: &confirmed}}}
	op := store.ManagedPlatformOperation{ID: "delete", PlatformID: "platform", Revision: 4, Kind: "delete", Lease: "current-lease"}
	adapter := neonLifecycleAdapter{state: state, op: op}
	items, err := adapter.NeonTimelineDeletionIntents(context.Background(), "tenant", "timeline", "parent")
	if err != nil || len(items) != 1 || !items[0].Confirmed || items[0].PlatformRevision != 1 || items[0].OwnerOperationID != "create" || !reflect.DeepEqual(state.op, op) || !reflect.DeepEqual(state.lookup, []string{"tenant", "timeline", "parent"}) {
		t.Fatal("adapter lost original authority or current delete lease")
	}
	for _, revision := range []int64{1, 3} {
		intent := managedplatform.DurableResourceIntent{ID: "child", PlatformID: "platform", PlatformRevision: revision, Component: "timeline", Kind: "neon_timeline", ExternalKey: "tenant/timeline", OwnerOperationID: "create"}
		if err = adapter.Cancel(context.Background(), intent); err != nil || state.older != (revision == 1) || state.cancelled.PlatformRevision != revision || state.cancelled.OwnerOperationID != "create" || !reflect.DeepEqual(state.op, op) {
			t.Fatal("adapter changed original intent or used the wrong cancellation scope")
		}
	}
	state.err = errors.New("development fixture: lease changed")
	if _, err = adapter.NeonTimelineDeletionIntents(context.Background(), "tenant", "timeline", "parent"); !errors.Is(err, state.err) {
		t.Fatal("adapter hid lookup failure")
	}
	if err = adapter.Cancel(context.Background(), managedplatform.DurableResourceIntent{PlatformRevision: 1, Component: "timeline", Kind: "neon_timeline"}); !errors.Is(err, state.err) {
		t.Fatal("adapter hid cancellation failure")
	}
}
