package serverless

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/idlehttp"
	"github.com/hakopod/hakopod/internal/spec"
)

type fakeRuntime struct {
	routes  []Route
	wake    func(context.Context) error
	invalid atomic.Bool
	wakes   atomic.Int32
	sleeps  atomic.Int32
}

func (f *fakeRuntime) Routes(context.Context) ([]Route, error) { return f.routes, nil }
func (f *fakeRuntime) Validate(context.Context, Route) error {
	if f.invalid.Load() {
		return errors.New("stopped")
	}
	return nil
}
func (f *fakeRuntime) WakeHTTPService(ctx context.Context, _ idlehttp.IdleHTTPService) error {
	f.wakes.Add(1)
	if f.wake != nil {
		return f.wake(ctx)
	}
	return nil
}
func (f *fakeRuntime) SleepHTTPService(context.Context, idlehttp.IdleHTTPService) error {
	f.sleeps.Add(1)
	return nil
}
func fixture(t *testing.T, handler http.HandlerFunc) (*Gateway, *fakeRuntime) {
	t.Helper()
	backend := httptest.NewServer(handler)
	t.Cleanup(backend.Close)
	f := &fakeRuntime{routes: []Route{{Target: idlehttp.IdleHTTPService{ApplicationID: "a", Service: "web", Revision: 1, Hosts: []string{"app.example.test"}}, Backend: backend.URL, Settings: spec.Serverless{IdleSeconds: 30, StartupTimeoutSeconds: 5, RequestTimeoutSeconds: 2, MaxConcurrency: 2}}}}
	g := New(f)
	if err := g.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return g, f
}
func invoke(g *Gateway, method string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://app.example.test/callback?value=1", body)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}
func age(g *Gateway) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, e := range g.entries {
		e.gate <- struct{}{}
		e.last = time.Now().Add(-time.Hour)
		<-e.gate
	}
}
func TestColdStartForwardsOriginalBodyExactlyOnce(t *testing.T) {
	var calls atomic.Int32
	var read atomic.Bool
	gate := make(chan struct{})
	entered := make(chan struct{})
	g, f := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		data, _ := io.ReadAll(r.Body)
		if string(data) != "important payload" || r.Method != "POST" || r.URL.RawQuery != "value=1" || r.Host != "app.example.test" {
			t.Error("original request changed")
		}
		w.WriteHeader(201)
	})
	f.wake = func(ctx context.Context) error {
		close(entered)
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	body := &observedReader{Reader: strings.NewReader("important payload"), read: &read}
	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- invoke(g, "POST", body) }()
	<-entered
	if calls.Load() != 0 || read.Load() {
		t.Fatal("body read or forwarded during cold start")
	}
	close(gate)
	if response := <-done; response.Code != 201 || calls.Load() != 1 || f.wakes.Load() != 1 {
		t.Fatal(response.Code, calls.Load(), f.wakes.Load())
	}
	age(g)
	g.SleepIdle(context.Background())
	if f.sleeps.Load() != 1 {
		t.Fatal("idle service did not sleep")
	}
	f.wake = nil
	if response := invoke(g, "POST", strings.NewReader("important payload")); response.Code != 201 || f.wakes.Load() != 2 || calls.Load() != 2 {
		t.Fatal("did not wake after sleep")
	}
}

type observedReader struct {
	io.Reader
	read *atomic.Bool
}

func (r *observedReader) Read(p []byte) (int, error) { r.read.Store(true); return r.Reader.Read(p) }
func TestConcurrencyAndActiveRequestPreventsSleep(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan struct{}, 2)
	g, f := fixture(t, func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-gate; w.WriteHeader(204) })
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := invoke(g, "GET", nil); w.Code != 204 {
				t.Error(w.Code)
			}
		}()
	}
	<-entered
	<-entered
	if w := invoke(g, "GET", nil); w.Code != 429 {
		t.Fatal("overflow not rejected", w.Code)
	}
	age(g)
	g.SleepIdle(context.Background())
	if f.sleeps.Load() != 0 {
		t.Fatal("slept with active requests")
	}
	close(gate)
	wg.Wait()
	if f.wakes.Load() != 1 {
		t.Fatal("concurrent cold requests woke separately")
	}
}
func TestStopsUnknownHostsAndLimits(t *testing.T) {
	var calls atomic.Int32
	g, f := fixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) })
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("GET", "http://unknown.example.test/", nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	f.invalid.Store(true)
	if w := invoke(g, "GET", nil); w.Code != 503 {
		t.Fatal("stopped revision forwarded", w.Code)
	}
	f.invalid.Store(false)
	if w := invoke(g, "POST", strings.NewReader(strings.Repeat("x", (4<<20)+1))); w.Code != 413 {
		t.Fatal(w.Code)
	}
	if calls.Load() != 0 || f.wakes.Load() != 0 {
		t.Fatal("rejected request reached workload")
	}
}
func TestFailedColdStartDoesNotReadBodyOrRetry(t *testing.T) {
	var calls atomic.Int32
	g, f := fixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	f.wake = func(context.Context) error { return errors.New("not ready") }
	var read atomic.Bool
	if w := invoke(g, "POST", &observedReader{Reader: strings.NewReader("secret"), read: &read}); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if read.Load() || calls.Load() != 0 || f.wakes.Load() != 1 {
		t.Fatal("failed cold start forwarded or retried")
	}
}
func TestRequestDeadlineAndAlwaysWarm(t *testing.T) {
	g, f := fixture(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	g.mu.Lock()
	for _, e := range g.entries {
		e.route.Settings.RequestTimeoutSeconds = 1
		e.route.Settings.MinReplicas = 1
	}
	g.mu.Unlock()
	if w := invoke(g, "GET", nil); w.Code != 504 {
		t.Fatal(w.Code)
	}
	age(g)
	g.SleepIdle(context.Background())
	if f.sleeps.Load() != 0 {
		t.Fatal("always warm slept")
	}
}

func TestForwardedChainValidation(t *testing.T) {
	for _, chain := range []string{"", "unknown", "203.0.113.2, invalid", strings.Repeat("1.1.1.1,", 10) + "1.1.1.1"} {
		if validForwardedFor(chain) {
			t.Fatal("invalid forwarded chain accepted")
		}
	}
	if !validForwardedFor("203.0.113.2, 2001:db8::1") {
		t.Fatal("valid chain rejected")
	}
}

func TestRevisionChangedDuringColdStartDoesNotForward(t *testing.T) {
	var calls atomic.Int32
	g, f := fixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	f.wake = func(context.Context) error { f.invalid.Store(true); return nil }
	var read atomic.Bool
	w := invoke(g, "POST", &observedReader{Reader: strings.NewReader("payload"), read: &read})
	if w.Code != 503 || calls.Load() != 0 || read.Load() {
		t.Fatal("request entered a release that changed while starting", w.Code, calls.Load(), read.Load())
	}
}
