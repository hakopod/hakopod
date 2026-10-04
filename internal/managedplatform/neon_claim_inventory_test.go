package managedplatform

import (
	"context"
	"strings"
	"testing"
)

type effectiveNeonClaimFixture struct {
	*durableNeonFixture
	priorClaims   []DurableResourceClaim
	currentClaims []DurableResourceClaim
}

func (f *effectiveNeonClaimFixture) Claims(_ context.Context, revision int64) ([]DurableResourceClaim, error) {
	if revision == f.op.Revision {
		return f.currentClaims, nil
	}
	return f.priorClaims, nil
}

func TestDurableNeonClaimInventoryPreservesBoundedPriorAuthority(t *testing.T) {
	op := DurableOperation{ID: testOperation, PlatformID: strings.Repeat("a", 32), Revision: 4, Kind: "delete"}
	provider := DurableResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 2, Component: "tenant", Kind: "neon_tenant", ResourceID: "development-provider-identity", ImmutableGeneration: 1, OwnerOperationID: strings.Repeat("b", 32)}
	kubernetes := DurableResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 1, Component: "deployment.neon-storage-controller", Kind: "runtime_component", ResourceID: "development-workload-uid", ImmutableGeneration: 1, OwnerOperationID: strings.Repeat("b", 32)}
	current := DurableResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 4, Component: "timeline", Kind: "neon_timeline", ResourceID: "development-timeline-identity", ImmutableGeneration: 1, OwnerOperationID: op.ID}
	for _, test := range []struct {
		name      string
		mutate    func(*effectiveNeonClaimFixture)
		wantError bool
	}{
		{name: "mixed prior generations"},
		{name: "oldest prior provider", mutate: func(f *effectiveNeonClaimFixture) { f.priorClaims[0].PlatformRevision = 1 }},
		{name: "zero prior revision", mutate: func(f *effectiveNeonClaimFixture) { f.priorClaims[0].PlatformRevision = 0 }, wantError: true},
		{name: "current revision in prior inventory", mutate: func(f *effectiveNeonClaimFixture) { f.priorClaims[0].PlatformRevision = 4 }, wantError: true},
		{name: "future prior revision", mutate: func(f *effectiveNeonClaimFixture) { f.priorClaims[0].PlatformRevision = 5 }, wantError: true},
		{name: "foreign prior platform", mutate: func(f *effectiveNeonClaimFixture) { f.priorClaims[0].PlatformID = strings.Repeat("f", 32) }, wantError: true},
		{name: "foreign Kubernetes platform", mutate: func(f *effectiveNeonClaimFixture) { f.priorClaims[1].PlatformID = strings.Repeat("f", 32) }, wantError: true},
		{name: "invalid generation", mutate: func(f *effectiveNeonClaimFixture) { f.priorClaims[0].ImmutableGeneration = 0 }, wantError: true},
		{name: "missing identity", mutate: func(f *effectiveNeonClaimFixture) { f.priorClaims[0].ResourceID = "" }, wantError: true},
		{name: "duplicate prior provider", mutate: func(f *effectiveNeonClaimFixture) { f.priorClaims = append(f.priorClaims, f.priorClaims[0]) }, wantError: true},
		{name: "older current claim", mutate: func(f *effectiveNeonClaimFixture) { f.currentClaims[0].PlatformRevision = 3 }, wantError: true},
		{name: "future current claim", mutate: func(f *effectiveNeonClaimFixture) { f.currentClaims[0].PlatformRevision = 5 }, wantError: true},
		{name: "foreign current owner", mutate: func(f *effectiveNeonClaimFixture) { f.currentClaims[0].OwnerOperationID = strings.Repeat("e", 32) }, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			fixture := &effectiveNeonClaimFixture{durableNeonFixture: &durableNeonFixture{op: op, events: &events}, priorClaims: []DurableResourceClaim{provider, kubernetes}, currentClaims: []DurableResourceClaim{current}}
			if test.mutate != nil {
				test.mutate(fixture)
			}
			runtime := &DurableNeonRuntime{lifecycle: fixture}
			prior, active, err := runtime.claims(context.Background())
			if (err != nil) != test.wantError {
				t.Fatalf("unexpected inventory validation: %v", err)
			}
			if !test.wantError && (len(prior) != 1 || prior["tenant"] != fixture.priorClaims[0] || len(active) != 1 || active["timeline"] != current) {
				t.Fatal("provider inventory lost original authority or included Kubernetes claims")
			}
			if len(events) != 0 {
				t.Fatal("inventory inspection mutated durable state")
			}
		})
	}
}
