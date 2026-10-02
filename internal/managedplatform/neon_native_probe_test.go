//go:build hakopod_native_acceptance && linux

package managedplatform

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type nativeProbeForbiddenLifecycle struct{}

func (nativeProbeForbiddenLifecycle) forbidden() { panic("native probe accessed lifecycle authority") }
func (l nativeProbeForbiddenLifecycle) Operation() DurableOperation {
	l.forbidden()
	return DurableOperation{}
}
func (l nativeProbeForbiddenLifecycle) Heartbeat(context.Context) error { l.forbidden(); return nil }
func (l nativeProbeForbiddenLifecycle) Claims(context.Context, int64) ([]DurableResourceClaim, error) {
	l.forbidden()
	return nil, nil
}
func (l nativeProbeForbiddenLifecycle) Reserve(context.Context, DurableResourceIntent) (DurableResourceIntent, error) {
	l.forbidden()
	return DurableResourceIntent{}, nil
}
func (l nativeProbeForbiddenLifecycle) Intents(context.Context, int64) ([]DurableResourceIntent, error) {
	l.forbidden()
	return nil, nil
}
func (l nativeProbeForbiddenLifecycle) Confirm(context.Context, DurableResourceIntent, DurableResourceClaim) error {
	l.forbidden()
	return nil
}
func (l nativeProbeForbiddenLifecycle) Cancel(context.Context, DurableResourceIntent) error {
	l.forbidden()
	return nil
}
func (l nativeProbeForbiddenLifecycle) Claim(context.Context, DurableResourceClaim) error {
	l.forbidden()
	return nil
}
func (l nativeProbeForbiddenLifecycle) Advance(context.Context, DurableResourceClaim) (DurableResourceClaim, error) {
	l.forbidden()
	return DurableResourceClaim{}, nil
}
func (l nativeProbeForbiddenLifecycle) Verify(context.Context, DurableResourceClaim) error {
	l.forbidden()
	return nil
}
func (l nativeProbeForbiddenLifecycle) Release(context.Context, DurableResourceClaim) error {
	l.forbidden()
	return nil
}

func TestNeonNativeProbeReturnsOnlyExactSanitizedObservations(t *testing.T) {
	tenantID, timelineID := strings.Repeat("1", 32), strings.Repeat("2", 32)
	tenantToken, timelineToken, computeToken := strings.Repeat("3", 32), strings.Repeat("4", 32), strings.Repeat("5", 32)
	members := []neonTimelineMember{{ID: "1", Host: "sk-a"}, {ID: "2", Host: "sk-b"}, {ID: "3", Host: "sk-c"}}
	timelineIdentity, _, _, err := verifiedNeonTimelineIdentity(tenantID, timelineID, 9, members)
	if err != nil {
		t.Fatal(err)
	}
	config := NeonRuntimeConfig{
		StorageController: NeonControlTarget{Name: "storage", Origin: "https://storage.example", Token: "controller-private-secret"},
		Computes:          []NeonControlTarget{{Name: "primary", Origin: "https://compute.example", Token: "compute-private-secret"}},
		Safekeepers:       []NeonSafekeeperRegistration{{Name: "0", NodeID: 1, Generation: 1, Host: "sk-a", AvailabilityZone: "zone-a"}, {Name: "1", NodeID: 2, Generation: 1, Host: "sk-b", AvailabilityZone: "zone-b"}, {Name: "2", NodeID: 3, Generation: 1, Host: "sk-c", AvailabilityZone: "zone-c"}},
		SafekeeperToken:   "safekeeper-private-secret", RequestTimeout: time.Second, RootCAs: x509.NewCertPool(), allowMissingStorageRegistrationsForTest: true,
	}
	_, endpointIdentity, err := neonComputeIdentity(config.Computes[0])
	if err != nil {
		t.Fatal(err)
	}
	response := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	}
	runtime, err := NewDurableNeonRuntime(config, nativeProbeForbiddenLifecycle{})
	if err != nil {
		t.Fatal(err)
	}
	runtime.control.client = &http.Client{Transport: neonRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.Method + " " + request.URL.Path {
		case "GET /control/v1/hakopod/ownership":
			return response(http.StatusOK, `{"protocol":"hakopod-ownership-v1","tenant_delete_protocol":"prepare-v1","mutations":["tenant","timeline","pageserver_registration","safekeeper_registration"]}`)
		case "GET /control/v1/tenant/" + tenantID:
			return response(http.StatusOK, `{"ownership_token":"`+tenantToken+`","ownership_state":"completed"}`)
		case "POST /debug/v1/inspect":
			return response(http.StatusOK, `{"attachment":[7,11]}`)
		case "GET /control/v1/tenant/" + tenantID + "/timeline/" + timelineID:
			return response(http.StatusOK, `{"ownership_token":"`+timelineToken+`","shards":[{"tenant_id":"`+tenantID+`","timeline_id":"`+timelineID+`"}]}`)
		case "GET /v1/status":
			id := map[string]string{"sk-a": "1", "sk-b": "2", "sk-c": "3"}[request.URL.Hostname()]
			return response(http.StatusOK, `{"id":`+id+`}`)
		case "GET /v1/tenant/" + tenantID + "/timeline/" + timelineID:
			return response(http.StatusOK, `{"tenant_id":"`+tenantID+`","timeline_id":"`+timelineID+`","ownership_token":"`+timelineToken+`","mconf":{"generation":9,"members":[{"id":1,"host":"sk-a","pg_port":5454},{"id":2,"host":"sk-b","pg_port":5454},{"id":3,"host":"sk-c","pg_port":5454}],"new_members":null}}`)
		case "GET /status":
			return response(http.StatusOK, `{"status":"running","tenant":"`+tenantID+`","timeline":"`+timelineID+`","operation_uuid":"`+computeToken+`"}`)
		default:
			t.Fatalf("unexpected native probe request %s %s", request.Method, request.URL.String())
			return nil, nil
		}
	})}
	result, err := runtime.ProbeNeonNative(context.Background(), NeonNativeProbeRequest{TenantID: tenantID, TimelineID: timelineID, Claims: []DurableResourceClaim{
		{Component: "tenant", ResourceID: tenantID + "@11:" + tenantToken, ImmutableGeneration: 7},
		{Component: "timeline", ResourceID: timelineIdentity + ":" + timelineToken, ImmutableGeneration: 9},
		{Component: "compute-primary", ResourceID: endpointIdentity + ":" + computeToken, ImmutableGeneration: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OwnershipCapabilityVerified || result.TenantGeneration != 7 || result.TimelineGeneration != 9 || strings.Join(result.ComputeNames, ",") != "primary" {
		t.Fatalf("unexpected native probe result: %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range []string{"controller-private-secret", "compute-private-secret", "safekeeper-private-secret", tenantToken, timelineToken, computeToken} {
		if strings.Contains(text, secret) {
			t.Fatal("native probe result exposed secret material")
		}
	}
	if strings.Contains(text, "wrong_owner_refused") || strings.Contains(text, "wrong_deletion_token_refused") {
		t.Fatal("native probe serialized an unproved ownership refusal")
	}
}

func TestNeonNativeProbeRejectsIncompleteAcceptedClaimsWithoutRequests(t *testing.T) {
	runtime := &DurableNeonRuntime{control: &NeonRuntime{config: NeonRuntimeConfig{Computes: []NeonControlTarget{{Name: "primary"}}}, client: &http.Client{Transport: neonRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("incomplete claim set reached provider")
		return nil, nil
	})}}}
	_, err := runtime.ProbeNeonNative(context.Background(), NeonNativeProbeRequest{TenantID: strings.Repeat("1", 32), TimelineID: strings.Repeat("2", 32)})
	if err == nil {
		t.Fatal("incomplete accepted claims were allowed")
	}
}
