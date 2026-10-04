//go:build !hakopod_native_acceptance || !linux

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestShippingAPIHasNoNativeProbeRoute(t *testing.T) {
	mux := http.NewServeMux()
	(&Server{}).registerNativeProbeRoutes(mux)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/managed-platforms/fixture/native-probe", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/managed-platform-recovery-operations/fixture/native-receipt", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/managed-platform-recovery-operations/fixture/native-cancellation-receipt", nil),
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, request)
		if w.Code != http.StatusNotFound {
			t.Fatalf("shipping server exposed a native provider route: %d", w.Code)
		}
	}
}
