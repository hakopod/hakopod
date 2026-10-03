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

func TestDurableNeonPendingTimelineResumesBeforeCanonicalInspection(t *testing.T) {
	for _, scenario := range []string{"completed", "missing replay token", "foreign replay token", "provider conflict", "canonical missing", "canonical missing ownership", "foreign controller token", "foreign safekeeper token", "no pending intent", "confirmed claim"} {
		t.Run(scenario, func(t *testing.T) {
			events := []string{}
			op := DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"}
			pageservers := []NeonPageserverRegistration{
				{Name: "0", NodeID: 1, Generation: 1, Host: "ps-0.test", AvailabilityZone: "zone-a"},
				{Name: "1", NodeID: 2, Generation: 1, Host: "ps-1.test", AvailabilityZone: "zone-b"},
			}
			safekeepers := []NeonSafekeeperRegistration{
				{Name: "0", NodeID: 1, Generation: 1, Host: "sk-0.test", AvailabilityZone: "zone-a"},
				{Name: "1", NodeID: 2, Generation: 1, Host: "sk-1.test", AvailabilityZone: "zone-b"},
				{Name: "2", NodeID: 3, Generation: 1, Host: "sk-2.test", AvailabilityZone: "zone-c"},
			}
			claims := map[string]DurableResourceClaim{}
			claimStorage := func(kind, name string, nodeID int64, host, zone string) {
				component := kind + "-registration-" + name
				identity := neonStorageIdentity(kind, name, nodeID, 1, host, zone)
				owned, err := encodeNeonOwnedResourceID(identity, fixtureIntentID(component))
				if err != nil {
					t.Fatal(err)
				}
				claims[component] = DurableResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 1, Component: component, Kind: "runtime_component", ResourceID: owned, ImmutableGeneration: 1, OwnerOperationID: op.ID}
			}
			for _, node := range pageservers {
				claimStorage("pageserver", node.Name, node.NodeID, node.Host, node.AvailabilityZone)
			}
			for _, node := range safekeepers {
				claimStorage("safekeeper", node.Name, node.NodeID, node.Host, node.AvailabilityZone)
			}
			tenantOwned, err := encodeNeonOwnedResourceID(testTenant+"@11", fixtureIntentID("tenant"))
			if err != nil {
				t.Fatal(err)
			}
			claims["tenant"] = DurableResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 1, Component: "tenant", Kind: "neon_tenant", ResourceID: tenantOwned, ImmutableGeneration: 7, OwnerOperationID: op.ID}

			intentID := fixtureIntentID("timeline")
			intents := map[string]DurableResourceIntent{"timeline": {ID: intentID, PlatformID: op.PlatformID, PlatformRevision: 1, Component: "timeline", Kind: "neon_timeline", ExternalKey: testTenant + "/" + testTimeline, OwnerOperationID: op.ID}}
			members := []neonTimelineMember{{ID: "1", Host: "sk-0.test"}, {ID: "2", Host: "sk-1.test"}, {ID: "3", Host: "sk-2.test"}}
			timelineIdentity, _, _, err := verifiedNeonTimelineIdentity(testTenant, testTimeline, 13, members)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "no pending intent" {
				delete(intents, "timeline")
			}
			if scenario == "confirmed claim" {
				owned, encodeErr := encodeNeonOwnedResourceID(timelineIdentity, intentID)
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				claims["timeline"] = DurableResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 1, Component: "timeline", Kind: "neon_timeline", ResourceID: owned, ImmutableGeneration: 13, OwnerOperationID: op.ID}
				intent := intents["timeline"]
				intent.Confirmed = true
				intents["timeline"] = intent
			}
			lifecycle := &durableNeonFixture{op: op, claims: claims, intents: intents, events: &events}
			request := durableNeonRequest()
			request.ComputeConfig = map[string]json.RawMessage{}
			request.RecoveryTimelineGeneration = 13
			request.AncestorTimelineID = strings.Repeat("5", 32)

			posts := 0
			runtime := &DurableNeonRuntime{lifecycle: lifecycle, control: &NeonRuntime{
				config: NeonRuntimeConfig{StorageController: NeonControlTarget{Name: "storage", Origin: "https://storage.test", Token: "storage-secret-token"}, Pageservers: pageservers, Safekeepers: safekeepers, SafekeeperToken: "safekeeper-secret-token", RequestTimeout: time.Second},
				client: &http.Client{Transport: neonRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					status, body := http.StatusOK, ""
					switch {
					case req.URL.Host == "storage.test" && req.Method == http.MethodGet && req.URL.Path == "/control/v1/hakopod/ownership":
						body = `{"protocol":"hakopod-ownership-v1","tenant_delete_protocol":"prepare-v1","mutations":["tenant","timeline","pageserver_registration","safekeeper_registration"]}`
					case req.URL.Host == "storage.test" && req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, "/control/v1/node/"):
						id := strings.TrimPrefix(req.URL.Path, "/control/v1/node/")
						node := pageservers[map[string]int{"1": 0, "2": 1}[id]]
						body = `{"id":` + id + `,"availability_zone_id":"` + node.AvailabilityZone + `","listen_http_addr":"` + node.Host + `","listen_http_port":9897,"listen_https_port":9898,"listen_pg_addr":"` + node.Host + `","listen_pg_port":6400,"ownership_token":"` + fixtureIntentID("pageserver-registration-"+node.Name) + `"}`
					case req.URL.Host == "storage.test" && req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, "/control/v1/safekeeper/"):
						id := strings.TrimPrefix(req.URL.Path, "/control/v1/safekeeper/")
						node := safekeepers[map[string]int{"1": 0, "2": 1, "3": 2}[id]]
						body = `{"id":` + id + `,"region_id":"hakopod","version":1,"host":"` + node.Host + `","port":5454,"http_port":7677,"https_port":7676,"availability_zone_id":"` + node.AvailabilityZone + `","ownership_token":"` + fixtureIntentID("safekeeper-registration-"+node.Name) + `"}`
					case req.URL.Host == "storage.test" && req.Method == http.MethodGet && req.URL.Path == "/control/v1/tenant/"+testTenant:
						body = `{"ownership_token":"` + fixtureIntentID("tenant") + `","ownership_state":"completed"}`
					case req.URL.Host == "storage.test" && req.Method == http.MethodPost && req.URL.Path == "/debug/v1/inspect":
						body = `{"attachment":[7,11]}`
					case req.URL.Host == "storage.test" && req.Method == http.MethodPost && req.URL.Path == "/v1/tenant/"+testTenant+"/timeline":
						status = http.StatusCreated
						posts++
						if len(events) == 0 || events[len(events)-1] != "heartbeat" || req.Header.Get(neonOwnershipHeader) != intentID {
							t.Fatal("timeline replay omitted its heartbeat or exact ownership token")
						}
						var got map[string]any
						if decodeErr := json.NewDecoder(req.Body).Decode(&got); decodeErr != nil {
							t.Fatal(decodeErr)
						}
						want := map[string]any{"new_timeline_id": testTimeline, "pg_version": float64(17), "generation": float64(13), "ancestor_timeline_id": request.AncestorTimelineID, "read_only": false}
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("timeline replay changed its body: got=%v want=%v", got, want)
						}
						events = append(events, "post")
						token := intentID
						if scenario == "foreign replay token" {
							token = strings.Repeat("f", 32)
						}
						body = `{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `","safekeepers":{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `","generation":13,"safekeepers":[{"id":1,"hostname":"sk-0.test"},{"id":2,"hostname":"sk-1.test"},{"id":3,"hostname":"sk-2.test"}]},"ownership_token":"` + token + `"}`
						if scenario == "missing replay token" {
							body = strings.Replace(body, `,"ownership_token":"`+token+`"`, "", 1)
						}
						if scenario == "provider conflict" {
							status = http.StatusConflict
						}
					case req.URL.Host == "storage.test" && req.Method == http.MethodGet && req.URL.Path == "/control/v1/tenant/"+testTenant+"/timeline/"+testTimeline:
						events = append(events, "inspect")
						if scenario != "no pending intent" && scenario != "confirmed claim" && posts != 1 {
							t.Fatal("timeline was inspected before its pending create replayed")
						}
						if scenario == "canonical missing" {
							status = http.StatusNotFound
							break
						}
						token := intentID
						if scenario == "foreign controller token" {
							token = strings.Repeat("f", 32)
						}
						body = `{"shards":[{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `"}],"ownership_token":"` + token + `"}`
						if scenario == "canonical missing ownership" {
							body = `{"shards":[{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `"}]}`
						}
					case strings.HasPrefix(req.URL.Host, "sk-") && req.Method == http.MethodGet && req.URL.Path == "/v1/status":
						id := map[string]string{"sk-0.test:7676": "1", "sk-1.test:7676": "2", "sk-2.test:7676": "3"}[req.URL.Host]
						body = `{"id":` + id + `}`
					case strings.HasPrefix(req.URL.Host, "sk-") && req.Method == http.MethodGet && req.URL.Path == "/v1/tenant/"+testTenant+"/timeline/"+testTimeline:
						token := intentID
						if scenario == "foreign safekeeper token" && strings.HasPrefix(req.URL.Host, "sk-2") {
							token = strings.Repeat("f", 32)
						}
						body = `{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `","ownership_token":"` + token + `","mconf":{"generation":13,"members":[{"id":1,"host":"sk-0.test","pg_port":5454},{"id":2,"host":"sk-1.test","pg_port":5454},{"id":3,"host":"sk-2.test","pg_port":5454}],"new_members":null}}`
					default:
						t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})},
			}}
			_, err = runtime.Provision(context.Background(), request)
			success := scenario == "completed" || scenario == "confirmed claim"
			if (err == nil) != success {
				t.Fatalf("unexpected timeline resume result: err=%v events=%v", err, events)
			}
			claim, confirmed := lifecycle.claims["timeline"]
			if confirmed != success || success && (claim.ImmutableGeneration != 13 || !strings.HasPrefix(claim.ResourceID, timelineIdentity+":")) {
				t.Fatalf("timeline confirmation did not come from canonical state: confirmed=%v claim=%+v events=%v", confirmed, claim, events)
			}
			if scenario == "no pending intent" || scenario == "confirmed claim" {
				if posts != 0 {
					t.Fatal("timeline replayed without an exact unconfirmed pending intent")
				}
			} else if posts != 1 {
				t.Fatalf("pending timeline did not replay exactly once: %d", posts)
			}
			if scenario == "completed" && strings.Join(events, "|") != "heartbeat|post|inspect|confirm:timeline" {
				t.Fatalf("timeline was not canonically confirmed after replay: %v", events)
			}
		})
	}
}
