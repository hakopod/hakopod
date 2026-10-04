package managedplatform

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDurableNeonAbsentTenantDeletionRequiresExactOwnerResponse(t *testing.T) {
	owner := strings.Repeat("a", 32)
	identity, err := encodeNeonOwnedResourceID(testTenant+"@11", owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		status    int
		body      string
		terminal  bool
		wantError bool
	}{
		{name: "terminal owner tombstone", status: 200, body: "null", terminal: true},
		{name: "preflight requires continuation", status: 200, body: `{"schema_version":1,"digest":"` + strings.Repeat("a", 64) + `"}`},
		{name: "foreign owner", status: 409, body: "null", wantError: true},
		{name: "unauthenticated", status: 401, body: "null", wantError: true},
		{name: "unqualified missing endpoint", status: 404, body: "null", wantError: true},
		{name: "upstream unavailable", status: 503, body: "null", wantError: true},
		{name: "status only", status: 200, wantError: true},
		{name: "empty object", status: 200, body: `{}`, wantError: true},
		{name: "wrong schema", status: 200, body: `{"schema_version":2,"digest":"` + strings.Repeat("a", 64) + `"}`, wantError: true},
		{name: "missing digest", status: 200, body: `{"schema_version":1}`, wantError: true},
		{name: "invalid digest", status: 200, body: `{"schema_version":1,"digest":"` + strings.Repeat("A", 64) + `"}`, wantError: true},
		{name: "extra fence", status: 200, body: `{"schema_version":1,"digest":"` + strings.Repeat("a", 64) + `","delete_token":"` + owner + `"}`, wantError: true},
		{name: "duplicate schema", status: 200, body: `{"schema_version":1,"schema_version":1,"digest":"` + strings.Repeat("a", 64) + `"}`, wantError: true},
		{name: "duplicate digest", status: 200, body: `{"schema_version":1,"digest":"` + strings.Repeat("a", 64) + `","digest":"` + strings.Repeat("b", 64) + `"}`, wantError: true},
		{name: "malformed object", status: 200, body: `{"schema_version":1,"digest":`, wantError: true},
		{name: "trailing response", status: 200, body: `null {}`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			runtime := &DurableNeonRuntime{control: &NeonRuntime{config: NeonRuntimeConfig{
				StorageController: NeonControlTarget{Name: "storage", Origin: "https://storage.test", Token: "development-token"}, RequestTimeout: time.Second,
			}, client: &http.Client{Transport: neonRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				if request.Method != http.MethodDelete || request.URL.RequestURI() != "/v1/tenant/"+testTenant+"?validate_only=true" || request.Header.Get(neonOwnershipHeader) != owner || request.Header.Get("Authorization") != "Bearer development-token" || request.Header.Get(neonDeletionHeader) != "" {
					t.Fatal("absent tenant verification lost its exact owner or attempted a mutation")
				}
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body)), Header: make(http.Header)}, nil
			})}}}
			terminal, err := runtime.inspectAbsentTenantDeletion(context.Background(), testTenant, DurableResourceClaim{ResourceID: identity})
			if (err != nil) != test.wantError || terminal != test.terminal || requests != 1 {
				t.Fatalf("unexpected absent tenant result: terminal=%v error=%v requests=%d", terminal, err, requests)
			}
		})
	}
}
