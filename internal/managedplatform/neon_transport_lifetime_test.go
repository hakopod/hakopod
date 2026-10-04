package managedplatform

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDurableNeonRetriesReleaseProviderConnections(t *testing.T) {
	for _, kind := range []string{"create", "delete"} {
		t.Run(kind, func(t *testing.T) {
			closed := make(chan struct{}, 32)
			var requests atomic.Int64
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				http.Error(w, "provider temporarily unavailable", http.StatusServiceUnavailable)
			}))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateClosed {
					closed <- struct{}{}
				}
			}
			server.StartTLS()
			defer server.Close()
			for attempt := 0; attempt < 12; attempt++ {
				events := []string{}
				lifecycle := &durableNeonFixture{
					op:     DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 2, Kind: kind},
					claims: map[string]DurableResourceClaim{}, intents: map[string]DurableResourceIntent{}, events: &events,
				}
				runtime := durableNeonRuntimeForTest(t, server, server, lifecycle)
				runtime.control.config.allowUnqualifiedOwnershipProtocolForTest = false
				var err error
				if kind == "create" {
					_, err = runtime.Provision(context.Background(), durableNeonRequest())
				} else {
					err = runtime.Deprovision(context.Background(), durableNeonDeleteRequest())
				}
				if err == nil || requests.Load() != int64(attempt+1) {
					t.Fatal("retry did not reach the unavailable provider exactly once")
				}
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("completed retry retained its provider connection")
				}
			}
		})
	}
}
