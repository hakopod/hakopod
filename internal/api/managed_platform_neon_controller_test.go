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

type neonControllerAuthorityFixture struct {
	calls   int
	applied bool
	err     error
}

func (*neonControllerAuthorityFixture) ReconcileManagedPlatform(context.Context, *store.Store, store.ManagedPlatformOperation) error {
	return nil
}
func (*neonControllerAuthorityFixture) ValidNeonControllerToken(platform string, revision int64, token string) bool {
	return platform == strings.Repeat("a", 32) && revision == 1 && token == strings.Repeat("t", 64)
}
func (a *neonControllerAuthorityFixture) NotifyNeonController(context.Context, *store.Store, string, int64, *managedplatform.NeonAttachNotification, *managedplatform.NeonSafekeeperNotification) (bool, error) {
	a.calls++
	return a.applied, a.err
}

func TestNeonControllerHandlerScopesAndBoundsNotifications(t *testing.T) {
	authority := &neonControllerAuthorityFixture{applied: true}
	server := Server{ManagedPlatformRuntime: authority}
	mux := http.NewServeMux()
	server.registerNeonController(mux)
	base := "/api/v1/internal/neon/storage-controller/" + strings.Repeat("a", 32) + "/1/notify-attach"
	for _, test := range []struct {
		path, token, body string
		status            int
	}{{base, strings.Repeat("t", 64), `{"tenant_id":"t","preferred_az":null,"stripe_size":null,"shards":[]}`, 200}, {base, "wrong", `{}`, 401}, {strings.Replace(base, "/1/", "/2/", 1), strings.Repeat("t", 64), `{}`, 401}, {base, strings.Repeat("t", 64), `{"unknown":true}`, 400}, {base, strings.Repeat("t", 64), `{} {}`, 400}, {base, strings.Repeat("t", 64), strings.Repeat(" ", 16385), 400}} {
		req := httptest.NewRequest(http.MethodPut, test.path, strings.NewReader(test.body))
		req.Header.Set("Authorization", "Bearer "+test.token)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if response.Code != test.status {
			t.Fatalf("status=%d wanted=%d", response.Code, test.status)
		}
	}
	if authority.calls != 1 {
		t.Fatal("invalid callback reached runtime")
	}
	authority.applied = false
	req := httptest.NewRequest(http.MethodPut, base, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("t", 64))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != 503 {
		t.Fatal("pending configuration was acknowledged")
	}
}
