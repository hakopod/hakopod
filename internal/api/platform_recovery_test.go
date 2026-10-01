package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/store"
)

const validRecoveryBody = `{"kind":"backup","project":"demo","environment":"development","source_platform_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","destination_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","destination_revision":1,"expected_source_revision":1}`

func recoveryPrincipal() store.Principal {
	return store.Principal{ID: "identity", KeyID: "key", Admin: true, Project: "demo", Environment: "development", Permissions: []string{"deployments:write"}}
}

func recoveryRequest(path, body string, principal store.Principal) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Idempotency-Key", "capacity-guard-1")
	return request.WithContext(context.WithValue(request.Context(), principalKey{}, principal))
}

func recoveryHandlers(server *Server) []struct {
	name, path string
	handler    func(http.ResponseWriter, *http.Request)
} {
	return []struct {
		name, path string
		handler    func(http.ResponseWriter, *http.Request)
	}{{"review", "/api/v1/managed-platform-recovery/reviews", server.reviewPlatformRecovery}, {"accept", "/api/v1/managed-platform-recovery/operations", server.acceptPlatformRecovery}}
}

func TestPlatformRecoveryClosedGateFollowsRequestAndScopeAuthorization(t *testing.T) {
	server := &Server{}
	for _, test := range recoveryHandlers(server) {
		t.Run(test.name+"/malformed", func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, recoveryRequest(test.path, `{}`, recoveryPrincipal()))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("malformed recovery returned %d %s", response.Code, response.Body.String())
			}
		})
		t.Run(test.name+"/unauthorized", func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, recoveryRequest(test.path, validRecoveryBody, store.Principal{ID: "identity", KeyID: "key"}))
			if response.Code != http.StatusForbidden {
				t.Fatalf("unauthorized recovery returned %d %s", response.Code, response.Body.String())
			}
		})
		t.Run(test.name+"/authorized", func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, recoveryRequest(test.path, validRecoveryBody, recoveryPrincipal()))
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "managed_platform_recovery_unavailable") {
				t.Fatalf("closed qualification gate returned %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestManagedCloudCapacityGuardRejectsAuthorizedRecoveryWrites(t *testing.T) {
	server := &Server{Store: &store.Store{ManagedCloud: true}}
	for _, test := range recoveryHandlers(server) {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, recoveryRequest(test.path, validRecoveryBody, recoveryPrincipal()))
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "managed_platform_capacity_unavailable") {
				t.Fatalf("capacity guard returned %d %s", response.Code, response.Body.String())
			}
		})
	}
}
