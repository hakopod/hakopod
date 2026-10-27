package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type loggedCall struct {
	Method, Path string
	Body         string
}

func logsFixture(calls *[]loggedCall) *Server {
	app := store.Application{ID: "app-one", Project: "demo", Environment: "development", Spec: spec.Application{Name: "demo", Services: map[string]spec.Service{"web": {Image: "nginx:alpine"}}}}
	request := func(ctx context.Context, method, path string, body any, key string, out any) error {
		encoded, _ := json.Marshal(body)
		*calls = append(*calls, loggedCall{method, path, string(encoded)})
		var result any = map[string]any{"entries": []any{}, "history": map[string]any{"available": true}, "next_cursor": "c2"}
		if path == "/applications/app-one" {
			result = app
		}
		raw, _ := json.Marshal(result)
		return json.Unmarshal(raw, out)
	}
	return New(request, Scope{Project: "demo", Environment: "development"}, false, 1)
}

func lastCall(t *testing.T, calls []loggedCall) loggedCall {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("no API call")
	}
	return calls[len(calls)-1]
}

func TestLogsWithoutHistoryKeepsRequestBody(t *testing.T) {
	var calls []loggedCall
	s := logsFixture(&calls)
	for _, name := range []string{"logs", "read_logs"} {
		calls = nil
		if _, err := s.Call(context.Background(), name, json.RawMessage(`{"application_id":"app-one","service":"web","query":"severity >= ERROR"}`)); err != nil {
			t.Fatal(err)
		}
		got := lastCall(t, calls)
		want := `{"limit":100,"query":"severity \u003e= ERROR","service":"web","since_seconds":3600}`
		if got.Method != "POST" || got.Path != "/applications/app-one/logs/query" || got.Body != want {
			t.Fatalf("%s: %+v", name, got)
		}
	}
}

func TestLogsHistoryFields(t *testing.T) {
	var calls []loggedCall
	s := logsFixture(&calls)
	out, err := s.Call(context.Background(), "logs", json.RawMessage(`{"application_id":"app-one","service":"web","from":"2026-10-01T00:00:00Z","to":"2026-10-01T01:00:00Z","pod":"web-abc","cursor":"c1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.Unmarshal([]byte(lastCall(t, calls).Body), &body)
	want := map[string]any{"history": true, "from": "2026-10-01T00:00:00Z", "to": "2026-10-01T01:00:00Z", "pod": "web-abc", "cursor": "c1", "service": "web", "query": "", "limit": float64(100)}
	if len(body) != len(want) {
		t.Fatalf("body %v", body)
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v, want %v", k, body[k], v)
		}
	}
	if m := out.(map[string]any); m["next_cursor"] != "c2" || m["history"] == nil {
		t.Errorf("result changed: %v", m)
	}
	calls = nil
	s.Call(context.Background(), "logs", json.RawMessage(`{"application_id":"app-one","service":"web","pod":"0b8c1c52-3f4d-4c52-9a52-2f6b1f6a9e10"}`))
	body = nil
	json.Unmarshal([]byte(lastCall(t, calls).Body), &body)
	if body["pod_uid"] != "0b8c1c52-3f4d-4c52-9a52-2f6b1f6a9e10" || body["pod"] != nil || body["history"] != true || body["since_seconds"] != float64(3600) {
		t.Errorf("uid body %v", body)
	}
	if _, err := s.Call(context.Background(), "logs", json.RawMessage(`{"application_id":"app-one","service":"web","from":"yesterday"}`)); err == nil {
		t.Error("bad from accepted")
	}
}

func TestLogSourcesURL(t *testing.T) {
	var calls []loggedCall
	s := logsFixture(&calls)
	if _, err := s.Call(context.Background(), "log_sources", json.RawMessage(`{"application_id":"app-one","from":"2026-10-01T00:00:00Z","limit":50}`)); err != nil {
		t.Fatal(err)
	}
	got := lastCall(t, calls)
	if got.Method != "GET" || got.Path != "/applications/app-one/log-sources?from=2026-10-01T00%3A00%3A00Z&limit=50" {
		t.Fatalf("%+v", got)
	}
	calls = nil
	s.Call(context.Background(), "log_sources", json.RawMessage(`{"application_id":"app-one"}`))
	if got := lastCall(t, calls); got.Path != "/applications/app-one/log-sources" {
		t.Fatalf("%+v", got)
	}
	if _, err := s.Call(context.Background(), "log_sources", json.RawMessage(`{"application_id":"app-one","limit":501}`)); err == nil {
		t.Error("limit 501 accepted")
	}
}

func TestPrintLogToolSchemas(t *testing.T) {
	for _, tool := range New(nil, Scope{}, false, 1).Tools() {
		m := tool.(map[string]any)
		if m["name"] == "logs" || m["name"] == "log_sources" {
			raw, _ := json.MarshalIndent(m["inputSchema"], "", " ")
			t.Log(m["name"], string(raw))
		}
	}
}

func unavailableServer(calls *[]loggedCall, fail bool) *Server {
	app := store.Application{ID: "app-one", Project: "demo", Environment: "development", Spec: spec.Application{Name: "demo", Services: map[string]spec.Service{"web": {Image: "nginx:alpine"}}}}
	request := func(ctx context.Context, method, path string, body any, key string, out any) error {
		encoded, _ := json.Marshal(body)
		*calls = append(*calls, loggedCall{method, path, string(encoded)})
		var result any = app
		switch {
		case fail && path != "/applications/app-one":
			return errors.New("API returned 500")
		case strings.HasSuffix(path, "/log-sources") || strings.Contains(path, "/log-sources?"):
			result = map[string]any{"available": false, "message": "Log history is not available on this installation. Collector is off.", "items": []any{}}
		case strings.HasSuffix(path, "/logs/query"):
			if body.(map[string]any)["history"] == true {
				result = map[string]any{"entries": []any{}, "history": map[string]any{"available": false, "message": "Log history is not available on this installation. Collector is off."}}
			} else {
				result = map[string]any{"entries": []any{map[string]any{"message": "live line"}}}
			}
		}
		raw, _ := json.Marshal(result)
		return json.Unmarshal(raw, out)
	}
	return New(request, Scope{Project: "demo", Environment: "development"}, false, 1)
}

func TestLogsHistoryUnavailableFallsBackToLive(t *testing.T) {
	var calls []loggedCall
	s := unavailableServer(&calls, false)
	from := time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339)
	out, err := s.Call(context.Background(), "logs", json.RawMessage(`{"application_id":"app-one","service":"web","query":"x","from":"`+from+`","pod":"web-abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	var live map[string]any
	json.Unmarshal([]byte(lastCall(t, calls).Body), &live)
	if live["history"] != nil || live["since_seconds"] != float64(86400) || live["pod"] != "web-abc" || live["query"] != "x" || live["cursor"] != nil {
		t.Errorf("live body %v", live)
	}
	m := out.(map[string]any)
	fb, _ := m["history_fallback"].(map[string]any)
	if fb["mode"] != "live" || fb["reason"] != "Log history is not available on this installation. Collector is off." || fb["note"] != historyFallbackNote || m["entries"] == nil {
		t.Errorf("result %v", m)
	}
	calls = nil
	out, _ = s.Call(context.Background(), "logs", json.RawMessage(`{"application_id":"app-one","service":"web","pod":"0b8c1c52-3f4d-4c52-9a52-2f6b1f6a9e10"}`))
	live = nil
	json.Unmarshal([]byte(lastCall(t, calls).Body), &live)
	note := out.(map[string]any)["history_fallback"].(map[string]any)["note"].(string)
	if live["pod"] != nil || !strings.HasSuffix(note, "Pod UIDs only apply to stored history; showing all pods.") {
		t.Errorf("uid: %v %s", live, note)
	}
	raw, _ := json.MarshalIndent(out, "", " ")
	t.Log(string(raw))
}

func TestLogsHistoryServerErrorDoesNotFallBack(t *testing.T) {
	var calls []loggedCall
	s := unavailableServer(&calls, true)
	if _, err := s.Call(context.Background(), "logs", json.RawMessage(`{"application_id":"app-one","service":"web","history":true}`)); err == nil || len(calls) != 2 {
		t.Errorf("err %v calls %d", err, len(calls))
	}
}

func TestLogSourcesUnavailableMessage(t *testing.T) {
	var calls []loggedCall
	out, err := unavailableServer(&calls, false).Call(context.Background(), "log_sources", json.RawMessage(`{"application_id":"app-one"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(map[string]any)["message"]; got != "Log history is not available on this installation. Collector is off. Past pods are only listed when log history is enabled." {
		t.Errorf("%v", got)
	}
}
