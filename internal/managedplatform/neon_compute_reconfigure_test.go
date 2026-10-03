package managedplatform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNeonComputeRoutingDigestMatchesRustCanonicalBytes(t *testing.T) {
	canonical := `{"pageserver_connstring":"postgresql://pageserver-0:6400?sslmode=verify-full&sslrootcert=/ca.crt","safekeeper_connstrings":["safekeeper-0:5454","safekeeper-1:5454","safekeeper-2:5454"],"safekeepers_generation":4,"tenant_id":"11111111111111111111111111111111","timeline_id":"22222222222222222222222222222222"}`
	raw := json.RawMessage(`{"spec":` + canonical + `}`)
	got, err := neonComputeRoutingDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(canonical))
	if got != hex.EncodeToString(want[:]) {
		t.Fatal("Rust/Go canonical routing differs")
	}
}

func TestNeonComputeReconfigureCASAndObservation(t *testing.T) {
	for _, scenario := range []string{"running", "already-applied", "empty-replay", "missing-authority", "pending", "foreign-before", "foreign-after", "changed-digest-after", "wrong-routing", "stale-cas"} {
		t.Run(scenario, func(t *testing.T) {
			platform, tenant, timeline, state, raw := neonControllerFixture()
			raw, err := BindNeonControllerRouting(raw, platform, tenant, timeline, 2, state)
			if err != nil {
				t.Fatal(err)
			}
			token := strings.Repeat("e", 32)
			raw, err = bindNeonComputeOwnership(raw, token)
			if err != nil {
				t.Fatal(err)
			}
			desired, err := neonComputeRoutingDigest(raw)
			if err != nil {
				t.Fatal(err)
			}
			oldHash, newHash := strings.Repeat("a", 64), strings.Repeat("b", 64)
			initial := map[string]string{"status": "running", "tenant": tenant, "timeline": timeline, "operation_uuid": token, "spec_sha256": oldHash, "authorized_spec_sha256": oldHash, "routing_sha256": strings.Repeat("c", 64)}
			switch scenario {
			case "already-applied":
				initial["routing_sha256"] = desired
			case "empty-replay", "missing-authority":
				initial["status"] = "empty"
				initial["tenant"] = ""
				initial["timeline"] = ""
				initial["spec_sha256"] = ""
				initial["routing_sha256"] = ""
				if scenario == "missing-authority" {
					initial["operation_uuid"] = ""
					initial["authorized_spec_sha256"] = ""
				}
			case "pending":
				initial["status"] = "configuration"
			case "foreign-before":
				initial["operation_uuid"] = strings.Repeat("f", 32)
			}
			var configured atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/configure" {
					configured.Add(1)
					if r.Header.Get("hakopod-expected-spec-sha256") != oldHash || r.Header.Get(neonOwnershipHeader) != token {
						t.Error("configure omitted exact ownership/CAS fence")
					}
					if scenario == "stale-cas" {
						w.WriteHeader(http.StatusConflict)
						return
					}
				}
				response := map[string]string{"status": "running", "tenant": tenant, "timeline": timeline, "operation_uuid": token, "spec_sha256": newHash, "authorized_spec_sha256": newHash, "routing_sha256": desired}
				if scenario == "foreign-after" && r.URL.Path == "/status" {
					response["operation_uuid"] = strings.Repeat("f", 32)
				}
				if scenario == "changed-digest-after" && r.URL.Path == "/status" {
					response["spec_sha256"] = oldHash
					response["authorized_spec_sha256"] = oldHash
				}
				if scenario == "wrong-routing" {
					response["routing_sha256"] = strings.Repeat("c", 64)
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			runtime := &NeonRuntime{config: NeonRuntimeConfig{RequestTimeout: time.Second}, client: server.Client()}
			body, _ := json.Marshal(initial)
			err = runtime.configureOwnedCompute(context.Background(), NeonControlTarget{Origin: server.URL}, raw, token, tenant, timeline, body, false)
			wantSuccess := scenario == "running" || scenario == "already-applied" || scenario == "empty-replay"
			if (err == nil) != wantSuccess {
				t.Fatalf("unexpected configure result: %v", err)
			}
			wantConfigure := scenario != "already-applied" && scenario != "missing-authority" && scenario != "pending" && scenario != "foreign-before"
			if (configured.Load() == 1) != wantConfigure {
				t.Fatal("unexpected external mutation")
			}
		})
	}
}

func TestNeonComputeReconfigureTimeoutRetryObservesCompletedApply(t *testing.T) {
	platform, tenant, timeline, state, raw := neonControllerFixture()
	raw, err := BindNeonControllerRouting(raw, platform, tenant, timeline, 2, state)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("e", 32)
	raw, err = bindNeonComputeOwnership(raw, token)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := neonComputeRoutingDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	oldHash, newHash := strings.Repeat("a", 64), strings.Repeat("b", 64)
	var calls atomic.Int32
	finished := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/configure" {
			calls.Add(1)
			<-time.After(60 * time.Millisecond)
			close(finished)
			return
		}
	}))
	defer server.Close()
	runtime := &NeonRuntime{config: NeonRuntimeConfig{RequestTimeout: 30 * time.Millisecond}, client: server.Client()}
	initial, _ := json.Marshal(map[string]string{"status": "running", "tenant": tenant, "timeline": timeline, "operation_uuid": token, "spec_sha256": oldHash, "authorized_spec_sha256": oldHash, "routing_sha256": strings.Repeat("c", 64)})
	target := NeonControlTarget{Origin: server.URL}
	if runtime.configureOwnedCompute(context.Background(), target, raw, token, tenant, timeline, initial, false) == nil {
		t.Fatal("timed-out configure acknowledged")
	}
	<-finished
	completed, _ := json.Marshal(map[string]string{"status": "running", "tenant": tenant, "timeline": timeline, "operation_uuid": token, "spec_sha256": newHash, "authorized_spec_sha256": newHash, "routing_sha256": desired})
	if err = runtime.configureOwnedCompute(context.Background(), target, raw, token, tenant, timeline, completed, false); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("completed configuration was reapplied after timeout")
	}
}

func TestNeonComputeLifecycleDoesNotRollbackUncommittedCallbackRouting(t *testing.T) {
	// The provider has applied a callback while its database transaction still
	// exposes the prior routing snapshot to ordinary readers. Provision must
	// observe ownership without consulting that stale resolver or configuring.
	events := []string{}
	op := DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "create"}
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/control/v1/tenant/" + testTenant:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case "/debug/v1/inspect":
			_ = json.NewEncoder(w).Encode(map[string]any{"attachment": []int{7, 11}})
		case "/control/v1/tenant/" + testTenant + "/timeline/" + testTimeline:
			_ = json.NewEncoder(w).Encode(map[string]any{"shards": []map[string]string{{"tenant_id": testTenant, "timeline_id": testTimeline}}})
		default:
			t.Error("unexpected storage mutation")
			w.WriteHeader(500)
		}
	}))
	defer storage.Close()
	var mutations, staleReads atomic.Int32
	token := strings.Repeat("e", 32)
	callbackApplied := make(chan struct{})
	close(callbackApplied)
	compute := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-callbackApplied
		if r.URL.Path != "/status" {
			mutations.Add(1)
			w.WriteHeader(409)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "running", "tenant": testTenant, "timeline": testTimeline, "operation_uuid": token, "spec_sha256": strings.Repeat("b", 64), "authorized_spec_sha256": strings.Repeat("b", 64), "routing_sha256": strings.Repeat("c", 64)})
	}))
	defer compute.Close()
	lifecycle := &durableNeonFixture{op: op, claims: map[string]DurableResourceClaim{}, intents: map[string]DurableResourceIntent{}, events: &events}
	runtime := durableNeonRuntimeForTest(t, storage, compute, lifecycle)
	_, identity, err := neonComputeIdentity(runtime.control.config.Computes[0])
	if err != nil {
		t.Fatal(err)
	}
	resource, err := encodeNeonComputeClaimResourceID(identity, token)
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range []DurableResourceClaim{{Component: "tenant", Kind: "neon_tenant", ResourceID: testTenant + "@11", ImmutableGeneration: 7}, {Component: "timeline", Kind: "neon_timeline", ResourceID: strings.Repeat("a", 64), ImmutableGeneration: 13}, {Component: "compute-primary", Kind: "runtime_component", ResourceID: resource, ImmutableGeneration: 1}} {
		claim.PlatformID = op.PlatformID
		claim.PlatformRevision = 1
		claim.OwnerOperationID = op.ID
		lifecycle.claims[claim.Component] = claim
	}
	runtime.control.config.ResolveComputeConfig = func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		staleReads.Add(1)
		return durableNeonRequest().ComputeConfig["primary"], nil
	}
	result, err := runtime.Provision(context.Background(), durableNeonRequest())
	if err != nil || !result.Complete {
		t.Fatalf("owned lifecycle did not complete: %v", err)
	}
	if mutations.Load() != 0 || staleReads.Load() != 0 {
		t.Fatal("lifecycle could roll back a callback using stale database routing")
	}
}
