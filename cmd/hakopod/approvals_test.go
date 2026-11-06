package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const approvalBody = `{"error":{"code":"approval_required","message":"Agent policy \"Prod\" requires approval. Approval apr_1 expires at 2099-01-01T00:00:00Z. Retry the identical request with the header Hakopod-Approval: apr_1 after it is approved."}}`

// approvalServer refuses the first POST, reports states in order, then accepts the retry.
func approvalServer(t *testing.T, states ...string) (*client, func() [][]byte, func() []string) {
	var mu sync.Mutex
	var bodies [][]byte
	var headers []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "GET" && r.URL.Path == "/api/v1/agent-approvals/apr_1" {
			state := states[0]
			if len(states) > 1 {
				states = states[1:]
			}
			io.WriteString(w, `{"id":"apr_1","state":"`+state+`"}`)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, raw)
		headers = append(headers, r.Header.Get("Hakopod-Approval"))
		if r.Header.Get("Hakopod-Approval") == "" {
			w.Header().Set("Hakopod-Approval-Id", "apr_1")
			w.Header().Set("Hakopod-Approval-Expires", "2099-01-01T00:00:00Z")
			w.WriteHeader(403)
			io.WriteString(w, approvalBody)
			return
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(server.Close)
	return &client{url: server.URL, http: server.Client()},
		func() [][]byte { mu.Lock(); defer mu.Unlock(); return bodies },
		func() []string { mu.Lock(); defer mu.Unlock(); return headers }
}

func interactive(t *testing.T, on bool) {
	oldI, oldP := approvalInteractive, approvalPoll
	approvalInteractive, approvalPoll = func() bool { return on }, time.Millisecond
	t.Cleanup(func() { approvalInteractive, approvalPoll = oldI, oldP })
}

func TestApprovalRequiredNonInteractive(t *testing.T) {
	interactive(t, false)
	c, bodies, _ := approvalServer(t, "approved")
	err := c.request(context.Background(), "POST", "/deployments", map[string]any{"a": 1}, "", nil)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != 3 || !strings.Contains(err.Error(), "apr_1") {
		t.Fatalf("%T %v", err, err)
	}
	var approval *approvalRequiredError
	if !errors.As(err, &approval) || approval.ID != "apr_1" || approval.ExpiresAt.Year() != 2099 {
		t.Fatal(approval)
	}
	if len(bodies()) != 1 {
		t.Fatal("non-interactive run retried")
	}
}

func TestApprovalInteractiveWaitRetriesOnce(t *testing.T) {
	interactive(t, true)
	c, bodies, headers := approvalServer(t, "pending", "pending", "approved")
	var out map[string]any
	if err := c.request(context.Background(), "POST", "/deployments", map[string]any{"a": 1, "b": "x"}, "", &out); err != nil {
		t.Fatal(err)
	}
	b, h := bodies(), headers()
	if len(b) != 2 || !bytes.Equal(b[0], b[1]) || h[0] != "" || h[1] != "apr_1" || out["ok"] != true {
		t.Fatalf("%q %q %v", b, h, out)
	}
}

func TestApprovalInteractiveDeniedOrExpired(t *testing.T) {
	for _, state := range []string{"denied", "expired"} {
		interactive(t, true)
		c, bodies, _ := approvalServer(t, "pending", state)
		err := c.request(context.Background(), "POST", "/deployments", map[string]any{}, "", nil)
		if err == nil || err.Error() != "Approval apr_1 was "+state+"." || len(bodies()) != 1 {
			t.Fatal(state, err)
		}
	}
}

func TestApprovalWaitStopsOnCancel(t *testing.T) {
	interactive(t, true)
	c, _, _ := approvalServer(t, "pending")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.request(ctx, "POST", "/deployments", map[string]any{}, "", nil); err == nil {
		t.Fatal("wait ignored cancellation")
	}
}
