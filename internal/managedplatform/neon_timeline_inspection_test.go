package managedplatform

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNeonTimelineInspectionRequiresCanonicalAbsenceAfterUnavailableDescribe(t *testing.T) {
	for _, test := range []struct {
		name            string
		controlStatus   int
		canonicalStatus int
		canonicalError  bool
		wantAbsent      bool
	}{
		{name: "missing timeline", controlStatus: 503, canonicalStatus: 404, wantAbsent: true},
		{name: "unowned success cannot confirm", controlStatus: 503, canonicalStatus: 200},
		{name: "missing or unavailable tenant", controlStatus: 503, canonicalStatus: 503},
		{name: "unauthorized fallback", controlStatus: 503, canonicalStatus: 401},
		{name: "failed fallback", controlStatus: 503, canonicalError: true},
		{name: "direct absence", controlStatus: 404, wantAbsent: true},
		{name: "unauthorized describe", controlStatus: 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			runtime := &DurableNeonRuntime{control: &NeonRuntime{
				config: NeonRuntimeConfig{StorageController: NeonControlTarget{Name: "storage", Origin: "https://storage.test", Token: "storage-secret-token"}, RequestTimeout: time.Second},
				client: &http.Client{Transport: neonRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer storage-secret-token" || req.Header.Get(neonOwnershipHeader) != "" {
						t.Fatal("timeline inspection did not preserve read-only controller authentication")
					}
					wantPath := "/control/v1/tenant/" + testTenant + "/timeline/" + testTimeline
					status := test.controlStatus
					if calls == 2 {
						wantPath = "/v1/tenant/" + testTenant + "/timeline/" + testTimeline
						status = test.canonicalStatus
					}
					if calls > 2 || req.URL.Path != wantPath || req.URL.RawQuery != "" {
						t.Fatalf("unexpected timeline inspection request: %s", req.URL.Path)
					}
					if calls == 2 && test.canonicalError {
						return nil, errors.New("unavailable fixture transport")
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
				})},
			}}
			exists, token, err := runtime.inspectTimeline(context.Background(), testTenant, testTimeline)
			if exists || token != "" || (err == nil) != test.wantAbsent {
				t.Fatalf("unexpected ownership or absence result: exists=%v tokenPresent=%v err=%v", exists, token != "", err)
			}
			if !test.wantAbsent && test.controlStatus == 503 && !strings.Contains(err.Error(), "HTTP 503") {
				t.Fatalf("fallback replaced the original unavailability: %v", err)
			}
			wantCalls := 1
			if test.controlStatus == 503 {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("unexpected inspection count: %d", calls)
			}
		})
	}
}
