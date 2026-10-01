package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
)

type recordingManagedPlatformPlanner struct{ calls int }

func (p *recordingManagedPlatformPlanner) PlanManagedPlatform(context.Context, store.Principal, store.ManagedPlatform, int64, string) (managedplatform.Plan, error) {
	p.calls++
	return managedplatform.Plan{}, nil
}

func (p *recordingManagedPlatformPlanner) SealManagedPlatformSnapshot(context.Context, store.Principal, store.ManagedPlatform, managedplatform.Plan, int64, string) ([]byte, error) {
	p.calls++
	return nil, nil
}

func TestManagedPlatformReviewRejectsScopeBeforePlanning(t *testing.T) {
	planner := &recordingManagedPlatformPlanner{}
	server := &Server{ManagedPlatformPlanner: planner}
	body := `{"project":"other","environment":"development","expected_revision":0,"kind":"create","spec":{"schema_version":1,"name":"fixture","kind":"supabase"}}`
	request := httptest.NewRequest("POST", "/api/v1/managed-platforms/reviews", strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), principalKey{}, store.Principal{ID: "identity", KeyID: "key"}))
	response := httptest.NewRecorder()
	server.reviewManagedPlatform(response, request)
	if response.Code != 403 {
		t.Fatalf("expected forbidden, got %d", response.Code)
	}
	if planner.calls != 0 {
		t.Fatalf("planner called %d times for unauthorized scope", planner.calls)
	}
}

func TestManagedPlatformListRejectsInvalidScope(t *testing.T) {
	for _, query := range []string{"project=demo", "environment=development", "project=&environment=", "project=bad%20scope&environment=development", "project=demo&project=other&environment=development"} {
		t.Run(query, func(t *testing.T) {
			server := &Server{}
			request := httptest.NewRequest("GET", "/api/v1/managed-platforms?"+query, nil)
			request = request.WithContext(context.WithValue(request.Context(), principalKey{}, store.Principal{Project: "demo", Environment: "development"}))
			response := httptest.NewRecorder()
			server.managedPlatforms(response, request)
			if response.Code != 400 {
				t.Fatalf("expected invalid scope, got %d", response.Code)
			}
		})
	}
}

func TestManagedCloudCapacityGuardRejectsPlatformWritesBeforePlanning(t *testing.T) {
	for _, kind := range []string{"create", "update"} {
		for _, name := range []string{"review", "accept"} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				planner := &recordingManagedPlatformPlanner{}
				server := &Server{Store: &store.Store{ManagedCloud: true}, ManagedPlatformPlanner: planner}
				body := `{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","project":"demo","environment":"development","expected_revision":1,"kind":"` + kind + `","spec":{"schema_version":1,"name":"fixture","kind":"supabase"}}`
				if kind == "create" {
					body = `{"project":"demo","environment":"development","expected_revision":0,"kind":"create","spec":{"schema_version":1,"name":"fixture","kind":"supabase"}}`
				}
				request := httptest.NewRequest("POST", "/api/v1/managed-platforms/"+name+"s", strings.NewReader(body))
				request.Header.Set("Idempotency-Key", "capacity-guard-1")
				request = request.WithContext(context.WithValue(request.Context(), principalKey{}, store.Principal{ID: "identity", KeyID: "key", Admin: true, Project: "demo", Environment: "development", Permissions: []string{"deployments:write"}}))
				response := httptest.NewRecorder()
				if name == "review" {
					server.reviewManagedPlatform(response, request)
				} else {
					server.acceptManagedPlatform(response, request)
				}
				if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "managed_platform_capacity_unavailable") {
					t.Fatalf("capacity guard returned %d %s", response.Code, response.Body.String())
				}
				if planner.calls != 0 {
					t.Fatalf("planner called %d times before capacity admission", planner.calls)
				}
			})
		}
	}
}
