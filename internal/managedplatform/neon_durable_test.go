package managedplatform

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type neonRoundTripFunc func(*http.Request) (*http.Response, error)

func (f neonRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type durableNeonFixture struct {
	op      DurableOperation
	claims  map[string]DurableResourceClaim
	intents map[string]DurableResourceIntent
	events  *[]string
}

func (f *durableNeonFixture) Operation() DurableOperation { return f.op }
func (f *durableNeonFixture) Heartbeat(context.Context) error {
	*f.events = append(*f.events, "heartbeat")
	return nil
}
func (f *durableNeonFixture) Claims(_ context.Context, revision int64) ([]DurableResourceClaim, error) {
	result := []DurableResourceClaim{}
	for _, claim := range f.claims {
		if claim.PlatformRevision == revision {
			result = append(result, claim)
		}
	}
	return result, nil
}
func (f *durableNeonFixture) Reserve(_ context.Context, intent DurableResourceIntent) (DurableResourceIntent, error) {
	if existing, ok := f.intents[intent.Component]; ok {
		return existing, nil
	}
	intent.ID = strings.Repeat("a", 32)
	f.intents[intent.Component] = intent
	*f.events = append(*f.events, "reserve:"+intent.Component)
	return intent, nil
}
func (f *durableNeonFixture) Intents(_ context.Context, revision int64) ([]DurableResourceIntent, error) {
	result := []DurableResourceIntent{}
	for _, intent := range f.intents {
		if intent.PlatformRevision == revision {
			result = append(result, intent)
		}
	}
	return result, nil
}
func (f *durableNeonFixture) Confirm(_ context.Context, intent DurableResourceIntent, claim DurableResourceClaim) error {
	intent.Confirmed = true
	f.intents[intent.Component] = intent
	f.claims[claim.Component] = claim
	*f.events = append(*f.events, "confirm:"+claim.Component)
	return nil
}
func (f *durableNeonFixture) Cancel(_ context.Context, intent DurableResourceIntent) error {
	delete(f.intents, intent.Component)
	return nil
}
func (f *durableNeonFixture) Claim(_ context.Context, claim DurableResourceClaim) error {
	f.claims[claim.Component] = claim
	return nil
}
func (f *durableNeonFixture) Advance(_ context.Context, claim DurableResourceClaim) (DurableResourceClaim, error) {
	claim.PlatformRevision = f.op.Revision
	claim.OwnerOperationID = f.op.ID
	f.claims[claim.Component] = claim
	return claim, nil
}
func (f *durableNeonFixture) Verify(context.Context, DurableResourceClaim) error { return nil }
func (f *durableNeonFixture) Release(_ context.Context, claim DurableResourceClaim) error {
	delete(f.claims, claim.Component)
	delete(f.intents, claim.Component)
	return nil
}

func durableNeonRequest() NeonLifecycleRequest {
	raw := json.RawMessage(`{"spec":{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `","safekeeper_connstrings":["sk-1:5454","sk-2:5454","sk-3:5454"],"storage_auth_token":"runtime-storage-secret"},"compute_ctl_config":{"jwks":{"keys":[]}}}`)
	return NeonLifecycleRequest{OperationID: testOperation, TenantID: testTenant, TimelineID: testTimeline, CreateTenant: true, ComputeConfig: map[string]json.RawMessage{"primary": raw}}
}

func durableNeonRuntimeForTest(t *testing.T, storage, compute *httptest.Server, lifecycle DurableLifecycle) *DurableNeonRuntime {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(storage.Certificate())
	roots.AddCert(compute.Certificate())
	runtime, err := NewDurableNeonRuntime(NeonRuntimeConfig{StorageController: NeonControlTarget{Name: "storage", Origin: storage.URL, Token: "storage-secret-token"}, Computes: []NeonControlTarget{{Name: "primary", Origin: compute.URL, Token: "compute-secret-token"}}, RequestTimeout: time.Second, RootCAs: roots, AllowMissingStorageRegistrationsForTest: true, AllowUnqualifiedOwnershipProtocolForTest: true}, lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func TestDurableNeonAmbiguousTenantCreateRecoversOnlyItsMatchingIntent(t *testing.T) {
	events := []string{}
	tenantCreated := false
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /control/v1/tenant/" + testTenant:
			if tenantCreated {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case "POST /debug/v1/inspect":
			_, _ = w.Write([]byte(`{"attachment":[7,11]}`))
		case "POST /v1/tenant":
			events = append(events, "post:tenant")
			tenantCreated = true
			w.WriteHeader(http.StatusInternalServerError)
		case "GET /control/v1/tenant/" + testTenant + "/timeline/" + testTimeline:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Fatalf("unexpected storage request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer storage.Close()
	compute := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		t.Fatalf("unexpected compute request %s", request.URL.Path)
	}))
	defer compute.Close()
	lifecycle := &durableNeonFixture{op: DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"}, claims: map[string]DurableResourceClaim{}, intents: map[string]DurableResourceIntent{}, events: &events}
	runtime := durableNeonRuntimeForTest(t, storage, compute, lifecycle)
	if _, err := runtime.Provision(context.Background(), durableNeonRequest()); err == nil {
		t.Fatal("ambiguous create unexpectedly succeeded")
	}
	if lifecycle.intents["tenant"].Confirmed || len(lifecycle.claims) != 0 || strings.Join(events, "|") != "reserve:tenant|heartbeat|post:tenant" {
		t.Fatalf("ambiguous create did not remain pending: events=%v claims=%v intents=%v", events, lifecycle.claims, lifecycle.intents)
	}
	if _, err := runtime.Provision(context.Background(), durableNeonRequest()); err == nil {
		t.Fatal("later timeline inspection unexpectedly succeeded")
	}
	if claim := lifecycle.claims["tenant"]; claim.ResourceID != testTenant+"@11" || claim.ImmutableGeneration != 7 || !lifecycle.intents["tenant"].Confirmed || strings.Count(strings.Join(events, "|"), "post:tenant") != 1 {
		t.Fatalf("matching pending tenant intent was not recovered exactly once: events=%v claims=%v intents=%v", events, lifecycle.claims, lifecycle.intents)
	}
}

func TestDurableNeonRejectsForeignExistingTenantWithoutPendingIntent(t *testing.T) {
	events := []string{}
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /control/v1/tenant/" + testTenant:
			w.WriteHeader(http.StatusOK)
		case "POST /debug/v1/inspect":
			_, _ = w.Write([]byte(`{"attachment":[7,11]}`))
		default:
			t.Fatalf("unexpected storage request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer storage.Close()
	compute := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		t.Fatalf("unexpected compute request %s", request.URL.Path)
	}))
	defer compute.Close()
	lifecycle := &durableNeonFixture{op: DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"}, claims: map[string]DurableResourceClaim{}, intents: map[string]DurableResourceIntent{}, events: &events}
	if _, err := durableNeonRuntimeForTest(t, storage, compute, lifecycle).Provision(context.Background(), durableNeonRequest()); err == nil || len(lifecycle.claims) != 0 {
		t.Fatal("foreign existing tenant was adopted without a matching pending intent")
	}
}

func TestDurableNeonValidatesProviderIdentitiesBeforeConfirmingClaims(t *testing.T) {
	events := []string{}
	tenantCreated := false
	timelineCreated := false
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /control/v1/tenant/" + testTenant:
			if tenantCreated {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case "POST /v1/tenant":
			tenantCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"shards":[{"shard_id":"` + testTenant + `","node_id":11,"generation":7}]}`))
		case "GET /control/v1/tenant/" + testTenant + "/timeline/" + testTimeline:
			if timelineCreated {
				_, _ = w.Write([]byte(`{"shards":[{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `"}]}`))
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case "POST /v1/tenant/" + testTenant + "/timeline":
			timelineCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `","safekeepers":{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `","generation":13,"safekeepers":[{"id":1,"hostname":"sk-1"},{"id":2,"hostname":"sk-2"},{"id":3,"hostname":"sk-3"}]}}`))
		default:
			t.Fatalf("unexpected storage request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer storage.Close()
	computeAttached := false
	compute := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/configure" {
			if request.Header.Get(neonComputeOwnershipHeader) != strings.Repeat("a", 32) {
				t.Fatal("compute configure omitted the durable intent ownership token")
			}
			var config struct {
				Spec struct {
					OperationUUID string `json:"operation_uuid"`
				} `json:"spec"`
			}
			if err := json.NewDecoder(request.Body).Decode(&config); err != nil {
				t.Fatal(err)
			}
			if config.Spec.OperationUUID != strings.Repeat("a", 32) {
				t.Fatal("compute spec did not bind the durable intent ownership token")
			}
			computeAttached = true
			w.WriteHeader(http.StatusOK)
			return
		}
		if !computeAttached {
			_, _ = w.Write([]byte(`{"tenant":"","timeline":"","status":"empty"}`))
			return
		}
		_, _ = w.Write([]byte(`{"tenant":"` + testTenant + `","timeline":"` + testTimeline + `","operation_uuid":"` + strings.Repeat("a", 32) + `","status":"running"}`))
	}))
	defer compute.Close()
	lifecycle := &durableNeonFixture{op: DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"}, claims: map[string]DurableResourceClaim{}, intents: map[string]DurableResourceIntent{}, events: &events}
	state, err := durableNeonRuntimeForTest(t, storage, compute, lifecycle).Provision(context.Background(), durableNeonRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Complete || lifecycle.claims["tenant"].ResourceID != testTenant+"@11" || lifecycle.claims["tenant"].ImmutableGeneration != 7 || lifecycle.claims["timeline"].ImmutableGeneration != 13 || lifecycle.claims["compute-primary"].ResourceID == "primary" {
		t.Fatal("provider identities were not persisted exactly")
	}
	_, computeToken, err := parseNeonComputeClaimResourceID(lifecycle.claims["compute-primary"].ResourceID)
	if err != nil || computeToken != strings.Repeat("a", 32) || computeToken == lifecycle.claims["compute-primary"].OwnerOperationID {
		t.Fatal("compute claim did not preserve the durable intent token independently of operation ownership")
	}
}

func TestDurableNeonDeletionRejectsChangedTenantAttachmentBeforeMutation(t *testing.T) {
	events := []string{}
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /control/v1/tenant/" + testTenant:
			w.WriteHeader(http.StatusOK)
		case "POST /debug/v1/inspect":
			_, _ = w.Write([]byte(`{"attachment":[8,12]}`))
		default:
			t.Fatalf("unexpected mutating storage request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer storage.Close()
	compute := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		t.Fatalf("compute was mutated before identity preflight")
	}))
	defer compute.Close()
	op := DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 2, Kind: "delete"}
	_, computeID, err := neonComputeIdentity(NeonControlTarget{Name: "primary", Origin: compute.URL})
	if err != nil {
		t.Fatal(err)
	}
	computeClaimID, err := encodeNeonComputeClaimResourceID(computeID, strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &durableNeonFixture{op: op, claims: map[string]DurableResourceClaim{
		"tenant":          {PlatformID: op.PlatformID, PlatformRevision: 1, Component: "tenant", Kind: "neon_tenant", ResourceID: testTenant + "@11", ImmutableGeneration: 7, OwnerOperationID: strings.Repeat("b", 32)},
		"timeline":        {PlatformID: op.PlatformID, PlatformRevision: 1, Component: "timeline", Kind: "neon_timeline", ResourceID: strings.Repeat("c", 64), ImmutableGeneration: 13, OwnerOperationID: strings.Repeat("b", 32)},
		"compute-primary": {PlatformID: op.PlatformID, PlatformRevision: 1, Component: "compute-primary", Kind: "runtime_component", ResourceID: computeClaimID, ImmutableGeneration: 1, OwnerOperationID: strings.Repeat("b", 32)},
	}, intents: map[string]DurableResourceIntent{}, events: &events}
	if err = durableNeonRuntimeForTest(t, storage, compute, lifecycle).Deprovision(context.Background(), durableNeonRequest()); err == nil {
		t.Fatal("changed tenant generation and node did not block deletion")
	}
}

func TestNeonComputeIdentityIncludesNormalizedEndpoint(t *testing.T) {
	_, first, err := neonComputeIdentity(NeonControlTarget{Name: "primary", Origin: "https://compute-a.example.test/"})
	if err != nil {
		t.Fatal(err)
	}
	_, equivalent, err := neonComputeIdentity(NeonControlTarget{Name: "primary", Origin: "https://COMPUTE-A.EXAMPLE.TEST"})
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := neonComputeIdentity(NeonControlTarget{Name: "primary", Origin: "https://compute-b.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if first != equivalent || first == other {
		t.Fatal("compute endpoint identity is not canonical and collision-resistant across origins")
	}
}

func TestDurableNeonComputeOwnershipTokenSurvivesRevisionAdvance(t *testing.T) {
	events := []string{}
	op := DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 2, Kind: "update"}
	endpoint := strings.Repeat("a", 64)
	token := strings.Repeat("b", 32)
	resourceID, err := encodeNeonComputeClaimResourceID(endpoint, token)
	if err != nil {
		t.Fatal(err)
	}
	claim := DurableResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 1, Component: "compute-primary", Kind: "runtime_component", ResourceID: resourceID, ImmutableGeneration: 1, OwnerOperationID: strings.Repeat("c", 32)}
	lifecycle := &durableNeonFixture{op: op, claims: map[string]DurableResourceClaim{claim.Component: claim}, intents: map[string]DurableResourceIntent{}, events: &events}
	runtime := &DurableNeonRuntime{lifecycle: lifecycle}
	prior := map[string]DurableResourceClaim{claim.Component: claim}
	current := map[string]DurableResourceClaim{}
	advanced, err := runtime.activateClaim(context.Background(), prior, current, claim, true)
	if err != nil {
		t.Fatal(err)
	}
	_, advancedToken, err := parseNeonComputeClaimResourceID(advanced.ResourceID)
	if err != nil || advancedToken != token || advanced.OwnerOperationID != op.ID {
		t.Fatal("revision advance did not preserve the provider token while advancing operation ownership")
	}
}

func TestTerminateOwnedComputeRechecksTokenImmediatelyBeforeMutation(t *testing.T) {
	events := []string{}
	terminated := false
	expectedToken := strings.Repeat("a", 32)
	foreignToken := strings.Repeat("b", 32)
	lifecycle := &durableNeonFixture{op: DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 2, Kind: "delete"}, claims: map[string]DurableResourceClaim{}, intents: map[string]DurableResourceIntent{}, events: &events}
	runtime := &DurableNeonRuntime{lifecycle: lifecycle, control: &NeonRuntime{config: NeonRuntimeConfig{RequestTimeout: time.Second}, client: &http.Client{Transport: neonRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			terminated = true
		}
		body := `{"tenant":"` + testTenant + `","timeline":"` + testTimeline + `","operation_uuid":"` + foreignToken + `","status":"running"}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}}
	target := NeonControlTarget{Name: "primary", Origin: "https://compute.test", Token: "compute-secret-token"}
	if err := runtime.terminateOwnedCompute(context.Background(), target, testTenant, testTimeline, expectedToken); err == nil || terminated {
		t.Fatal("compute termination proceeded after the provider token changed")
	}
}

func TestTerminateOwnedComputeSendsOwnershipHeader(t *testing.T) {
	events := []string{}
	token := strings.Repeat("a", 32)
	requestCount := 0
	lifecycle := &durableNeonFixture{op: DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 2, Kind: "delete"}, claims: map[string]DurableResourceClaim{}, intents: map[string]DurableResourceIntent{}, events: &events}
	runtime := &DurableNeonRuntime{lifecycle: lifecycle, control: &NeonRuntime{config: NeonRuntimeConfig{RequestTimeout: time.Second}, client: &http.Client{Transport: neonRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		body := `{"tenant":"` + testTenant + `","timeline":"` + testTimeline + `","operation_uuid":"` + token + `","status":"running"}`
		if request.Method == http.MethodPost {
			if request.Header.Get(neonComputeOwnershipHeader) != token {
				t.Fatal("terminate omitted the stable provider token")
			}
			body = `{}`
		} else if requestCount == 3 {
			body = `{"tenant":"","timeline":"","operation_uuid":"` + token + `","status":"terminated"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}}
	target := NeonControlTarget{Name: "primary", Origin: "https://compute.test", Token: "compute-secret-token"}
	if err := runtime.terminateOwnedCompute(context.Background(), target, testTenant, testTimeline, token); err != nil {
		t.Fatal(err)
	}
	if requestCount != 3 || strings.Join(events, "|") != "heartbeat" {
		t.Fatal("owned termination did not perform the bounded verify-mutate-verify sequence")
	}
}

func TestDurableNeonDeleteConfigurationUsesClaimedEndpointsWithoutLiveZones(t *testing.T) {
	events := []string{}
	lifecycle := &durableNeonFixture{op: DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 2, Kind: "delete"}, claims: map[string]DurableResourceClaim{}, intents: map[string]DurableResourceIntent{}, events: &events}
	config := NeonRuntimeConfig{StorageController: NeonControlTarget{Name: "storage", Origin: "https://storage.example.test", Token: "storage-secret-token"}, Computes: []NeonControlTarget{{Name: "compute-0", Origin: "https://compute.example.test", Token: "compute-secret-token"}}, Pageservers: []NeonPageserverRegistration{{Name: "0", NodeID: 1, Generation: 1, Host: "neon-pageserver-0.ns.svc"}, {Name: "1", NodeID: 2, Generation: 1, Host: "neon-pageserver-1.ns.svc"}}, Safekeepers: []NeonSafekeeperRegistration{{Name: "0", NodeID: 1, Generation: 1, Host: "neon-safekeeper-0.ns.svc"}, {Name: "1", NodeID: 2, Generation: 1, Host: "neon-safekeeper-1.ns.svc"}, {Name: "2", NodeID: 3, Generation: 1, Host: "neon-safekeeper-2.ns.svc"}}, SafekeeperToken: "safekeeper-secret-token", RootCAs: x509.NewCertPool(), DeprovisionOnly: true}
	runtime, err := NewDurableNeonRuntime(config, lifecycle)
	if err != nil {
		t.Fatalf("delete runtime required live availability zones: %v", err)
	}
	if _, err = runtime.Provision(context.Background(), durableNeonRequest()); err == nil {
		t.Fatal("deprovision-only runtime accepted provisioning")
	}
	config.DeprovisionOnly = false
	if _, err = NewDurableNeonRuntime(config, lifecycle); err == nil {
		t.Fatal("provisioning runtime accepted storage registrations without availability zones")
	}
}

func TestDurableNeonRequestTimeoutLeavesLeaseMargin(t *testing.T) {
	events := []string{}
	lifecycle := &durableNeonFixture{op: DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"}, claims: map[string]DurableResourceClaim{}, intents: map[string]DurableResourceIntent{}, events: &events}
	config := NeonRuntimeConfig{
		StorageController: NeonControlTarget{Name: "storage", Origin: "https://storage.example.test", Token: "storage-secret-token"},
		Computes:          []NeonControlTarget{{Name: "primary", Origin: "https://compute.example.test", Token: "compute-secret-token"}},
		RequestTimeout:    21 * time.Second, RootCAs: x509.NewCertPool(), AllowMissingStorageRegistrationsForTest: true,
	}
	if _, err := NewDurableNeonRuntime(config, lifecycle); err == nil {
		t.Fatal("provider request timeout was allowed to consume the 30-second operation lease")
	}
}

func TestDurableNeonResourceReadBoundCoversRevisionRollover(t *testing.T) {
	if maxDurableNeonResources != MaxComponents*5 || maxDurableNeonResources < 135 {
		t.Fatal("durable Neon resource read bound does not cover revision rollover")
	}
}

func TestDurableNeonRegistersAndReobservesStorageNodesBeforeUse(t *testing.T) {
	events := []string{}
	registeredPageserver, registeredSafekeeper := false, false
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /control/v1/node/1":
			if !registeredPageserver {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"id":1,"availability_zone_id":"zone-a","listen_http_addr":"neon-pageserver-0.ns.svc","listen_http_port":9897,"listen_https_port":9898,"listen_pg_addr":"neon-pageserver-0.ns.svc","listen_pg_port":6400}`))
		case "POST /control/v1/node":
			registeredPageserver = true
		case "GET /control/v1/safekeeper/1":
			if !registeredSafekeeper {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"id":1,"region_id":"hakopod","version":1,"host":"neon-safekeeper-0.ns.svc","port":5454,"http_port":7677,"https_port":7676,"availability_zone_id":"zone-a"}`))
		case "POST /control/v1/safekeeper/1":
			registeredSafekeeper = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected storage request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer storage.Close()
	roots := x509.NewCertPool()
	roots.AddCert(storage.Certificate())
	lifecycle := &durableNeonFixture{op: DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"}, claims: map[string]DurableResourceClaim{}, intents: map[string]DurableResourceIntent{}, events: &events}
	runtime, err := NewDurableNeonRuntime(NeonRuntimeConfig{StorageController: NeonControlTarget{Name: "storage", Origin: storage.URL, Token: "storage-secret-token"}, Computes: []NeonControlTarget{{Name: "primary", Origin: storage.URL, Token: "compute-secret-token"}}, Pageservers: []NeonPageserverRegistration{{Name: "0", NodeID: 1, Generation: 1, Host: "neon-pageserver-0.ns.svc", AvailabilityZone: "zone-a"}, {Name: "1", NodeID: 2, Generation: 1, Host: "neon-pageserver-1.ns.svc", AvailabilityZone: "zone-b"}}, Safekeepers: []NeonSafekeeperRegistration{{Name: "0", NodeID: 1, Generation: 1, Host: "neon-safekeeper-0.ns.svc", AvailabilityZone: "zone-a"}, {Name: "1", NodeID: 2, Generation: 1, Host: "neon-safekeeper-1.ns.svc", AvailabilityZone: "zone-b"}, {Name: "2", NodeID: 3, Generation: 1, Host: "neon-safekeeper-2.ns.svc", AvailabilityZone: "zone-c"}}, SafekeeperToken: "safekeeper-secret-token", RequestTimeout: time.Second, RootCAs: roots}, lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise one member of each registry directly; the complete production
	// inventory follows the same bounded loop and durable claim contract.
	runtime.control.config.Pageservers = runtime.control.config.Pageservers[:1]
	runtime.control.config.Safekeepers = runtime.control.config.Safekeepers[:1]
	if err = runtime.registerStorageNodes(context.Background(), map[string]DurableResourceClaim{}, map[string]DurableResourceClaim{}, map[string]DurableResourceIntent{}); err != nil {
		t.Fatal(err)
	}
	if len(lifecycle.claims) != 2 || !registeredPageserver || !registeredSafekeeper {
		t.Fatalf("storage registration was not durably confirmed: %#v", lifecycle.claims)
	}
}

func TestDurableNeonReobservesIdenticalSafekeeperMembership(t *testing.T) {
	const tenantID = "11111111111111111111111111111111"
	const timelineID = "22222222222222222222222222222222"
	response := `{"tenant_id":"` + tenantID + `","timeline_id":"` + timelineID + `","mconf":{"generation":4,"members":[{"id":1,"host":"neon-safekeeper-0.ns.svc","pg_port":5454},{"id":2,"host":"neon-safekeeper-1.ns.svc","pg_port":5454},{"id":3,"host":"neon-safekeeper-2.ns.svc","pg_port":5454}],"new_members":null}}`
	requests := 0
	mismatch := false
	runtime := &DurableNeonRuntime{control: &NeonRuntime{
		config: NeonRuntimeConfig{
			SafekeeperToken: "safekeeper-secret-token",
			RequestTimeout:  time.Second,
			Safekeepers: []NeonSafekeeperRegistration{
				{Name: "0", NodeID: 1, Generation: 1, Host: "neon-safekeeper-0.ns.svc", AvailabilityZone: "zone-a"},
				{Name: "1", NodeID: 2, Generation: 1, Host: "neon-safekeeper-1.ns.svc", AvailabilityZone: "zone-b"},
				{Name: "2", NodeID: 3, Generation: 1, Host: "neon-safekeeper-2.ns.svc", AvailabilityZone: "zone-c"},
			},
		},
		client: &http.Client{Transport: neonRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			requests++
			if request.Header.Get("Authorization") != "Bearer safekeeper-secret-token" {
				t.Fatal("safekeeper request omitted its bearer token")
			}
			if request.URL.Path == "/v1/status" {
				id := "1"
				if strings.Contains(request.URL.Host, "safekeeper-1") {
					id = "2"
				}
				if strings.Contains(request.URL.Host, "safekeeper-2") {
					id = "3"
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":` + id + `}`)), Header: make(http.Header)}, nil
			}
			body := response
			if mismatch && strings.Contains(request.URL.Host, "safekeeper-2") {
				body = strings.Replace(body, `"generation":4`, `"generation":5`, 1)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})},
	}}
	exists, identity, generation, hosts, err := runtime.inspectSafekeeperTimeline(context.Background(), tenantID, timelineID)
	if err != nil || !exists || requests != 6 || generation != 4 || len(hosts) != 3 {
		t.Fatal("safekeeper membership was not fully observed", exists, generation, hosts, requests, err)
	}
	expected, _, _, err := verifiedNeonTimelineIdentity(tenantID, timelineID, 4, []neonTimelineMember{{ID: "1", Host: "neon-safekeeper-0.ns.svc"}, {ID: "2", Host: "neon-safekeeper-1.ns.svc"}, {ID: "3", Host: "neon-safekeeper-2.ns.svc"}})
	if err != nil || identity != expected {
		t.Fatal("safekeeper observation did not recover the creation identity", err)
	}
	mismatch = true
	if _, _, _, _, err = runtime.inspectSafekeeperTimeline(context.Background(), tenantID, timelineID); err == nil || !strings.Contains(err.Error(), "membership is inconsistent") {
		t.Fatal("inconsistent safekeeper membership was accepted", err)
	}
}

func TestDurableNeonDeletionResumesAfterClaimsWereReleased(t *testing.T) {
	events := []string{}
	op := DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 2, Kind: "delete"}
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
	for _, node := range pageservers {
		component := "pageserver-registration-" + node.Name
		claims[component] = DurableResourceClaim{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: component, Kind: "runtime_component", ResourceID: neonStorageIdentity("pageserver", node.Name, node.NodeID, node.Generation, node.Host, node.AvailabilityZone), ImmutableGeneration: node.Generation, OwnerOperationID: op.ID}
	}
	for _, node := range safekeepers {
		component := "safekeeper-registration-" + node.Name
		claims[component] = DurableResourceClaim{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: component, Kind: "runtime_component", ResourceID: neonStorageIdentity("safekeeper", node.Name, node.NodeID, node.Generation, node.Host, node.AvailabilityZone), ImmutableGeneration: node.Generation, OwnerOperationID: op.ID}
	}
	lifecycle := &durableNeonFixture{op: op, claims: claims, intents: map[string]DurableResourceIntent{}, events: &events}
	attached := false
	registrationsPresent := true
	runtime := &DurableNeonRuntime{lifecycle: lifecycle, control: &NeonRuntime{config: NeonRuntimeConfig{
		StorageController: NeonControlTarget{Name: "storage", Origin: "https://controller.test", Token: "storage-secret-token"},
		Computes:          []NeonControlTarget{{Name: "primary", Origin: "https://compute.test", Token: "compute-secret-token"}},
		Pageservers:       pageservers, Safekeepers: safekeepers, SafekeeperToken: "safekeeper-secret-token", RequestTimeout: time.Second, DeprovisionOnly: true,
	}, client: &http.Client{Transport: neonRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, status := "", http.StatusNotFound
		if registrationsPresent && request.URL.Host == "controller.test" && strings.HasPrefix(request.URL.Path, "/control/v1/node/") {
			id := strings.TrimPrefix(request.URL.Path, "/control/v1/node/")
			zone := map[string]string{"1": "zone-a", "2": "zone-b"}[id]
			host := map[string]string{"1": "ps-0.test", "2": "ps-1.test"}[id]
			body, status = `{"id":`+id+`,"availability_zone_id":"`+zone+`","listen_http_addr":"`+host+`","listen_http_port":9897,"listen_https_port":9898,"listen_pg_addr":"`+host+`","listen_pg_port":6400}`, http.StatusOK
		} else if registrationsPresent && request.URL.Host == "controller.test" && strings.HasPrefix(request.URL.Path, "/control/v1/safekeeper/") {
			id := strings.TrimPrefix(request.URL.Path, "/control/v1/safekeeper/")
			zone := map[string]string{"1": "zone-a", "2": "zone-b", "3": "zone-c"}[id]
			host := map[string]string{"1": "sk-0.test", "2": "sk-1.test", "3": "sk-2.test"}[id]
			body, status = `{"id":`+id+`,"region_id":"hakopod","version":1,"host":"`+host+`","port":5454,"http_port":7677,"https_port":7676,"availability_zone_id":"`+zone+`"}`, http.StatusOK
		} else if strings.HasPrefix(request.URL.Host, "sk-") && request.URL.Path == "/v1/status" {
			id := map[byte]string{'0': "1", '1': "2", '2': "3"}[request.URL.Host[3]]
			body, status = `{"id":`+id+`}`, http.StatusOK
		} else if request.URL.Host == "compute.test" && request.URL.Path == "/status" {
			if attached {
				body = `{"tenant":"` + testTenant + `","timeline":"` + testTimeline + `","status":"running"}`
			} else {
				body = `{"tenant":"","timeline":"","status":"empty"}`
			}
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}}
	if err := runtime.Deprovision(context.Background(), durableNeonRequest()); err != nil {
		t.Fatalf("deletion did not resume after tenant, timeline, and compute claims were released: %v", err)
	}
	if len(lifecycle.claims) != len(pageservers)+len(safekeepers) {
		t.Fatal("storage registration claims were released before namespace deletion")
	}
	registrationsPresent = false
	if err := runtime.Deprovision(context.Background(), durableNeonRequest()); err != nil {
		t.Fatalf("authenticated missing storage registrations were not treated as already deleted: %v", err)
	}
	if len(lifecycle.claims) != len(pageservers)+len(safekeepers) {
		t.Fatal("missing storage registration claims were released before namespace deletion")
	}
	attached = true
	if err := runtime.Deprovision(context.Background(), durableNeonRequest()); err == nil {
		t.Fatal("attached compute without an ownership claim was accepted")
	}
}
