package managedplatform

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

type neonDeprovisionStateFixture struct {
	*durableNeonFixture
	empty        bool
	proofErr     error
	heartbeatErr error
	proofCalls   int
}

func (f *neonDeprovisionStateFixture) NeonProviderStateEmpty(context.Context) (bool, error) {
	f.proofCalls++
	return f.empty, f.proofErr
}

func (f *neonDeprovisionStateFixture) Heartbeat(ctx context.Context) error {
	if err := f.durableNeonFixture.Heartbeat(ctx); err != nil {
		return err
	}
	return f.heartbeatErr
}

func TestDurableNeonDeprovisionRequiresExplicitFencedEmptyState(t *testing.T) {
	unavailable := errors.New("provider unavailable")
	stale := errors.New("delete lease expired")
	for _, test := range []struct {
		name         string
		proof        bool
		empty        bool
		proofErr     error
		heartbeatErr error
		wantRequests int
		wantSuccess  bool
	}{
		{name: "unprovisioned", proof: true, empty: true, wantSuccess: true},
		{name: "native state remains", proof: true, wantRequests: 1},
		{name: "proof unavailable", wantRequests: 1},
		{name: "proof failure", proof: true, empty: true, proofErr: stale},
		{name: "lease lost after proof", proof: true, empty: true, heartbeatErr: stale},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			base := &durableNeonFixture{op: DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 2, Kind: "delete"}, events: &events}
			proof := &neonDeprovisionStateFixture{durableNeonFixture: base, empty: test.empty, proofErr: test.proofErr, heartbeatErr: test.heartbeatErr}
			var lifecycle DurableLifecycle = base
			if test.proof {
				lifecycle = proof
			}
			requests := 0
			runtime := &DurableNeonRuntime{lifecycle: lifecycle, control: &NeonRuntime{
				config: NeonRuntimeConfig{
					StorageController: NeonControlTarget{Name: "storage", Origin: "https://storage.test", Token: "storage-secret-token"},
					Computes:          []NeonControlTarget{{Name: "primary", Origin: "https://compute.test", Token: "compute-secret-token"}},
					RequestTimeout:    time.Second,
				},
				client: &http.Client{Transport: neonRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					requests++
					if request.Method != http.MethodGet || request.URL.Path != "/control/v1/hakopod/ownership" {
						t.Fatalf("unexpected provider request %s %s", request.Method, request.URL.Path)
					}
					return nil, unavailable
				})},
			}}
			err := runtime.Deprovision(context.Background(), durableNeonRequest())
			if (err == nil) != test.wantSuccess || requests != test.wantRequests {
				t.Fatalf("deprovision result: error=%v requests=%d", err, requests)
			}
			if test.proof && proof.proofCalls != 1 {
				t.Fatal("delete did not consult the durable absence proof")
			}
			if test.proofErr != nil || test.heartbeatErr != nil {
				if !errors.Is(err, stale) {
					t.Fatalf("delete did not retain the lease failure: %v", err)
				}
			}
			if test.wantSuccess && (len(events) != 1 || events[0] != "heartbeat") {
				t.Fatalf("unprovisioned delete did not recheck its lease: %v", events)
			}
			if base.claimsCalls != 0 || base.intentsCalls != 0 {
				t.Fatal("revision-local reads were used as absence proof")
			}
		})
	}
}
