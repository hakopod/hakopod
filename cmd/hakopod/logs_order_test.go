package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHistorySinceReadsNewestFirstAndPrintsChronologically(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	body, _ := historyBody(historyOptions{Service: "web", Since: "3d"}, now)
	if body["order"] != "desc" {
		t.Fatalf("--since alone did not ask for newest first: %v", body)
	}
	for _, o := range []historyOptions{{Service: "web", From: "3d"}, {Service: "web", Since: "3d", To: "1h"}} {
		if b, _ := historyBody(o, now); b["order"] != nil {
			t.Fatalf("an explicit window changed the order: %v", b)
		}
	}
	var orders []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		orders = append(orders, b["order"])
		if b["cursor"] == nil {
			_, _ = w.Write([]byte(`{"entries":[{"timestamp":"2026-10-01T00:00:04Z","pod":"web-a","message":"four"},{"timestamp":"2026-10-01T00:00:03Z","pod":"web-a","message":"three"}],"next_cursor":"c1","history":{"available":true}}`))
			return
		}
		_, _ = w.Write([]byte(`{"entries":[{"timestamp":"2026-10-01T00:00:02Z","pod":"web-a","message":"two"}],"next_cursor":"c2","history":{"available":true}}`))
	}))
	defer server.Close()
	c, _ := newClient(config{URL: server.URL, Key: "fixture"})
	var out, errOut bytes.Buffer
	if err := printHistoryLogs(context.Background(), c, "app-one", body, 3, liveOptions{}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if len(orders) != 2 || orders[0] != "desc" || orders[1] != "desc" {
		t.Fatalf("order not kept across pages: %v", orders)
	}
	want := "2026-10-01T00:00:02Z web-a r0 two\n2026-10-01T00:00:03Z web-a r0 three\n2026-10-01T00:00:04Z web-a r0 four\n"
	if out.String() != want {
		t.Fatalf("stdout %q", out.String())
	}
	if errOut.String() != "Stopped after 3 lines; older lines are available. Raise --limit or narrow the time window.\n" {
		t.Fatalf("stderr %q", errOut.String())
	}
}
