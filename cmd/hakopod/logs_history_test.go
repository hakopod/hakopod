package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseSpanAndWhen(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{"30s": 30 * time.Second, "90m": 90 * time.Minute, "2h": 2 * time.Hour, "3d": 72 * time.Hour, "1h30m": 90 * time.Minute, "1.5h": 90 * time.Minute} {
		if got, err := parseSpan(in); err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "1w", "h", "-2h", "0m", "1d2h"} {
		if _, err := parseSpan(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if got, _ := parseWhen("2h", now); !got.Equal(now.Add(-2 * time.Hour)) {
		t.Errorf("relative: %v", got)
	}
	if got, _ := parseWhen("2026-10-01T08:00:00+02:00", now); !got.Equal(time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC)) {
		t.Errorf("rfc3339: %v", got)
	}
	if _, err := parseWhen("tomorrow", now); err == nil {
		t.Error("garbage accepted")
	}
}

func TestHistoryBody(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	body, err := historyBody(historyOptions{Service: "web", Pod: "web-abc", From: "3d", To: "2026-10-09T00:00:00Z"}, now)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"service": "web", "query": "", "history": true, "from": "2026-10-07T12:00:00Z", "to": "2026-10-09T00:00:00Z", "pod": "web-abc"}
	if len(body) != len(want) {
		t.Fatalf("%v", body)
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v want %v", k, body[k], v)
		}
	}
	uid := "0b8c1c52-3f4d-4c52-9a52-2f6b1f6a9e10"
	body, _ = historyBody(historyOptions{Service: "web", Pod: uid, Since: "30d"}, now)
	if body["pod_uid"] != uid || body["pod"] != nil || body["from"] != "2026-09-10T12:00:00Z" {
		t.Errorf("%v", body)
	}
	if _, err := historyBody(historyOptions{Service: "web", Since: "31d"}, now); err == nil {
		t.Error("31d accepted")
	}
	if _, err := historyBody(historyOptions{Service: "web", From: "1h", To: "2h"}, now); err == nil {
		t.Error("to before from accepted")
	}
}

func TestPrintHistoryLogsFollowsCursor(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		if b["cursor"] == nil {
			w.Write([]byte(`{"entries":[{"timestamp":"2026-10-01T00:00:01Z","pod":"web-a","restart":1,"message":"one"}],"next_cursor":"c1","history":{"available":true,"gaps":[{"pod_uid":"u","pod_name":"web-a","seconds":5,"note":"Log file rotated before Hakopod read it."}]}}`))
			return
		}
		w.Write([]byte(`{"entries":[{"timestamp":"2026-10-01T00:00:02Z","pod":"web-a","restart":1,"message":"two"}],"next_cursor":"","history":{"available":true,"scan_limited":true}}`))
	}))
	defer server.Close()
	c, _ := newClient(config{URL: server.URL, Key: "fixture"})
	var out, errOut bytes.Buffer
	if err := printHistoryLogs(context.Background(), c, "app-one", map[string]any{"history": true, "service": "web"}, 1000, liveOptions{}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[1]["cursor"] != "c1" || bodies[0]["limit"] != float64(500) || bodies[0]["history"] != true {
		t.Fatalf("%v", bodies)
	}
	if out.String() != "2026-10-01T00:00:01Z web-a r1 one\n2026-10-01T00:00:02Z web-a r1 two\n" {
		t.Errorf("stdout %q", out.String())
	}
	if !strings.Contains(errOut.String(), "up to 5 seconds missing") || !strings.Contains(errOut.String(), "search stopped") {
		t.Errorf("stderr %q", errOut.String())
	}
}

func TestPrintLogPodsTable(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/applications/app-one/log-sources" {
			t.Errorf("path %s", r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{"available":true,"retention_days":7,"items":[
{"pod_name":"web-6d9f-abcde","service":"web","revision":4,"started_at":"2026-10-09T08:00:00Z","ended_at":"2026-10-09T09:30:00Z","end_reason":"oom_killed","gap_seconds":5,"current":false},
{"pod_name":"web-7c1a-xyz","service":"web","revision":5,"started_at":"2026-10-09T09:30:00Z","ended_at":null,"end_reason":"","gap_seconds":0,"current":true}]}`))
	}))
	defer server.Close()
	c, _ := newClient(config{URL: server.URL, Key: "fixture"})
	var out, errOut bytes.Buffer
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	if err := printLogPods(context.Background(), c, "app-one", "7d", now, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if gotQuery != "from=2026-10-03T00%3A00%3A00Z&limit=500" {
		t.Errorf("query %s", gotQuery)
	}
	want := `POD             SERVICE  REVISION  STARTED               ENDED                 REASON      GAP
web-6d9f-abcde  web      4         2026-10-09T08:00:00Z  2026-10-09T09:30:00Z  oom_killed  5s
web-7c1a-xyz    web      5         2026-10-09T09:30:00Z  running               -           -
`
	if out.String() != want || errOut.Len() != 0 {
		t.Errorf("table:\n%s\nstderr %q", out.String(), errOut.String())
	}
}

func TestPrintHistoryLogsSkipsEmptyPages(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1, 2:
			w.Write([]byte(`{"entries":[],"next_cursor":"c"}`))
		case 3:
			w.Write([]byte(`{"entries":[{"timestamp":"2026-10-01T00:00:01Z","pod":"web-a","restart":0,"message":"boom"}],"next_cursor":""}`))
		}
	}))
	defer server.Close()
	c, _ := newClient(config{URL: server.URL, Key: "fixture"})
	var out, errOut bytes.Buffer
	if err := printHistoryLogs(context.Background(), c, "app-one", map[string]any{"history": true}, 1000, liveOptions{}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || !strings.Contains(out.String(), "boom") {
		t.Errorf("calls %d out %q", calls, out.String())
	}
	calls = 0
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"entries":[],"next_cursor":"c"}`))
	})
	out.Reset()
	errOut.Reset()
	printHistoryLogs(context.Background(), c, "app-one", map[string]any{"history": true}, 1000, liveOptions{}, &out, &errOut)
	if calls != 50 || errOut.String() != "Stopped after 50 pages; narrow --since or --query to see more.\n" {
		t.Errorf("calls %d stderr %q", calls, errOut.String())
	}
}

func fallbackServer(t *testing.T, status int, bodies *[]map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		*bodies = append(*bodies, b)
		if status != 200 {
			http.Error(w, `{"error":{"message":"boom"}}`, status)
			return
		}
		if b["history"] == true {
			w.Write([]byte(`{"entries":[],"history":{"available":false,"message":"Log history is not available on this installation. Collector is off."}}`))
			return
		}
		w.Write([]byte(`{"entries":[{"pod":"web-a","message":"live line"}]}`))
	}))
}

func TestHistoryUnavailableFallsBackToLive(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	uid := "0b8c1c52-3f4d-4c52-9a52-2f6b1f6a9e10"
	for _, tc := range []struct {
		pod       string
		wantPod   string
		wantSince float64
		wantNote  bool
	}{{"web-abc", "web-abc", 86400, false}, {uid, "", 86400, true}} {
		var bodies []map[string]any
		server := fallbackServer(t, 200, &bodies)
		c, _ := newClient(config{URL: server.URL, Key: "fixture"})
		hb, _ := historyBody(historyOptions{Service: "web", Pod: tc.pod, Query: "severity >= ERROR", Since: "3d"}, now)
		var out, errOut bytes.Buffer
		if err := printHistoryLogs(context.Background(), c, "app-one", hb, 1000, liveOptions{Container: "app", Tail: 100, Now: now}, &out, &errOut); err != nil {
			t.Fatal(err)
		}
		server.Close()
		if len(bodies) != 2 {
			t.Fatalf("requests: %v", bodies)
		}
		live := bodies[1]
		if live["history"] != nil || live["cursor"] != nil || live["since_seconds"] != tc.wantSince || live["pod"] != tc.wantPod || live["container"] != "app" || live["service"] != "web" || live["query"] != "severity >= ERROR" || live["tail"] != float64(100) {
			t.Errorf("live body %v", live)
		}
		wantErr := "Log history is not available on this installation. Collector is off. Showing live logs from current pods instead (up to 24h)."
		if tc.wantNote {
			wantErr += " Pod UIDs only apply to stored history; showing all pods."
		}
		if strings.TrimSpace(errOut.String()) != wantErr || !strings.Contains(out.String(), "live line") {
			t.Errorf("stderr %q stdout %q", errOut.String(), out.String())
		}
	}
}

func TestHistoryServerErrorDoesNotFallBack(t *testing.T) {
	var bodies []map[string]any
	server := fallbackServer(t, 500, &bodies)
	defer server.Close()
	c, _ := newClient(config{URL: server.URL, Key: "fixture"})
	var out, errOut bytes.Buffer
	err := printHistoryLogs(context.Background(), c, "app-one", map[string]any{"history": true, "service": "web"}, 10, liveOptions{}, &out, &errOut)
	if err == nil || len(bodies) != 1 {
		t.Errorf("err %v requests %d", err, len(bodies))
	}
}

func TestLogPodsUnavailableMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"available":false,"message":"Log history is not available on this installation. Collector is off.","items":[]}`))
	}))
	defer server.Close()
	c, _ := newClient(config{URL: server.URL, Key: "fixture"})
	var out, errOut bytes.Buffer
	printLogPods(context.Background(), c, "app-one", "7d", time.Now(), &out, &errOut)
	if !strings.HasSuffix(strings.TrimSpace(errOut.String()), "Collector is off. Past pods are only listed when log history is enabled.") {
		t.Errorf("stderr %q", errOut.String())
	}
}

func TestLiveFallbackUsesSince(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	var bodies []map[string]any
	server := fallbackServer(t, 200, &bodies)
	defer server.Close()
	c, _ := newClient(config{URL: server.URL, Key: "fixture"})
	hb, _ := historyBody(historyOptions{Service: "web", Since: "2h"}, now)
	var out, errOut bytes.Buffer
	if err := printHistoryLogs(context.Background(), c, "app-one", hb, 10, liveOptions{Container: "app", Tail: 100, Now: now}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[1]["since_seconds"] != float64(7200) {
		t.Errorf("%v", bodies)
	}
}
