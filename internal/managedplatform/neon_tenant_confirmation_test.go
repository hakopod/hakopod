package managedplatform

import (
	"context"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDurableNeonFreshTenantRequiresCanonicalConfirmation(t *testing.T) {
	for _, scenario := range []string{"completed", "missing ownership", "applying", "foreign", "deleting", "not found"} {
		t.Run(scenario, func(t *testing.T) {
			events := []string{}
			lifecycle := &durableNeonFixture{
				op:      DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"},
				claims:  map[string]DurableResourceClaim{},
				intents: map[string]DurableResourceIntent{},
				events:  &events,
			}
			intentID := fixtureIntentID("tenant")
			tenantInspections := 0
			posts := 0
			runtime := &DurableNeonRuntime{lifecycle: lifecycle, control: &NeonRuntime{
				config: NeonRuntimeConfig{
					StorageController:                       NeonControlTarget{Name: "storage", Origin: "https://storage.test", Token: "storage-secret-token"},
					Computes:                                []NeonControlTarget{{Name: "primary", Origin: "https://compute.test", Token: "compute-secret-token"}},
					RequestTimeout:                          time.Second,
					allowMissingStorageRegistrationsForTest: true,
				},
				client: &http.Client{Transport: neonRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					status, body := http.StatusOK, ""
					switch req.Method + " " + req.URL.Path {
					case "GET /control/v1/hakopod/ownership":
						body = `{"protocol":"hakopod-ownership-v1","tenant_delete_protocol":"prepare-v1","mutations":["tenant","timeline","pageserver_registration","safekeeper_registration"]}`
					case "GET /control/v1/tenant/" + testTenant:
						tenantInspections++
						if tenantInspections == 1 {
							status = http.StatusNotFound
							break
						}
						events = append(events, "inspect")
						token, state := intentID, "completed"
						switch scenario {
						case "missing ownership":
							body = `{}`
							break
						case "applying":
							state = "applying"
						case "foreign":
							token = strings.Repeat("f", 32)
						case "deleting":
							state = "deleting"
						case "not found":
							status = http.StatusNotFound
						}
						if body == "" && status == http.StatusOK {
							body = `{"ownership_token":"` + token + `","ownership_state":"` + state + `"}`
						}
					case "POST /v1/tenant":
						posts++
						if req.Header.Get(neonOwnershipHeader) != intentID {
							t.Fatal("fresh tenant creation omitted its reserved ownership token")
						}
						events = append(events, "post")
						status = http.StatusCreated
						body = `{"ownership_token":"` + intentID + `","shards":[{"shard_id":"` + testTenant + `","node_id":99,"generation":0}]}`
					case "POST /debug/v1/inspect":
						events = append(events, "attachment")
						body = `{"attachment":[7,11]}`
					case "GET /control/v1/tenant/" + testTenant + "/timeline/" + testTimeline,
						"GET /v1/tenant/" + testTenant + "/timeline/" + testTimeline:
						status = http.StatusServiceUnavailable
					default:
						t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})},
			}}

			if _, err := runtime.Provision(context.Background(), durableNeonRequest()); err == nil {
				t.Fatal("fixture should stop no later than timeline inspection")
			}
			if posts != 1 || tenantInspections != 2 {
				t.Fatalf("fresh tenant was not created and canonically reinspected exactly once: posts=%d inspections=%d events=%v", posts, tenantInspections, events)
			}
			claim, confirmed := lifecycle.claims["tenant"]
			if scenario == "completed" {
				ownedID, err := encodeNeonOwnedResourceID(testTenant+"@11", intentID)
				if err != nil {
					t.Fatal(err)
				}
				if !confirmed || claim.ResourceID != ownedID || claim.ImmutableGeneration != 7 || strings.Join(events, "|") != "reserve:tenant|heartbeat|post|inspect|attachment|confirm:tenant" {
					t.Fatalf("fresh tenant was not confirmed from canonical state: claim=%+v events=%v", claim, events)
				}
			} else if confirmed {
				t.Fatalf("fresh tenant was confirmed from non-canonical state: claim=%+v events=%v", claim, events)
			}
		})
	}
}

func TestDurableNeonRequestTimeoutAllowsProviderReconciliationBudget(t *testing.T) {
	newRuntime := func(timeout time.Duration) (*DurableNeonRuntime, error) {
		events := []string{}
		return NewDurableNeonRuntime(NeonRuntimeConfig{
			StorageController:                       NeonControlTarget{Name: "storage", Origin: "https://storage.test", Token: "storage-secret-token"},
			Computes:                                []NeonControlTarget{{Name: "primary", Origin: "https://compute.test", Token: "compute-secret-token"}},
			RequestTimeout:                          timeout,
			RootCAs:                                 x509.NewCertPool(),
			allowMissingStorageRegistrationsForTest: true,
		}, &durableNeonFixture{
			op:      DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"},
			claims:  map[string]DurableResourceClaim{},
			intents: map[string]DurableResourceIntent{},
			events:  &events,
		})
	}

	runtime, err := newRuntime(2 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := runtime.control.client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("durable Neon runtime omitted its bounded HTTP transport")
	}
	if runtime.control.client.Timeout != 2*time.Minute || transport.ResponseHeaderTimeout != 2*time.Minute {
		t.Fatalf("durable Neon request budget was shortened: client=%v header=%v", runtime.control.client.Timeout, transport.ResponseHeaderTimeout)
	}
	if _, err = newRuntime(2*time.Minute + time.Nanosecond); err == nil {
		t.Fatal("durable Neon runtime accepted an unbounded request timeout")
	}
}

func TestDurableNeonRequestUsesFullHeaderBudgetAndHonorsParentCancellation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		time.Sleep(650 * time.Millisecond)
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{}`))
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	events := []string{}
	runtime, err := NewDurableNeonRuntime(NeonRuntimeConfig{
		StorageController:                       NeonControlTarget{Name: "storage", Origin: server.URL, Token: "storage-secret-token"},
		Computes:                                []NeonControlTarget{{Name: "primary", Origin: server.URL, Token: "compute-secret-token"}},
		RequestTimeout:                          time.Second,
		RootCAs:                                 roots,
		allowMissingStorageRegistrationsForTest: true,
	}, &durableNeonFixture{
		op:      DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"},
		claims:  map[string]DurableResourceClaim{},
		intents: map[string]DurableResourceIntent{},
		events:  &events,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, status, err := runtime.control.request(context.Background(), runtime.control.config.StorageController, http.MethodGet, "/full-header-budget", nil); err != nil || status != http.StatusOK {
		t.Fatalf("request failed before its full one-second header budget: status=%d err=%v", status, err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	time.AfterFunc(25*time.Millisecond, cancel)
	started := time.Now()
	_, _, err = runtime.control.request(cancelled, runtime.control.config.StorageController, http.MethodGet, "/parent-cancellation", nil)
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("parent cancellation was not returned: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("parent cancellation did not abort promptly: %v", elapsed)
	}
}
