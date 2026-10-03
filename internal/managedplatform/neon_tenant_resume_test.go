package managedplatform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDurableNeonPendingTenantResumesBeforeCanonicalInspection(t *testing.T) {
	for _, scenario := range []string{"completed", "recovery generation", "missing ownership", "still applying", "foreign inspection token", "foreign replay token", "provider conflict", "no pending intent", "confirmed claim"} {
		t.Run(scenario, func(t *testing.T) {
			events := []string{}
			lifecycle := &durableNeonFixture{
				op:     DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"},
				claims: map[string]DurableResourceClaim{},
				intents: map[string]DurableResourceIntent{"tenant": {
					ID: fixtureIntentID("tenant"), PlatformID: strings.Repeat("4", 32), PlatformRevision: 1, Component: "tenant", Kind: "neon_tenant", ExternalKey: testTenant, OwnerOperationID: testOperation,
				}},
				events: &events,
			}
			if scenario == "no pending intent" {
				delete(lifecycle.intents, "tenant")
			}
			ownedID, err := encodeNeonOwnedResourceID(testTenant+"@11", fixtureIntentID("tenant"))
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "confirmed claim" {
				lifecycle.claims["tenant"] = DurableResourceClaim{PlatformID: lifecycle.op.PlatformID, PlatformRevision: 1, Component: "tenant", Kind: "neon_tenant", ResourceID: ownedID, ImmutableGeneration: 7, OwnerOperationID: testOperation}
				intent := lifecycle.intents["tenant"]
				intent.Confirmed = true
				lifecycle.intents["tenant"] = intent
			}
			request := durableNeonRequest()
			if scenario == "recovery generation" {
				request.RecoveryTenantGeneration = 6
			}
			posts := 0
			runtime := &DurableNeonRuntime{lifecycle: lifecycle, control: &NeonRuntime{
				config: NeonRuntimeConfig{StorageController: NeonControlTarget{Name: "storage", Origin: "https://storage.test", Token: "storage-secret-token"}, Computes: []NeonControlTarget{{Name: "primary", Origin: "https://compute.test", Token: "compute-secret-token"}}, RequestTimeout: time.Second, allowMissingStorageRegistrationsForTest: true},
				client: &http.Client{Transport: neonRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					status, body := http.StatusOK, ""
					switch req.Method + " " + req.URL.Path {
					case "GET /control/v1/hakopod/ownership":
						body = `{"protocol":"hakopod-ownership-v1","tenant_delete_protocol":"prepare-v1","mutations":["tenant","timeline","pageserver_registration","safekeeper_registration"]}`
					case "POST /v1/tenant":
						posts++
						if len(events) == 0 || events[len(events)-1] != "heartbeat" || req.Header.Get(neonOwnershipHeader) != fixtureIntentID("tenant") {
							t.Fatal("replay omitted its heartbeat or exact ownership token")
						}
						var got map[string]any
						if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
							t.Fatal(err)
						}
						want := map[string]any{"new_tenant_id": testTenant}
						if scenario == "recovery generation" {
							want["generation"] = float64(6)
						}
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("replay changed the original request body: %v", got)
						}
						events = append(events, "post")
						token := fixtureIntentID("tenant")
						if scenario == "foreign replay token" {
							token = strings.Repeat("f", 32)
						}
						// The provider may retain the response assembled before its
						// reconciler attached the tenant. Only inspection can confirm it.
						status, body = http.StatusCreated, `{"ownership_token":"`+token+`","shards":[{"shard_id":"`+testTenant+`","node_id":99,"generation":0}]}`
						if scenario == "provider conflict" {
							status = http.StatusConflict
						}
					case "GET /control/v1/tenant/" + testTenant:
						events = append(events, "inspect")
						if scenario != "no pending intent" && scenario != "confirmed claim" && posts != 1 {
							t.Fatal("tenant was inspected before the pending create resumed")
						}
						token, state := fixtureIntentID("tenant"), "completed"
						if scenario == "foreign inspection token" {
							token = strings.Repeat("f", 32)
						}
						if scenario == "still applying" {
							state = "applying"
						}
						body = `{"ownership_token":"` + token + `","ownership_state":"` + state + `"}`
						if scenario == "missing ownership" {
							body = `{}`
						}
					case "POST /debug/v1/inspect":
						events = append(events, "attachment")
						body = `{"attachment":[7,11]}`
					case "GET /control/v1/tenant/" + testTenant + "/timeline/" + testTimeline, "GET /v1/tenant/" + testTenant + "/timeline/" + testTimeline:
						status = http.StatusServiceUnavailable
					default:
						t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})},
			}}
			if _, err := runtime.Provision(context.Background(), request); err == nil {
				t.Fatal("fixture should stop no later than timeline inspection")
			}
			success := scenario == "completed" || scenario == "recovery generation" || scenario == "confirmed claim"
			claim, confirmed := lifecycle.claims["tenant"]
			if confirmed != success || success && (claim.ResourceID != ownedID || claim.ImmutableGeneration != 7) {
				t.Fatalf("tenant confirmation did not use canonical ownership and attachment: confirmed=%v events=%v", confirmed, events)
			}
			if scenario == "no pending intent" || scenario == "confirmed claim" {
				if posts != 0 {
					t.Fatal("tenant create replayed without an unconfirmed pending intent")
				}
			} else if posts != 1 {
				t.Fatalf("expected one replay, got %d", posts)
			}
			if scenario == "completed" || scenario == "recovery generation" {
				if strings.Join(events, "|") != "heartbeat|post|inspect|attachment|confirm:tenant" {
					t.Fatalf("ownership was confirmed before canonical inspection: %v", events)
				}
			}
		})
	}
}
