package main

import (
	"reflect"
	"testing"
)

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
