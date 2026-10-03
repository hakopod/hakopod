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

func TestDurableNeonPendingStorageRegistrationReplaysBeforeInspection(t *testing.T) {
	for _, kind := range []string{"pageserver", "safekeeper"} {
		for _, scenario := range []string{"completed", "canonical missing ownership", "canonical not found", "missing replay token", "foreign replay token", "provider conflict", "no pending intent", "confirmed claim"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				events := []string{}
				component := kind + "-registration-0"
				intentID := fixtureIntentID(component)
				pageserver := NeonPageserverRegistration{Name: "0", NodeID: 1, Generation: 1, Host: "neon-pageserver-0.ns.svc", AvailabilityZone: "zone-a"}
				safekeeper := NeonSafekeeperRegistration{Name: "0", NodeID: 1, Generation: 1, Host: "neon-safekeeper-0.ns.svc", AvailabilityZone: "zone-a"}
				identity := neonStorageIdentity(kind, "0", 1, 1, map[string]string{"pageserver": pageserver.Host, "safekeeper": safekeeper.Host}[kind], "zone-a")
				lifecycle := &durableNeonFixture{
					op:      DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"},
					claims:  map[string]DurableResourceClaim{},
					intents: map[string]DurableResourceIntent{},
					events:  &events,
				}
				if scenario != "no pending intent" && scenario != "confirmed claim" {
					lifecycle.intents[component] = DurableResourceIntent{ID: intentID, PlatformID: lifecycle.op.PlatformID, PlatformRevision: 1, Component: component, Kind: "runtime_component", ExternalKey: identity, OwnerOperationID: testOperation}
				}
				if scenario == "confirmed claim" {
					ownedID, err := encodeNeonOwnedResourceID(identity, intentID)
					if err != nil {
						t.Fatal(err)
					}
					lifecycle.claims[component] = DurableResourceClaim{PlatformID: lifecycle.op.PlatformID, PlatformRevision: 1, Component: component, Kind: "runtime_component", ResourceID: ownedID, ImmutableGeneration: 1, OwnerOperationID: testOperation}
				}

				postPath := "/control/v1/node"
				getPath := "/control/v1/node/1"
				wantBody := neonPageserverRegistrationBody(pageserver)
				canonical := `{"id":1,"availability_zone_id":"zone-a","listen_http_addr":"neon-pageserver-0.ns.svc","listen_http_port":9897,"listen_https_port":9898,"listen_pg_addr":"neon-pageserver-0.ns.svc","listen_pg_port":6400}`
				if kind == "safekeeper" {
					postPath = "/control/v1/safekeeper/1"
					getPath = postPath
					wantBody = neonSafekeeperRegistrationBody(safekeeper)
					canonical = `{"id":1,"region_id":"hakopod","version":1,"host":"neon-safekeeper-0.ns.svc","port":5454,"http_port":7677,"https_port":7676,"availability_zone_id":"zone-a"}`
				}
				encodedBody, err := json.Marshal(wantBody)
				if err != nil {
					t.Fatal(err)
				}
				var wantJSON map[string]any
				if err = json.Unmarshal(encodedBody, &wantJSON); err != nil {
					t.Fatal(err)
				}
				posts := 0
				runtime := &DurableNeonRuntime{lifecycle: lifecycle, control: &NeonRuntime{
					config: NeonRuntimeConfig{StorageController: NeonControlTarget{Name: "storage", Origin: "https://storage.test", Token: "storage-secret-token"}, Pageservers: []NeonPageserverRegistration{pageserver}, Safekeepers: []NeonSafekeeperRegistration{safekeeper}, RequestTimeout: time.Second},
					client: &http.Client{Transport: neonRoundTripFunc(func(req *http.Request) (*http.Response, error) {
						status, body := http.StatusOK, ""
						switch req.Method + " " + req.URL.Path {
						case "POST " + postPath:
							posts++
							if len(events) == 0 || events[len(events)-1] != "heartbeat" || req.Header.Get(neonOwnershipHeader) != intentID {
								t.Fatal("registration replay omitted its heartbeat or exact ownership token")
							}
							var got map[string]any
							if err := json.NewDecoder(req.Body).Decode(&got); err != nil || !reflect.DeepEqual(got, wantJSON) {
								t.Fatalf("registration replay changed its body: got=%v want=%v err=%v", got, wantJSON, err)
							}
							events = append(events, "post")
							token := intentID
							if scenario == "foreign replay token" {
								token = strings.Repeat("f", 32)
							}
							body = strings.TrimSuffix(canonical, "}") + `,"ownership_token":"` + token + `"}`
							if scenario == "missing replay token" {
								body = canonical
							}
							if scenario == "provider conflict" {
								status = http.StatusConflict
							}
						case "GET " + getPath:
							events = append(events, "inspect")
							body = canonical
							if scenario == "canonical not found" {
								status, body = http.StatusNotFound, ""
								break
							}
							if scenario != "canonical missing ownership" && scenario != "no pending intent" {
								body = strings.TrimSuffix(body, "}") + `,"ownership_token":"` + intentID + `"}`
							}
						default:
							t.Fatalf("unexpected registration request %s %s", req.Method, req.URL.Path)
						}
						return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
					})},
				}}
				if kind == "pageserver" {
					runtime.control.config.Safekeepers = nil
				} else {
					runtime.control.config.Pageservers = nil
				}

				err = runtime.registerStorageNodes(context.Background(), map[string]DurableResourceClaim{}, lifecycle.claims, lifecycle.intents)
				success := scenario == "completed" || scenario == "confirmed claim"
				if (err == nil) != success {
					t.Fatalf("unexpected registration result: err=%v events=%v", err, events)
				}
				claim, confirmed := lifecycle.claims[component]
				if confirmed != success || success && claim.ImmutableGeneration != 1 {
					t.Fatalf("registration confirmation changed: confirmed=%v claim=%+v events=%v", confirmed, claim, events)
				}
				if scenario == "completed" {
					if posts != 1 || strings.Join(events, "|") != "heartbeat|post|inspect|confirm:"+component {
						t.Fatalf("pending registration was not replayed before canonical confirmation: posts=%d events=%v", posts, events)
					}
				} else if scenario == "no pending intent" || scenario == "confirmed claim" {
					if posts != 0 {
						t.Fatal("registration replayed without an exact pending intent")
					}
				} else if posts != 1 {
					t.Fatalf("pending registration did not replay exactly once: %d", posts)
				}
			})
		}
	}
}
