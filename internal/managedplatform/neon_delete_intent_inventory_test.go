package managedplatform

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type neonDeletionInventoryFixture struct {
	*durableNeonFixture
	items  []DurableResourceIntent
	err    error
	lookup []string
}

func (f *neonDeletionInventoryFixture) NeonTimelineDeletionIntents(_ context.Context, tenantID, timelineID, parentToken string) ([]DurableResourceIntent, error) {
	f.lookup = []string{tenantID, timelineID, parentToken}
	return f.items, f.err
}

func TestDurableNeonScopedDeletionIntentInventory(t *testing.T) {
	for _, test := range []struct {
		name        string
		mutate      func([]DurableResourceIntent) []DurableResourceIntent
		emptyToken  bool
		lookupError bool
		wantError   bool
		wantPending bool
	}{
		{name: "original creation survives multiple delete revisions", wantPending: true},
		{name: "released child retains only original parent", mutate: func(items []DurableResourceIntent) []DurableResourceIntent { return items[:1] }},
		{name: "registration only has no parent or timeline", emptyToken: true, mutate: func([]DurableResourceIntent) []DurableResourceIntent { return nil }},
		{name: "missing token cannot hide older pending child", emptyToken: true, wantError: true},
		{name: "lookup lease failure propagates", lookupError: true, wantError: true},
		{name: "bounded inventory", mutate: func(items []DurableResourceIntent) []DurableResourceIntent { return append(items, items[1]) }, wantError: true},
		{name: "duplicate parents", mutate: func(items []DurableResourceIntent) []DurableResourceIntent {
			return []DurableResourceIntent{items[0], items[0]}
		}, wantError: true},
		{name: "duplicate pending children", mutate: func(items []DurableResourceIntent) []DurableResourceIntent {
			return []DurableResourceIntent{items[1], items[1]}
		}, wantError: true},
		{name: "unexpected resource", mutate: func(items []DurableResourceIntent) []DurableResourceIntent {
			items[1].Component = "compute-primary"
			return items
		}, wantError: true},
		{name: "current revision pair", mutate: func(items []DurableResourceIntent) []DurableResourceIntent {
			items[0].PlatformRevision = 4
			items[1].PlatformRevision = 4
			return items
		}, wantError: true},
		{name: "zero revision pair", mutate: func(items []DurableResourceIntent) []DurableResourceIntent {
			items[0].PlatformRevision = 0
			items[1].PlatformRevision = 0
			return items
		}, wantError: true},
		{name: "future revision pair", mutate: func(items []DurableResourceIntent) []DurableResourceIntent {
			items[0].PlatformRevision = 5
			items[1].PlatformRevision = 5
			return items
		}, wantError: true},
		{name: "current delete owner pair", mutate: func(items []DurableResourceIntent) []DurableResourceIntent {
			items[0].OwnerOperationID = testOperation
			items[1].OwnerOperationID = testOperation
			return items
		}, wantError: true},
		{name: "parent alone still requires confirmation", mutate: func(items []DurableResourceIntent) []DurableResourceIntent {
			items[0].Confirmed = false
			return items[:1]
		}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			op := DurableOperation{ID: testOperation, PlatformID: strings.Repeat("a", 32), Revision: 4, Kind: "delete"}
			parentToken := fixtureIntentID("tenant")
			items := []DurableResourceIntent{
				{ID: parentToken, PlatformID: op.PlatformID, PlatformRevision: 1, Component: "tenant", Kind: "neon_tenant", ExternalKey: testTenant, OwnerOperationID: strings.Repeat("b", 32), Confirmed: true},
				{ID: fixtureIntentID("timeline"), PlatformID: op.PlatformID, PlatformRevision: 1, Component: "timeline", Kind: "neon_timeline", ExternalKey: testTenant + "/" + testTimeline, OwnerOperationID: strings.Repeat("b", 32)},
			}
			if test.mutate != nil {
				items = test.mutate(items)
			}
			if test.emptyToken {
				parentToken = ""
			}
			fixture := &neonDeletionInventoryFixture{durableNeonFixture: &durableNeonFixture{op: op}, items: items}
			if test.lookupError {
				fixture.err = errors.New("development fixture: current deletion lease changed")
			}
			runtime := &DurableNeonRuntime{lifecycle: fixture}
			intent, found, err := runtime.pendingTimelineDeletion(context.Background(), durableNeonDeleteRequest(), parentToken)
			if (err != nil) != test.wantError || found != test.wantPending {
				t.Fatalf("pending authority: found=%t error=%v", found, err)
			}
			if test.lookupError && !errors.Is(err, fixture.err) {
				t.Fatal("durable lookup error was hidden")
			}
			if found && (intent.ID != fixtureIntentID("timeline") || intent.PlatformRevision != 1 || intent.OwnerOperationID != strings.Repeat("b", 32)) {
				t.Fatal("lookup replaced original creation authority")
			}
			if fixture.intentsCalls != 0 || !reflect.DeepEqual(fixture.lookup, []string{testTenant, testTimeline, parentToken}) {
				t.Fatal("scoped lookup lost its exact parent or used ordinary creation reads")
			}
		})
	}
}
