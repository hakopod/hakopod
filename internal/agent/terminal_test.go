package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestTerminalIndependentOptIns(t *testing.T) {
	called := false
	r := func(context.Context, string, string, any, string, any) error { called = true; return nil }
	for _, options := range []Options{{AllowDeploy: true}, {AllowExec: true}, {AllowTerminal: true}} {
		s := NewWithOptions(r, Scope{"p", "dev"}, options, 1)
		if _, err := s.Call(context.Background(), "terminal_open", json.RawMessage(`{}`)); err == nil || called {
			t.Fatal("terminal opt-in bypass")
		}
	}
}

func TestHostOnlyToolBoundary(t *testing.T) {
	s := NewWithOptions(nil, Scope{}, Options{HostOnly: true, AllowHostTerminal: true}, 1)
	tools := s.Tools()
	if len(tools) != 5 {
		t.Fatalf("host-only tools: %d", len(tools))
	}
	for _, tool := range tools {
		name := tool.(map[string]any)["name"].(string)
		if len(name) < 14 || name[:14] != "host_terminal_" {
			t.Fatalf("leaked tool %s", name)
		}
	}
	for _, name := range []string{"api_call", "api_operations", "database_query", "pod_exec", "deployment_events"} {
		if _, err := s.Call(context.Background(), name, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("allowed %s", name)
		}
	}
}
