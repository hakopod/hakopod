package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
)

type neonTimelineStateFixture struct {
	input store.NeonControllerContext
}

func (f *neonTimelineStateFixture) WithNeonControllerState(_ context.Context, platformID string, revision int64, apply func(store.NeonControllerContext) (managedplatform.NeonControllerState, bool, error)) (bool, error) {
	if platformID != f.input.Operation.PlatformID || revision != f.input.Operation.Revision {
		return false, store.ErrConflict
	}
	next, applied, err := apply(f.input)
	if err == nil {
		f.input.State = next
	}
	return applied, err
}

func (f *neonTimelineStateFixture) NeonControllerState(context.Context, string, int64) (managedplatform.NeonControllerState, error) {
	return f.input.State, nil
}

func TestNeonInitialRoutingSerializesWithCallbacksAndConfirmedClaim(t *testing.T) {
	for _, scenario := range []string{"initial", "replay", "newer callback", "same generation conflict", "newer observation", "missing claim", "duplicate claim", "changed claim", "changed operation", "changed revision", "foreign membership", "foreign tenant", "restored identity"} {
		t.Run(scenario, func(t *testing.T) {
			platform := strings.Repeat("a", 32)
			op := store.ManagedPlatformOperation{ID: strings.Repeat("b", 32), PlatformID: platform, Revision: 1, Kind: "create", Spec: managedplatform.Spec{Neon: &managedplatform.NeonConfig{Pageservers: 2}}}
			tenant, timeline := neonDeterministicID(platform, "tenant"), neonDeterministicID(platform, "timeline")
			input := store.NeonControllerContext{Operation: op, PendingCompute: true, State: managedplatform.NeonControllerState{Attach: &managedplatform.NeonAttachNotification{TenantID: tenant, Shards: []managedplatform.NeonAttachShard{{NodeID: 1}}}}}
			if scenario == "restored identity" {
				tenant, timeline = strings.Repeat("c", 32), strings.Repeat("d", 32)
				input.Binding = store.NeonRecoveryBinding{OperationID: strings.Repeat("e", 32), TargetPlatformID: platform, TargetRevision: 1, TenantID: tenant, TimelineID: timeline}
			}
			hosts := []string{"sk-0.test", "sk-1.test", "sk-2.test"}
			routing := managedplatform.NeonSafekeeperNotification{TenantID: tenant, TimelineID: timeline, Generation: 1, Safekeepers: []managedplatform.NeonSafekeeperMember{{ID: 3, Hostname: &hosts[2]}, {ID: 1, Hostname: &hosts[0]}, {ID: 2, Hostname: &hosts[1]}}}
			sum := sha256.Sum256([]byte(tenant + "\x00" + timeline + "\x001@sk-0.test\x002@sk-1.test\x003@sk-2.test"))
			claim := managedplatform.DurableResourceClaim{PlatformID: platform, PlatformRevision: 1, Component: "timeline", Kind: "neon_timeline", ResourceID: hex.EncodeToString(sum[:]) + ":" + strings.Repeat("f", 32), ImmutableGeneration: 1, OwnerOperationID: op.ID}
			observation := managedplatform.NeonTimelineRoutingObservation{Claim: claim, Routing: routing}
			input.Claims = []store.PlatformResourceClaim{{PlatformID: claim.PlatformID, PlatformRevision: claim.PlatformRevision, Component: claim.Component, Kind: claim.Kind, ResourceID: claim.ResourceID, ImmutableGeneration: claim.ImmutableGeneration, OwnerOperationID: claim.OwnerOperationID}}
			if scenario == "replay" || scenario == "newer callback" || scenario == "same generation conflict" || scenario == "newer observation" {
				existing := routing
				existing.Safekeepers = []managedplatform.NeonSafekeeperMember{{ID: 1}, {ID: 2}, {ID: 3}}
				input.State.Safekeepers = &existing
			}
			switch scenario {
			case "newer callback":
				input.State.Safekeepers.Generation = 8
			case "same generation conflict":
				foreign := "foreign.test"
				input.State.Safekeepers.Safekeepers[0].Hostname = &foreign
			case "newer observation":
				observation.Routing.Generation = 2
				observation.Claim.ImmutableGeneration = 2
				input.Claims[0].ImmutableGeneration = 2
			case "missing claim":
				input.Claims = nil
			case "duplicate claim":
				input.Claims = append(input.Claims, input.Claims[0])
			case "changed claim":
				input.Claims[0].ResourceID = strings.Repeat("0", 64) + ":" + strings.Repeat("f", 32)
			case "changed operation":
				input.Operation.ID = strings.Repeat("0", 32)
			case "changed revision":
				observation.Claim.PlatformRevision = 2
			case "foreign membership":
				foreign := "foreign.test"
				observation.Routing.Safekeepers[0].Hostname = &foreign
			case "foreign tenant":
				observation.Routing.TenantID = strings.Repeat("0", 32)
			}
			fixture := &neonTimelineStateFixture{input: input}
			before, err := json.Marshal(fixture.input.State)
			if err != nil {
				t.Fatal(err)
			}
			err = neonControllerTimelineObserver(fixture, op)(context.Background(), observation)
			success := scenario == "initial" || scenario == "replay" || scenario == "newer callback" || scenario == "restored identity"
			if success != (err == nil) || !success && !errors.Is(err, store.ErrConflict) {
				t.Fatalf("unexpected bootstrap result: %v", err)
			}
			if !success || scenario == "replay" || scenario == "newer callback" {
				after, _ := json.Marshal(fixture.input.State)
				if string(before) != string(after) {
					t.Fatal("bootstrap replaced callback state or persisted a rejected observation")
				}
			}
			if scenario == "initial" || scenario == "restored identity" {
				if !reflect.DeepEqual(fixture.input.State.Safekeepers, &routing) {
					t.Fatal("initial provider membership was not persisted")
				}
			}
			if scenario == "restored identity" && fixture.input.State.Attach != nil {
				t.Fatal("restored routing retained the previous tenant attachment")
			}
			if scenario == "initial" {
				newer := routing
				newer.Generation = 2
				fixture.input.State, err = mergeNeonControllerState(fixture.input.State, tenant, timeline, 2, nil, &newer)
				if err != nil {
					t.Fatal(err)
				}
				raw := json.RawMessage(`{"spec":{"tenant_id":"` + tenant + `","timeline_id":"` + timeline + `","cluster":{}}}`)
				bound, err := neonControllerComputeResolver(fixture, platform, 1, 2)(context.Background(), "compute-0", raw)
				if err != nil || !strings.Contains(string(bound), `"safekeepers_generation":2`) {
					t.Fatalf("later callback did not replace initial routing: %v", err)
				}
			}
		})
	}
}
