package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestClientTimeoutsAndCallerCancellation(t *testing.T) {
	arrived := make(chan struct{})
	serverCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-r.Context().Done()
		close(serverCanceled)
	}))
	defer server.Close()
	defer server.CloseClientConnections()

	c, err := newClient(config{URL: server.URL, Key: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type is %T", c.http.Transport)
	}
	if transport.ResponseHeaderTimeout <= 75*time.Second {
		t.Fatalf("response header timeout %s must exceed the 75s managed-operation budget", transport.ResponseHeaderTimeout)
	}
	if c.http.Timeout <= transport.ResponseHeaderTimeout {
		t.Fatalf("response header timeout %s must leave margin within request timeout %s", transport.ResponseHeaderTimeout, c.http.Timeout)
	}
	if c.http.Timeout > 90*time.Second {
		t.Fatalf("request timeout %s exceeds the 90s bounded-policy ceiling", c.http.Timeout)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.request(ctx, http.MethodGet, "/me", nil, "", nil)
	}()
	select {
	case <-arrived:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("request did not reach the server")
	}
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("request succeeded after caller cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not finish after caller cancellation")
	}
	select {
	case <-serverCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not observe caller cancellation")
	}
}

func TestWorkspaceHeaderDoesNotReplaceCredential(t *testing.T) {
	workspace := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Hakopod-Workspace") != workspace || r.Header.Get("Authorization") != "Bearer fixture-session" {
			t.Error("workspace and session authority must both be forwarded")
		}
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	c, err := newClient(config{URL: server.URL, Key: "fixture-session", Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.request(context.Background(), "GET", "/me", nil, "", nil); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"other", workspace + "\r\n", "../../keys"} {
		if _, err = newClient(config{URL: server.URL, Key: "fixture", Workspace: invalid}); err == nil {
			t.Fatal("invalid workspace accepted")
		}
	}
}

func TestCredentialTransport(t *testing.T) {
	for _, raw := range []string{"http://example.com", "https://user:password@example.com", "https://example.com/?key=x", "https://example.com/api", "file:///etc/passwd"} {
		if _, err := newClient(config{URL: raw, Key: "test"}); err == nil {
			t.Errorf("unsafe API origin accepted: %s", raw)
		}
	}
	for _, raw := range []string{"https://control.example.com", "http://127.0.0.1:8080", "http://[::1]:8080", "http://localhost:8080"} {
		if _, err := newClient(config{URL: raw, Key: "test"}); err != nil {
			t.Errorf("valid API origin rejected: %s", raw)
		}
	}
}
func TestFlagsAfterApplication(t *testing.T) {
	want := []string{"--revision", "1", "--wait", "--project", "demo", "shop"}
	if got := reorder([]string{"shop", "--revision", "1", "--wait", "--project", "demo"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}
