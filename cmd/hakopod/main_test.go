package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

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
