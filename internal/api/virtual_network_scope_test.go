package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/store"
)

func TestVirtualNetworkPlanNeedsReadAndApplyNeedsAdministrator(t *testing.T) {
	reader := store.Principal{Project: "demo", Environment: "development", Permissions: []string{"deployments:read"}, IdentityPermissions: []string{"deployments:read"}}
	call := func(handler http.HandlerFunc, method string) int {
		// An empty network reaches parsing only after the scope check passes.
		r := httptest.NewRequest(method, "/api/v1/virtual-networks", strings.NewReader(`{"project":"demo","environment":"development"}`))
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, reader))
		w := httptest.NewRecorder()
		handler(w, r)
		return w.Code
	}
	s := &Server{}
	if code := call(s.planVirtualNetwork, "POST"); code != 400 {
		t.Fatalf("plan with read access: HTTP %d, want 400 from parsing after the scope check", code)
	}
	if code := call(s.putVirtualNetwork, "POST"); code != 403 {
		t.Fatalf("apply with read access: HTTP %d, want 403", code)
	}
}
