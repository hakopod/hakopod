package managedplatform

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type neonRoundTripFunc func(*http.Request) (*http.Response, error)

func (f neonRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type durableNeonFixture struct {
	op           DurableOperation
	claims       map[string]DurableResourceClaim
	intents      map[string]DurableResourceIntent
	events       *[]string
	claimsCalls  int
	intentsCalls int
}

func (f *durableNeonFixture) Operation() DurableOperation { return f.op }
func (f *durableNeonFixture) Heartbeat(context.Context) error {
	*f.events = append(*f.events, "heartbeat")
	return nil
}
func (f *durableNeonFixture) Claims(_ context.Context, revision int64) ([]DurableResourceClaim, error) {
	f.claimsCalls++
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
	intent.ID = fixtureIntentID(intent.Component)
	f.intents[intent.Component] = intent
	*f.events = append(*f.events, "reserve:"+intent.Component)
	return intent, nil
}

func fixtureIntentID(component string) string {
	sum := sha256.Sum256([]byte(component))
	return hex.EncodeToString(sum[:16])
}
func (f *durableNeonFixture) Intents(_ context.Context, revision int64) ([]DurableResourceIntent, error) {
	f.intentsCalls++
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

func durableNeonDeleteRequest() NeonLifecycleRequest {
	return NeonLifecycleRequest{OperationID: testOperation, TenantID: testTenant, TimelineID: testTimeline, ComputeConfig: map[string]json.RawMessage{}}
}

func TestDurableNeonDeletionRejectsMalformedLifecycleIdentities(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*DurableOperation, *NeonLifecycleRequest)
	}{
		{name: "operation", mutate: func(op *DurableOperation, request *NeonLifecycleRequest) {
			op.ID = "ABC"
			request.OperationID = op.ID
		}},
		{name: "tenant", mutate: func(_ *DurableOperation, request *NeonLifecycleRequest) { request.TenantID = strings.Repeat("A", 32) }},
		{name: "timeline", mutate: func(_ *DurableOperation, request *NeonLifecycleRequest) { request.TimelineID = strings.Repeat("g", 32) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			op := DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 2, Kind: "delete"}
			request := durableNeonDeleteRequest()
			test.mutate(&op, &request)
			lifecycle := &durableNeonFixture{op: op, claims: map[string]DurableResourceClaim{}, intents: map[string]DurableResourceIntent{}, events: &events}
			runtime := &DurableNeonRuntime{lifecycle: lifecycle, control: &NeonRuntime{config: NeonRuntimeConfig{DeprovisionOnly: true}}}
			if err := runtime.Deprovision(context.Background(), request); err == nil || !strings.Contains(err.Error(), "32 lowercase hexadecimal") {
				t.Fatalf("malformed deletion identity was accepted: %v", err)
			}
			if len(events) != 0 || lifecycle.claimsCalls != 0 || lifecycle.intentsCalls != 0 {
				t.Fatal("malformed deletion identity reached durable or provider state")
			}
		})
	}
}

func TestDurableNeonProvisionChecksCompleteOwnershipCapabilityBeforeState(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "missing", status: http.StatusNotFound},
		{name: "partial", status: http.StatusOK, body: `{"protocol":"hakopod-ownership-v1","mutations":["tenant","timeline"]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			lifecycle := &durableNeonFixture{op: DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"}, claims: map[string]DurableResourceClaim{}, intents: map[string]DurableResourceIntent{}, events: &events}
			requests := 0
			runtime := &DurableNeonRuntime{lifecycle: lifecycle, control: &NeonRuntime{
				config: NeonRuntimeConfig{
					StorageController: NeonControlTarget{Name: "storage", Origin: "https://storage.test", Token: "storage-secret-token"},
					Computes:          []NeonControlTarget{{Name: "primary", Origin: "https://compute.test", Token: "compute-secret-token"}},
					RequestTimeout:    time.Second,
				},
				client: &http.Client{Transport: neonRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					requests++
					if request.Method != http.MethodGet || request.URL.Path != "/control/v1/hakopod/ownership" || request.Header.Get("Authorization") != "Bearer storage-secret-token" {
						t.Fatalf("unexpected capability request %s %s", request.Method, request.URL.Path)
					}
					return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body)), Header: make(http.Header)}, nil
				})},
			}}
			if _, err := runtime.Provision(context.Background(), durableNeonRequest()); err == nil {
				t.Fatal("provisioning continued without complete provider ownership support")
			}
			if requests != 1 || lifecycle.claimsCalls != 0 || lifecycle.intentsCalls != 0 || len(events) != 0 {
				t.Fatalf("capability failure touched lifecycle state: requests=%d claims=%d intents=%d events=%v", requests, lifecycle.claimsCalls, lifecycle.intentsCalls, events)
			}
		})
	}
}

func durableNeonRuntimeForTest(t *testing.T, storage, compute *httptest.Server, lifecycle DurableLifecycle) *DurableNeonRuntime {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(storage.Certificate())
	roots.AddCert(compute.Certificate())
	runtime, err := NewDurableNeonRuntime(NeonRuntimeConfig{StorageController: NeonControlTarget{Name: "storage", Origin: storage.URL, Token: "storage-secret-token"}, Computes: []NeonControlTarget{{Name: "primary", Origin: compute.URL, Token: "compute-secret-token"}}, RequestTimeout: time.Second, RootCAs: roots, allowMissingStorageRegistrationsForTest: true, allowUnqualifiedOwnershipProtocolForTest: true}, lifecycle)
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
		case "GET /control/v1/tenant/" + testTenant + "/timeline/" + testTimeline, "GET /v1/tenant/" + testTenant + "/timeline/" + testTimeline:
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

func TestDurableNeonRejectsForeignProviderTokenForPendingTenant(t *testing.T) {
	events := []string{}
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /control/v1/hakopod/ownership":
			_, _ = w.Write([]byte(`{"protocol":"hakopod-ownership-v1","tenant_delete_protocol":"prepare-v1","mutations":["tenant","timeline","pageserver_registration","safekeeper_registration"]}`))
		case "POST /v1/tenant":
			w.WriteHeader(http.StatusConflict)
		case "GET /control/v1/tenant/" + testTenant:
			_, _ = w.Write([]byte(`{"ownership_token":"ffffffffffffffffffffffffffffffff","ownership_state":"completed"}`))
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
	lifecycle := &durableNeonFixture{
		op:     DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"},
		claims: map[string]DurableResourceClaim{},
		intents: map[string]DurableResourceIntent{"tenant": {
			ID: fixtureIntentID("tenant"), PlatformID: strings.Repeat("4", 32), PlatformRevision: 1, Component: "tenant", Kind: "neon_tenant", ExternalKey: testTenant, OwnerOperationID: testOperation,
		}},
		events: &events,
	}
	runtime := durableNeonRuntimeForTest(t, storage, compute, lifecycle)
	runtime.control.config.allowUnqualifiedOwnershipProtocolForTest = false
	if _, err := runtime.Provision(context.Background(), durableNeonRequest()); err == nil || !strings.Contains(err.Error(), "resume Neon tenant left a pending intent") {
		t.Fatalf("foreign provider ownership was accepted: %v", err)
	}
	if lifecycle.intents["tenant"].Confirmed || len(lifecycle.claims) != 0 || strings.Join(events, "|") != "heartbeat" {
		t.Fatal("foreign provider ownership was converted into a durable claim")
	}
}

func TestDurableNeonValidatesProviderIdentitiesBeforeConfirmingClaims(t *testing.T) {
	events := []string{}
	tenantCreated := false
	timelineCreated := false
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /control/v1/hakopod/ownership":
			_, _ = w.Write([]byte(`{"protocol":"hakopod-ownership-v1","tenant_delete_protocol":"prepare-v1","mutations":["tenant","timeline","pageserver_registration","safekeeper_registration"]}`))
		case "GET /control/v1/tenant/" + testTenant:
			if tenantCreated {
				_, _ = w.Write([]byte(`{"ownership_token":"` + fixtureIntentID("tenant") + `","ownership_state":"completed"}`))
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case "POST /debug/v1/inspect":
			_, _ = w.Write([]byte(`{"attachment":[7,11]}`))
		case "POST /v1/tenant":
			if request.Header.Get(neonOwnershipHeader) != fixtureIntentID("tenant") {
				t.Fatal("tenant create omitted its durable ownership token")
			}
			tenantCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"shards":[{"shard_id":"` + testTenant + `","node_id":99,"generation":0}],"ownership_token":"` + fixtureIntentID("tenant") + `","ownership_state":"completed"}`))
		case "GET /control/v1/tenant/" + testTenant + "/timeline/" + testTimeline:
			if timelineCreated {
				_, _ = w.Write([]byte(`{"shards":[{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `"}]}`))
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case "POST /v1/tenant/" + testTenant + "/timeline":
			if request.Header.Get(neonOwnershipHeader) != fixtureIntentID("timeline") {
				t.Fatal("timeline create omitted its durable ownership token")
			}
			timelineCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `","safekeepers":{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `","generation":13,"safekeepers":[{"id":1,"hostname":"sk-1"},{"id":2,"hostname":"sk-2"},{"id":3,"hostname":"sk-3"}]},"ownership_token":"` + fixtureIntentID("timeline") + `"}`))
		default:
			t.Fatalf("unexpected storage request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer storage.Close()
	computeAttached := false
	compute := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/configure" {
			if request.Header.Get(neonOwnershipHeader) != fixtureIntentID("compute-primary") {
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
			if config.Spec.OperationUUID != fixtureIntentID("compute-primary") {
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
		_, _ = w.Write([]byte(`{"tenant":"` + testTenant + `","timeline":"` + testTimeline + `","operation_uuid":"` + fixtureIntentID("compute-primary") + `","status":"running"}`))
	}))
	defer compute.Close()
	lifecycle := &durableNeonFixture{op: DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"}, claims: map[string]DurableResourceClaim{}, intents: map[string]DurableResourceIntent{}, events: &events}
	runtime := durableNeonRuntimeForTest(t, storage, compute, lifecycle)
	runtime.control.config.allowUnqualifiedOwnershipProtocolForTest = false
	state, err := runtime.Provision(context.Background(), durableNeonRequest())
	if err != nil {
		t.Fatal(err)
	}
	tenantIdentity, tenantToken, tenantErr := parseNeonOwnedResourceID(lifecycle.claims["tenant"].ResourceID)
	timelineIdentity, timelineToken, timelineErr := parseNeonOwnedResourceID(lifecycle.claims["timeline"].ResourceID)
	if !state.Complete || tenantErr != nil || tenantIdentity != testTenant+"@11" || tenantToken != fixtureIntentID("tenant") || lifecycle.claims["tenant"].ImmutableGeneration != 7 || timelineErr != nil || len(timelineIdentity) != 64 || timelineToken != fixtureIntentID("timeline") || lifecycle.claims["timeline"].ImmutableGeneration != 13 || lifecycle.claims["compute-primary"].ResourceID == "primary" {
		t.Fatal("provider identities were not persisted exactly")
	}
	_, computeToken, err := parseNeonComputeClaimResourceID(lifecycle.claims["compute-primary"].ResourceID)
	if err != nil || computeToken != fixtureIntentID("compute-primary") || computeToken == lifecycle.claims["compute-primary"].OwnerOperationID {
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
	if err = durableNeonRuntimeForTest(t, storage, compute, lifecycle).Deprovision(context.Background(), durableNeonDeleteRequest()); err == nil {
		t.Fatal("changed tenant generation and node did not block deletion")
	}
}

func TestDurableNeonDeletionUsesOwnershipHeadersAndRechecksProviderState(t *testing.T) {
	events := []string{}
	op := DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 2, Kind: "delete"}
	tenantToken := strings.Repeat("a", 32)
	timelineToken := strings.Repeat("b", 32)
	computeToken := strings.Repeat("c", 32)
	safekeepers := []NeonSafekeeperRegistration{
		{Name: "0", NodeID: 1, Generation: 1, Host: "sk-0.test", AvailabilityZone: "zone-a"},
		{Name: "1", NodeID: 2, Generation: 1, Host: "sk-1.test", AvailabilityZone: "zone-b"},
		{Name: "2", NodeID: 3, Generation: 1, Host: "sk-2.test", AvailabilityZone: "zone-c"},
	}
	members := []neonTimelineMember{{ID: "1", Host: "sk-0.test"}, {ID: "2", Host: "sk-1.test"}, {ID: "3", Host: "sk-2.test"}}
	timelineIdentity, _, _, err := verifiedNeonTimelineIdentity(testTenant, testTimeline, 13, members)
	if err != nil {
		t.Fatal(err)
	}
	computeTarget := NeonControlTarget{Name: "primary", Origin: "https://compute.test", Token: "compute-secret-token"}
	_, computeIdentity, err := neonComputeIdentity(computeTarget)
	if err != nil {
		t.Fatal(err)
	}
	tenantClaimID, err := encodeNeonOwnedResourceID(testTenant+"@11", tenantToken)
	if err != nil {
		t.Fatal(err)
	}
	timelineClaimID, err := encodeNeonOwnedResourceID(timelineIdentity, timelineToken)
	if err != nil {
		t.Fatal(err)
	}
	computeClaimID, err := encodeNeonComputeClaimResourceID(computeIdentity, computeToken)
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]DurableResourceClaim{
		"tenant":          {PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: "tenant", Kind: "neon_tenant", ResourceID: tenantClaimID, ImmutableGeneration: 7, OwnerOperationID: op.ID},
		"timeline":        {PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: "timeline", Kind: "neon_timeline", ResourceID: timelineClaimID, ImmutableGeneration: 13, OwnerOperationID: op.ID},
		"compute-primary": {PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: "compute-primary", Kind: "runtime_component", ResourceID: computeClaimID, ImmutableGeneration: 1, OwnerOperationID: op.ID},
	}
	for _, node := range safekeepers {
		component := "safekeeper-registration-" + node.Name
		token := fixtureIntentID(component)
		identity, encodeErr := encodeNeonOwnedResourceID(neonStorageIdentity("safekeeper", node.Name, node.NodeID, node.Generation, node.Host, node.AvailabilityZone), token)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		claims[component] = DurableResourceClaim{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: component, Kind: "runtime_component", ResourceID: identity, ImmutableGeneration: node.Generation, OwnerOperationID: op.ID}
	}
	lifecycle := &durableNeonFixture{op: op, claims: claims, intents: map[string]DurableResourceIntent{}, events: &events}
	tenantPresent := true
	tenantOwnershipState := "completed"
	tenantDeletionToken := ""
	controllerTimelinePresent := true
	safekeeperTimelinePresent := true
	computeAttached := true
	tenantDescribeCalls := 0
	timelineDescribeCalls := 0
	safekeeperTimelineCalls := 0
	tenantDeletes := 0
	tenantDeletePreflights := 0
	tenantDeletePreparations := 0
	rejectTenantDeletePreflight := true
	rejectTenantDeletePreparation := true
	timelineDeletes := 0
	runtime := &DurableNeonRuntime{lifecycle: lifecycle, control: &NeonRuntime{
		config: NeonRuntimeConfig{
			StorageController:                       NeonControlTarget{Name: "storage", Origin: "https://storage.test", Token: "storage-secret-token"},
			Computes:                                []NeonControlTarget{computeTarget},
			Safekeepers:                             safekeepers,
			SafekeeperToken:                         "safekeeper-secret-token",
			RequestTimeout:                          time.Second,
			DeprovisionOnly:                         true,
			allowMissingStorageRegistrationsForTest: true,
		},
		client: &http.Client{Transport: neonRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			body := ""
			status := http.StatusOK
			switch {
			case request.URL.Host == "storage.test" && request.Method == http.MethodGet && request.URL.Path == "/control/v1/hakopod/ownership":
				body = `{"protocol":"hakopod-ownership-v1","tenant_delete_protocol":"prepare-v1","mutations":["tenant","timeline","pageserver_registration","safekeeper_registration"]}`
			case request.URL.Host == "storage.test" && request.Method == http.MethodGet && request.URL.Path == "/control/v1/tenant/"+testTenant:
				tenantDescribeCalls++
				if tenantPresent {
					body = `{"ownership_token":"` + tenantToken + `","ownership_state":"` + tenantOwnershipState + `"`
					if tenantDeletionToken != "" {
						body += `,"ownership_delete_token":"` + tenantDeletionToken + `"`
					}
					body += `}`
				} else {
					status = http.StatusNotFound
				}
			case request.URL.Host == "storage.test" && request.Method == http.MethodPost && request.URL.Path == "/debug/v1/inspect":
				body = `{"attachment":[7,11]}`
			case request.URL.Host == "storage.test" && request.Method == http.MethodGet && request.URL.Path == "/control/v1/tenant/"+testTenant+"/timeline/"+testTimeline:
				timelineDescribeCalls++
				if controllerTimelinePresent {
					body = `{"shards":[{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `"}],"ownership_token":"` + timelineToken + `"}`
				} else {
					status = http.StatusNotFound
				}
			case request.URL.Host == "storage.test" && request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/control/v1/safekeeper/"):
				id := strings.TrimPrefix(request.URL.Path, "/control/v1/safekeeper/")
				index := map[string]int{"1": 0, "2": 1, "3": 2}[id]
				node := safekeepers[index]
				body = `{"id":` + id + `,"region_id":"hakopod","version":1,"host":"` + node.Host + `","port":5454,"http_port":7677,"https_port":7676,"availability_zone_id":"` + node.AvailabilityZone + `","ownership_token":"` + fixtureIntentID("safekeeper-registration-"+node.Name) + `"}`
			case strings.HasPrefix(request.URL.Host, "sk-") && request.Method == http.MethodGet && request.URL.Path == "/v1/status":
				if request.Header.Get("Authorization") != "Bearer safekeeper-secret-token" {
					t.Fatal("safekeeper status omitted its bearer token")
				}
				id := map[string]string{"sk-0.test:7676": "1", "sk-1.test:7676": "2", "sk-2.test:7676": "3"}[request.URL.Host]
				body = `{"id":` + id + `}`
			case strings.HasPrefix(request.URL.Host, "sk-") && request.Method == http.MethodGet && request.URL.Path == "/v1/tenant/"+testTenant+"/timeline/"+testTimeline:
				safekeeperTimelineCalls++
				if safekeeperTimelinePresent {
					body = `{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `","mconf":{"generation":13,"members":[{"id":1,"host":"sk-0.test","pg_port":5454},{"id":2,"host":"sk-1.test","pg_port":5454},{"id":3,"host":"sk-2.test","pg_port":5454}],"new_members":null},"ownership_token":"` + timelineToken + `"}`
				} else {
					status = http.StatusNotFound
				}
			case request.URL.Host == "compute.test" && request.Method == http.MethodGet && request.URL.Path == "/status":
				if computeAttached {
					body = `{"tenant":"` + testTenant + `","timeline":"` + testTimeline + `","operation_uuid":"` + computeToken + `","status":"running"}`
				} else {
					body = `{"tenant":"","timeline":"","operation_uuid":"` + computeToken + `","status":"terminated"}`
				}
			case request.URL.Host == "compute.test" && request.Method == http.MethodPost && request.URL.Path == "/terminate":
				if request.URL.RawQuery != "mode=immediate" || request.Header.Get(neonOwnershipHeader) != computeToken {
					t.Fatal("compute termination omitted its durable ownership token")
				}
				computeAttached = false
			case request.URL.Host == "storage.test" && request.Method == http.MethodDelete && request.URL.Path == "/v1/tenant/"+testTenant+"/timeline/"+testTimeline:
				timelineDeletes++
				if request.Header.Get(neonOwnershipHeader) != timelineToken {
					t.Fatal("timeline delete omitted its durable ownership token")
				}
				controllerTimelinePresent = false
				safekeeperTimelinePresent = false
				status = http.StatusNoContent
			case request.URL.Host == "storage.test" && request.Method == http.MethodDelete && request.URL.Path == "/v1/tenant/"+testTenant:
				if request.URL.Query().Get("validate_only") == "true" {
					tenantDeletePreflights++
					if !computeAttached || request.Header.Get(neonOwnershipHeader) != tenantToken {
						t.Fatal("tenant deletion preflight ran after mutation or omitted its durable ownership token")
					}
					if rejectTenantDeletePreflight {
						status = http.StatusConflict
						body = `{"msg":"foreign remote descendant"}`
					}
					break
				}
				if request.URL.Query().Get("prepare_delete") == "true" {
					tenantDeletePreparations++
					tenantOwnershipState = "deleting"
					if !computeAttached || request.Header.Get(neonOwnershipHeader) != tenantToken {
						t.Fatal("tenant deletion preparation ran after mutation or omitted its durable ownership token")
					}
					if rejectTenantDeletePreparation {
						status = http.StatusConflict
						body = `{"msg":"fence publication failed"}`
					} else {
						tenantDeletionToken = "55555555555555555555555555555555"
						body = `{"schema_version":1,"digest":"` + strings.Repeat("a", 64) + `","delete_token":"` + tenantDeletionToken + `"}`
					}
					break
				}
				tenantDeletes++
				if controllerTimelinePresent || safekeeperTimelinePresent || request.Header.Get(neonOwnershipHeader) != tenantToken || request.Header.Get(neonDeletionHeader) != "55555555555555555555555555555555" {
					t.Fatal("tenant delete ran before timeline deletion or omitted a durable token")
				}
				tenantPresent = false
				status = http.StatusNoContent
			default:
				t.Fatalf("unexpected Neon deletion request %s %s%s", request.Method, request.URL.Host, request.URL.RequestURI())
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})},
	}}
	claimsBefore := make(map[string]DurableResourceClaim, len(lifecycle.claims))
	for component, claim := range lifecycle.claims {
		claimsBefore[component] = claim
	}
	if err := runtime.Deprovision(context.Background(), durableNeonDeleteRequest()); err == nil {
		t.Fatal("foreign remote descendant did not block deletion")
	}
	if !tenantPresent || !controllerTimelinePresent || !safekeeperTimelinePresent || !computeAttached || tenantDeletes != 0 || timelineDeletes != 0 {
		t.Fatal("failed tenant preflight mutated a known owned resource")
	}
	if !reflect.DeepEqual(lifecycle.claims, claimsBefore) || len(events) != 0 {
		t.Fatal("failed tenant preflight advanced or released ownership claims")
	}
	if len(lifecycle.claims) != len(claims) {
		t.Fatal("failed tenant preflight advanced or released ownership claims")
	}
	rejectTenantDeletePreflight = false
	if err := runtime.Deprovision(context.Background(), durableNeonDeleteRequest()); err == nil {
		t.Fatal("failed tenant deletion fence preparation did not block deletion")
	}
	if !tenantPresent || !controllerTimelinePresent || !safekeeperTimelinePresent || !computeAttached || tenantDeletes != 0 || timelineDeletes != 0 || !reflect.DeepEqual(lifecycle.claims, claimsBefore) || len(events) != 0 {
		t.Fatal("failed tenant deletion fence preparation mutated Hakopod claims or child resources")
	}
	rejectTenantDeletePreparation = false
	// Resume the exact prepared deletion after a process stop that followed
	// pageserver timeline teardown but preceded safekeeper cleanup.
	tenantDeletionToken = "55555555555555555555555555555555"
	controllerTimelinePresent = false
	if err := runtime.Deprovision(context.Background(), durableNeonDeleteRequest()); err != nil {
		t.Fatal(err)
	}
	if tenantDeletePreflights != 3 || tenantDeletePreparations != 2 || tenantDeletes != 1 || timelineDeletes != 1 || tenantDescribeCalls != 4 || timelineDescribeCalls != 4 || safekeeperTimelineCalls != 12 {
		t.Fatalf("owned deletion did not preflight, prepare, and recheck provider state: tenant preflights=%d tenant preparations=%d tenant deletes=%d timeline deletes=%d tenant describes=%d timeline describes=%d safekeeper timeline describes=%d", tenantDeletePreflights, tenantDeletePreparations, tenantDeletes, timelineDeletes, tenantDescribeCalls, timelineDescribeCalls, safekeeperTimelineCalls)
	}
	if _, ok := lifecycle.claims["tenant"]; !ok {
		t.Fatal("tenant tombstone authority was released before namespace absence")
	}
	if _, ok := lifecycle.claims["timeline"]; ok {
		t.Fatal("timeline claim was not released after the verified delete")
	}
	if _, ok := lifecycle.claims["compute-primary"]; ok {
		t.Fatal("compute claim was not released after the verified termination")
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
			if request.Header.Get(neonOwnershipHeader) != token {
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

func TestDurableNeonRequestTimeoutRemainsBounded(t *testing.T) {
	events := []string{}
	lifecycle := &durableNeonFixture{op: DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"}, claims: map[string]DurableResourceClaim{}, intents: map[string]DurableResourceIntent{}, events: &events}
	config := NeonRuntimeConfig{
		StorageController: NeonControlTarget{Name: "storage", Origin: "https://storage.example.test", Token: "storage-secret-token"},
		Computes:          []NeonControlTarget{{Name: "primary", Origin: "https://compute.example.test", Token: "compute-secret-token"}},
		RequestTimeout:    2*time.Minute + time.Second, RootCAs: x509.NewCertPool(), allowMissingStorageRegistrationsForTest: true,
	}
	if _, err := NewDurableNeonRuntime(config, lifecycle); err == nil {
		t.Fatal("provider request timeout exceeded the bounded reconciliation budget")
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
		pageserverToken := fixtureIntentID("pageserver-registration-0")
		safekeeperToken := fixtureIntentID("safekeeper-registration-0")
		switch request.Method + " " + request.URL.Path {
		case "GET /control/v1/node/1":
			if !registeredPageserver {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"id":1,"availability_zone_id":"zone-a","listen_http_addr":"neon-pageserver-0.ns.svc","listen_http_port":9897,"listen_https_port":9898,"listen_pg_addr":"neon-pageserver-0.ns.svc","listen_pg_port":6400,"ownership_token":"` + pageserverToken + `"}`))
		case "POST /control/v1/node":
			if request.Header.Get(neonOwnershipHeader) != pageserverToken {
				t.Fatal("pageserver registration omitted its durable ownership token")
			}
			registeredPageserver = true
			_, _ = w.Write([]byte(`{"id":1,"availability_zone_id":"zone-a","listen_http_addr":"neon-pageserver-0.ns.svc","listen_http_port":9897,"listen_https_port":9898,"listen_pg_addr":"neon-pageserver-0.ns.svc","listen_pg_port":6400,"ownership_token":"` + pageserverToken + `"}`))
		case "GET /control/v1/safekeeper/1":
			if !registeredSafekeeper {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"id":1,"region_id":"hakopod","version":1,"host":"neon-safekeeper-0.ns.svc","port":5454,"http_port":7677,"https_port":7676,"availability_zone_id":"zone-a","ownership_token":"` + safekeeperToken + `"}`))
		case "POST /control/v1/safekeeper/1":
			if request.Header.Get(neonOwnershipHeader) != safekeeperToken {
				t.Fatal("safekeeper registration omitted its durable ownership token")
			}
			registeredSafekeeper = true
			_, _ = w.Write([]byte(`{"id":1,"region_id":"hakopod","version":1,"host":"neon-safekeeper-0.ns.svc","port":5454,"http_port":7677,"https_port":7676,"availability_zone_id":"zone-a","ownership_token":"` + safekeeperToken + `"}`))
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
	for component, claim := range lifecycle.claims {
		_, token, parseErr := parseNeonOwnedResourceID(claim.ResourceID)
		if parseErr != nil || token != fixtureIntentID(component) {
			t.Fatalf("%s claim did not preserve its provider ownership token: %v", component, parseErr)
		}
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
			SafekeeperToken:                          "safekeeper-secret-token",
			RequestTimeout:                           time.Second,
			allowUnqualifiedOwnershipProtocolForTest: true,
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
	exists, identity, generation, hosts, _, err := runtime.inspectSafekeeperTimeline(context.Background(), tenantID, timelineID)
	if err != nil || !exists || requests != 6 || generation != 4 || len(hosts) != 3 {
		t.Fatal("safekeeper membership was not fully observed", exists, generation, hosts, requests, err)
	}
	expected, _, _, err := verifiedNeonTimelineIdentity(tenantID, timelineID, 4, []neonTimelineMember{{ID: "1", Host: "neon-safekeeper-0.ns.svc"}, {ID: "2", Host: "neon-safekeeper-1.ns.svc"}, {ID: "3", Host: "neon-safekeeper-2.ns.svc"}})
	if err != nil || identity != expected {
		t.Fatal("safekeeper observation did not recover the creation identity", err)
	}
	mismatch = true
	if _, _, _, _, _, err = runtime.inspectSafekeeperTimeline(context.Background(), tenantID, timelineID); err == nil || !strings.Contains(err.Error(), "membership is inconsistent") {
		t.Fatal("inconsistent safekeeper membership was accepted", err)
	}
}

func TestInspectOwnedRecoveryReservationRejectsForeignComputeToken(t *testing.T) {
	target := NeonControlTarget{Name: "primary", Origin: "https://compute.example.test", Token: "compute-secret-token"}
	externalKey, _, err := neonComputeIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &durableNeonFixture{op: DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"}}
	runtime := &DurableNeonRuntime{lifecycle: lifecycle, control: &NeonRuntime{config: NeonRuntimeConfig{Computes: []NeonControlTarget{target}}, client: &http.Client{Transport: neonRoundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"tenant":"` + testTenant + `","timeline":"` + testTimeline + `","status":"running","operation_uuid":"` + strings.Repeat("f", 32) + `"}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}}
	request := durableNeonRequest()
	intent := DurableResourceIntent{ID: strings.Repeat("e", 32), PlatformID: lifecycle.op.PlatformID, PlatformRevision: 1, Component: "compute-primary", Kind: "runtime_component", ExternalKey: externalKey, OwnerOperationID: testOperation}
	if _, _, _, err = runtime.InspectOwnedRecoveryReservation(context.Background(), request, intent); err == nil || !strings.Contains(err.Error(), "ownership changed") {
		t.Fatalf("foreign compute reservation token was accepted: %v", err)
	}
}

func TestInspectOwnedRecoveryClaimDistinguishesAbsentFromForeignCompute(t *testing.T) {
	target := NeonControlTarget{Name: "primary", Origin: "https://compute.example.test", Token: "compute-secret-token"}
	_, identity, err := neonComputeIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	ownerToken := strings.Repeat("d", 32)
	resourceID, err := encodeNeonComputeClaimResourceID(identity, ownerToken)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &durableNeonFixture{op: DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"}}
	status := `{"tenant":"","timeline":"","status":"empty","operation_uuid":"` + ownerToken + `"}`
	runtime := &DurableNeonRuntime{lifecycle: lifecycle, control: &NeonRuntime{config: NeonRuntimeConfig{Computes: []NeonControlTarget{target}}, client: &http.Client{Transport: neonRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(status)), Header: make(http.Header)}, nil
	})}}}
	claim := DurableResourceClaim{PlatformID: lifecycle.op.PlatformID, PlatformRevision: 1, Component: "compute-primary", Kind: "runtime_component", ResourceID: resourceID, ImmutableGeneration: 1, OwnerOperationID: testOperation}
	exists, err := runtime.InspectOwnedRecoveryClaim(context.Background(), durableNeonRequest(), claim)
	if err != nil || exists {
		t.Fatalf("detached prior compute was not classified absent: %v %v", exists, err)
	}
	status = `{"tenant":"` + testTenant + `","timeline":"` + testTimeline + `","status":"running","operation_uuid":"` + strings.Repeat("f", 32) + `"}`
	if _, err = runtime.InspectOwnedRecoveryClaim(context.Background(), durableNeonRequest(), claim); err == nil || !strings.Contains(err.Error(), "ownership changed") {
		t.Fatalf("foreign prior compute was classified absent or untouched: %v", err)
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
	deletePageservers := append([]NeonPageserverRegistration(nil), pageservers...)
	for i := range deletePageservers {
		deletePageservers[i].AvailabilityZone = ""
	}
	deleteSafekeepers := append([]NeonSafekeeperRegistration(nil), safekeepers...)
	for i := range deleteSafekeepers {
		deleteSafekeepers[i].AvailabilityZone = ""
	}
	lifecycle := &durableNeonFixture{op: op, claims: claims, intents: map[string]DurableResourceIntent{}, events: &events}
	attached := false
	registrationsPresent := true
	runtime := &DurableNeonRuntime{lifecycle: lifecycle, control: &NeonRuntime{config: NeonRuntimeConfig{
		StorageController: NeonControlTarget{Name: "storage", Origin: "https://controller.test", Token: "storage-secret-token"},
		Computes:          []NeonControlTarget{{Name: "primary", Origin: "https://compute.test", Token: "compute-secret-token"}},
		Pageservers:       deletePageservers, Safekeepers: deleteSafekeepers, SafekeeperToken: "safekeeper-secret-token", RequestTimeout: time.Second, DeprovisionOnly: true, allowUnqualifiedOwnershipProtocolForTest: true,
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
	if err := runtime.Deprovision(context.Background(), durableNeonDeleteRequest()); err != nil {
		t.Fatalf("deletion did not resume after tenant, timeline, and compute claims were released: %v", err)
	}
	if len(lifecycle.claims) != len(pageservers)+len(safekeepers) {
		t.Fatal("storage registration claims were released before namespace deletion")
	}
	registrationsPresent = false
	if err := runtime.Deprovision(context.Background(), durableNeonDeleteRequest()); err != nil {
		t.Fatalf("authenticated missing storage registrations were not treated as already deleted: %v", err)
	}
	if len(lifecycle.claims) != len(pageservers)+len(safekeepers) {
		t.Fatal("missing storage registration claims were released before namespace deletion")
	}
	attached = true
	if err := runtime.Deprovision(context.Background(), durableNeonDeleteRequest()); err == nil {
		t.Fatal("attached compute without an ownership claim was accepted")
	}
}
