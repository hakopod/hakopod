package managedplatform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type pendingDeleteLifecycle struct {
	*durableNeonFixture
	cancelFailures int
}

func (f *pendingDeleteLifecycle) Cancel(ctx context.Context, intent DurableResourceIntent) error {
	if f.cancelFailures > 0 {
		f.cancelFailures--
		return fmt.Errorf("development fixture: interrupted before intent cancellation")
	}
	return f.durableNeonFixture.Cancel(ctx, intent)
}

func TestDurableNeonDeletesPendingTimelineThroughOwnedParent(t *testing.T) {
	for _, test := range []struct {
		name              string
		mutateIntent      func(*DurableResourceIntent)
		mutateParent      func(*DurableResourceIntent)
		noParentIntent    bool
		noIntent          bool
		pageStatus        int
		pageToken         string
		wrongPageIdentity bool
		safekeeperMask    int
		foreignSafekeeper bool
		attachedCompute   bool
		preflightFails    bool
		prepareFails      bool
		retry             bool
		retryCancel       bool
		absentPlacement   bool
		retryLedger       bool
		wantError         bool
	}{
		{name: "pageserver exists before safekeeper selection"},
		{name: "one safekeeper exists", safekeeperMask: 2},
		{name: "all safekeepers exist", safekeeperMask: 7},
		{name: "pageserver absent and safekeeper exists", pageStatus: 404, safekeeperMask: 1},
		{name: "all provider timelines absent", pageStatus: 404},
		{name: "retry after parent starts deletion", retry: true},
		{name: "retry after parent deletion before cancellation", retryCancel: true},
		{name: "resume ownership deletion after placement is absent", absentPlacement: true, pageStatus: 404},
		{name: "resume interrupted ownership tombstone", absentPlacement: true, pageStatus: 404, retryLedger: true},
		{name: "resume after timeline claim and intent are released", absentPlacement: true, pageStatus: 404, noIntent: true},
		{name: "released timeline still requires terminal ownership proof", absentPlacement: true, pageStatus: 404, noIntent: true, retryLedger: true},
		{name: "absent placement refuses foreign preflight", absentPlacement: true, pageStatus: 404, preflightFails: true, wantError: true},
		{name: "absent placement refuses failed preparation", absentPlacement: true, pageStatus: 404, prepareFails: true, wantError: true},
		{name: "absent placement refuses attached unclaimed compute", absentPlacement: true, pageStatus: 404, attachedCompute: true, wantError: true},
		{name: "absent placement refuses remaining safekeeper", absentPlacement: true, pageStatus: 404, safekeeperMask: 1, wantError: true},
		{name: "missing intent", noIntent: true, wantError: true},
		{name: "confirmed intent is not pending authority", mutateIntent: func(i *DurableResourceIntent) { i.Confirmed = true }, wantError: true},
		{name: "foreign intent platform", mutateIntent: func(i *DurableResourceIntent) { i.PlatformID = strings.Repeat("f", 32) }, wantError: true},
		{name: "foreign intent revision", mutateIntent: func(i *DurableResourceIntent) { i.PlatformRevision = 3 }, wantError: true},
		{name: "foreign current operation", mutateIntent: func(i *DurableResourceIntent) { i.PlatformRevision = 2 }, wantError: true},
		{name: "delete operation cannot reserve a timeline", mutateIntent: func(i *DurableResourceIntent) { i.PlatformRevision = 2; i.OwnerOperationID = testOperation }, wantError: true},
		{name: "different valid creation owner", mutateIntent: func(i *DurableResourceIntent) { i.OwnerOperationID = strings.Repeat("e", 32) }, wantError: true},
		{name: "missing parent creation intent", noParentIntent: true, wantError: true},
		{name: "unconfirmed parent creation intent", mutateParent: func(i *DurableResourceIntent) { i.Confirmed = false }, wantError: true},
		{name: "different parent token", mutateParent: func(i *DurableResourceIntent) { i.ID = strings.Repeat("e", 32) }, wantError: true},
		{name: "different parent identity", mutateParent: func(i *DurableResourceIntent) { i.ExternalKey = strings.Repeat("e", 32) }, wantError: true},
		{name: "malformed intent owner", mutateIntent: func(i *DurableResourceIntent) { i.OwnerOperationID = "unknown" }, wantError: true},
		{name: "wrong intent kind", mutateIntent: func(i *DurableResourceIntent) { i.Kind = "neon_tenant" }, wantError: true},
		{name: "wrong intent external key", mutateIntent: func(i *DurableResourceIntent) { i.ExternalKey += "other" }, wantError: true},
		{name: "malformed intent token", mutateIntent: func(i *DurableResourceIntent) { i.ID = "unknown" }, wantError: true},
		{name: "foreign pageserver token", pageToken: strings.Repeat("f", 32), wantError: true},
		{name: "wrong pageserver identity", wrongPageIdentity: true, wantError: true},
		{name: "unavailable pageserver is not absence", pageStatus: 503, wantError: true},
		{name: "foreign partial safekeeper token", safekeeperMask: 2, foreignSafekeeper: true, wantError: true},
		{name: "unclaimed attached compute", attachedCompute: true, wantError: true},
		{name: "foreign descendant blocks parent preflight", preflightFails: true, wantError: true},
		{name: "failed parent fence blocks cleanup", prepareFails: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			op := DurableOperation{ID: testOperation, PlatformID: strings.Repeat("a", 32), Revision: 2, Kind: "delete"}
			createID := strings.Repeat("b", 32)
			tenantToken, timelineToken := fixtureIntentID("tenant"), fixtureIntentID("timeline")
			deleteToken := strings.Repeat("d", 32)
			intent := DurableResourceIntent{ID: timelineToken, PlatformID: op.PlatformID, PlatformRevision: 1, Component: "timeline", Kind: "neon_timeline", ExternalKey: testTenant + "/" + testTimeline, OwnerOperationID: createID}
			if test.mutateIntent != nil {
				test.mutateIntent(&intent)
			}
			tenantClaimID, err := encodeNeonOwnedResourceID(testTenant+"@11", tenantToken)
			if err != nil {
				t.Fatal(err)
			}
			claims := map[string]DurableResourceClaim{"tenant": {PlatformID: op.PlatformID, PlatformRevision: 1, Component: "tenant", Kind: "neon_tenant", ResourceID: tenantClaimID, ImmutableGeneration: 7, OwnerOperationID: createID}}
			safekeepers := make([]NeonSafekeeperRegistration, 3)
			for i := range safekeepers {
				n := NeonSafekeeperRegistration{Name: fmt.Sprint(i), NodeID: int64(i + 1), Generation: 1, Host: fmt.Sprintf("sk-%d.test", i), AvailabilityZone: fmt.Sprintf("zone-%d", i)}
				safekeepers[i] = n
				component := "safekeeper-registration-" + n.Name
				id, err := encodeNeonOwnedResourceID(neonStorageIdentity("safekeeper", n.Name, n.NodeID, n.Generation, n.Host, n.AvailabilityZone), fixtureIntentID(component))
				if err != nil {
					t.Fatal(err)
				}
				claims[component] = DurableResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 1, Component: component, Kind: "runtime_component", ResourceID: id, ImmutableGeneration: 1, OwnerOperationID: createID}
			}
			lifecycle := &durableNeonFixture{op: op, claims: claims, intents: map[string]DurableResourceIntent{}, events: &events}
			parentIntent := DurableResourceIntent{ID: tenantToken, PlatformID: op.PlatformID, PlatformRevision: 1, Component: "tenant", Kind: "neon_tenant", ExternalKey: testTenant, OwnerOperationID: createID, Confirmed: true}
			if test.mutateParent != nil {
				test.mutateParent(&parentIntent)
			}
			if !test.noParentIntent {
				lifecycle.intents["tenant"] = parentIntent
			}
			if !test.noIntent {
				lifecycle.intents["timeline"] = intent
			}
			claimsBefore := make(map[string]DurableResourceClaim, len(claims))
			for k, v := range claims {
				claimsBefore[k] = v
			}
			parentPresent, parentState, fence := !test.absentPlacement, "completed", ""
			parentTombstoned := false
			pagePresent := test.pageStatus != 404
			skMask := test.safekeeperMask
			preflights, preparations, parentDeletes, childDeletes := 0, 0, 0, 0
			adapter := &pendingDeleteLifecycle{durableNeonFixture: lifecycle}
			if test.retryCancel {
				adapter.cancelFailures = 1
			}
			runtime := &DurableNeonRuntime{lifecycle: adapter, control: &NeonRuntime{config: NeonRuntimeConfig{
				StorageController: NeonControlTarget{Name: "storage", Origin: "https://storage.test", Token: "storage-secret-token"},
				Computes:          []NeonControlTarget{{Name: "primary", Origin: "https://compute.test", Token: "compute-secret-token"}},
				Safekeepers:       safekeepers, SafekeeperToken: "safekeeper-secret-token", RequestTimeout: time.Second, DeprovisionOnly: true,
			}, client: &http.Client{Transport: neonRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				status := http.StatusOK
				body := map[string]any{}
				path := req.URL.Path
				switch {
				case path == "/control/v1/hakopod/ownership":
					body = map[string]any{"protocol": "hakopod-ownership-v1", "tenant_delete_protocol": "prepare-v1", "mutations": []string{"tenant", "timeline", "pageserver_registration", "safekeeper_registration"}}
				case req.Method == http.MethodGet && path == "/control/v1/tenant/"+testTenant:
					if parentPresent {
						body = map[string]any{"ownership_token": tenantToken, "ownership_state": parentState, "ownership_delete_token": fence}
					} else {
						status = 404
					}
				case path == "/debug/v1/inspect":
					body = map[string]any{"attachment": []int{7, 11}}
				case req.Method == http.MethodGet && path == "/control/v1/tenant/"+testTenant+"/timeline/"+testTimeline:
					// Applying controller ownership intentionally has no token.
					body = map[string]any{"shards": []map[string]string{{"tenant_id": testTenant, "timeline_id": testTimeline}}}
				case req.URL.Host == "storage.test" && req.Method == http.MethodGet && path == "/v1/tenant/"+testTenant+"/timeline/"+testTimeline:
					if !pagePresent {
						status = 404
						break
					}
					if test.pageStatus != 0 {
						status = test.pageStatus
					}
					token := timelineToken
					if test.pageToken != "" {
						token = test.pageToken
					}
					body = map[string]any{"tenant_id": testTenant, "timeline_id": testTimeline, "ownership_token": token}
					if test.wrongPageIdentity {
						body["timeline_id"] = strings.Repeat("e", 32)
					}
				case strings.HasPrefix(path, "/control/v1/safekeeper/"):
					id := strings.TrimPrefix(path, "/control/v1/safekeeper/")
					index := map[string]int{"1": 0, "2": 1, "3": 2}[id]
					n := safekeepers[index]
					body = map[string]any{"id": n.NodeID, "region_id": "hakopod", "version": 1, "host": n.Host, "port": 5454, "http_port": 7677, "https_port": 7676, "availability_zone_id": n.AvailabilityZone, "ownership_token": fixtureIntentID("safekeeper-registration-" + n.Name)}
				case strings.HasPrefix(req.URL.Host, "sk-"):
					index := map[string]int{"sk-0.test:7676": 0, "sk-1.test:7676": 1, "sk-2.test:7676": 2}[req.URL.Host]
					if path == "/v1/status" {
						body = map[string]any{"id": index + 1}
						break
					}
					if skMask&(1<<index) == 0 {
						status = 404
						break
					}
					token := timelineToken
					if test.foreignSafekeeper {
						token = strings.Repeat("f", 32)
					}
					members := []map[string]any{}
					for _, n := range safekeepers {
						members = append(members, map[string]any{"id": n.NodeID, "host": n.Host, "pg_port": 5454})
					}
					body = map[string]any{"tenant_id": testTenant, "timeline_id": testTimeline, "ownership_token": token, "mconf": map[string]any{"generation": 1, "members": members, "new_members": nil}}
				case req.URL.Host == "compute.test" && req.Method == http.MethodGet && path == "/status":
					body = map[string]any{"tenant": "", "timeline": "", "status": "empty"}
					if test.attachedCompute {
						body = map[string]any{"tenant": testTenant, "timeline": testTimeline, "status": "running"}
					}
				case req.Method == http.MethodDelete && path == "/v1/tenant/"+testTenant:
					if req.Header.Get(neonOwnershipHeader) != tenantToken {
						t.Fatal("parent deletion lost the original ownership token")
					}
					if req.URL.Query().Get("validate_only") == "true" {
						preflights++
						if parentTombstoned {
							body = nil
							break
						}
						body = map[string]any{"schema_version": 1, "digest": strings.Repeat("a", 64)}
						if test.preflightFails {
							status = 409
						}
						break
					}
					if req.URL.Query().Get("prepare_delete") == "true" {
						preparations++
						if test.prepareFails {
							status = 409
							break
						}
						parentState, fence = "deleting", deleteToken
						body = map[string]any{"schema_version": 1, "digest": strings.Repeat("a", 64), "delete_token": deleteToken}
						break
					}
					if preflights == 0 || preparations == 0 || req.Header.Get(neonDeletionHeader) != deleteToken {
						t.Fatal("parent deletion ran without provider preflight and fence")
					}
					parentDeletes++
					pagePresent, skMask = false, 0
					if (test.retry || test.retryLedger) && parentDeletes == 1 {
						status = 202
					} else {
						parentPresent = false
						parentTombstoned = true
						status = 204
					}
				case req.Method == http.MethodDelete || req.Method == http.MethodPost:
					childDeletes++
					t.Fatalf("unexpected child mutation %s %s", req.Method, path)
				default:
					t.Fatalf("unexpected request %s %s %s", req.Method, req.URL.Host, path)
				}
				encoded, _ := json.Marshal(body)
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(encoded))), Header: make(http.Header)}, nil
			})}}}
			err = runtime.Deprovision(context.Background(), durableNeonDeleteRequest())
			if test.retry || test.retryLedger {
				if err == nil || !strings.Contains(err.Error(), "still in progress") {
					t.Fatalf("expected resumable parent deletion, got %v", err)
				}
				err = runtime.Deprovision(context.Background(), durableNeonDeleteRequest())
			}
			if test.retryCancel {
				if err == nil || !strings.Contains(err.Error(), "before intent cancellation") {
					t.Fatalf("expected cancellation interruption, got %v", err)
				}
				err = runtime.Deprovision(context.Background(), durableNeonDeleteRequest())
			}
			if test.wantError {
				if err == nil {
					t.Fatal("unsafe partial deletion was accepted")
				}
				if parentDeletes != 0 || childDeletes != 0 || !reflect.DeepEqual(lifecycle.claims, claimsBefore) {
					t.Fatal("refused deletion mutated resources or claims")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if parentPresent || pagePresent || skMask != 0 || parentDeletes == 0 || childDeletes != 0 {
				t.Fatal("owned parent did not remove its partial children")
			}
			if _, ok := lifecycle.claims["timeline"]; ok {
				t.Fatal("incomplete timeline became a confirmed claim")
			}
			if _, ok := lifecycle.intents["timeline"]; ok {
				t.Fatal("pending intent remains after verified parent deletion")
			}
			if claim, ok := lifecycle.claims["tenant"]; !ok || claim.ResourceID != tenantClaimID || claim.PlatformRevision != 2 || claim.OwnerOperationID != op.ID {
				t.Fatal("parent tombstone authority was released before namespace absence")
			}
			// A crash after intent cancellation but before namespace deletion
			// must resume from the retained parent tombstone, not a timeline
			// endpoint that returns 503 when its parent is absent.
			if err = runtime.Deprovision(context.Background(), durableNeonDeleteRequest()); err != nil {
				t.Fatalf("retry after cancellation lost deletion authority: %v", err)
			}
			for _, event := range events {
				if event == "confirm:timeline" {
					t.Fatal("pending timeline was confirmed during deletion")
				}
			}
		})
	}
}
