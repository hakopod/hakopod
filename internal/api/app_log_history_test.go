package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/applogs"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type appLogFixture struct {
	db        *store.Store
	server    *httptest.Server
	reader    *applogs.Store
	admin     string
	reader1   string // logs:read on the first app only
	appID     string
	otherID   string
	podRecord applogs.PodRecord
}

func newAppLogFixture(t *testing.T) *appLogFixture {
	t.Helper()
	db := notificationTestDB(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, name := range []string{"first", "second"} {
		next, err := spec.Parse([]byte("schema_version=1\nname='" + name + "'\n[services.web]\nimage='nginx:alpine'\n"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Accept(ctx, owner, "demo", "development", next, 0, "seed-"+name); err != nil {
			t.Fatal(err)
		}
		app, err := db.FindApplication(ctx, "demo", "development", name)
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = app.ID
	}
	_, readerKey, err := db.CreateKey(ctx, owner, store.KeyInput{Name: "reader", Project: "demo", Environment: "development", Application: "first", Permissions: []string{"logs:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	f := &appLogFixture{db: db, admin: raw, reader1: readerKey, appID: ids["first"], otherID: ids["second"]}
	f.reader = &applogs.Store{Root: t.TempDir(), Index: db}
	f.server = httptest.NewServer((&Server{Store: db, appLogs: f.reader}).Handler())
	t.Cleanup(f.server.Close)
	return f
}

func (f *appLogFixture) call(t *testing.T, method, path, key string, body any) (int, map[string]any) {
	t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, f.server.URL+"/api/v1"+path, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	res, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var out map[string]any
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatalf("status %d non-JSON body: %s", res.StatusCode, b)
	}
	return res.StatusCode, out
}

// seed stores three lines for one ended pod that has a recorded gap.
func (f *appLogFixture) seed(t *testing.T, now time.Time) {
	t.Helper()
	ctx := context.Background()
	inst := applogs.Instance{ApplicationID: f.appID, Service: "web", PodUID: "uid-111", PodName: "web-abc", Container: "app", Restart: 1, Revision: 3, Node: "node-a"}
	lines := []applogs.Line{
		{Time: now.Add(-30 * time.Minute), Stream: "stdout", Message: "server started"},
		{Time: now.Add(-29 * time.Minute), Stream: "stderr", Message: `{"level":"error","msg":"boom happened"}`},
		{Time: now.Add(-28 * time.Minute), Stream: "stdout", Message: "shutting down", Truncated: true},
	}
	if err := f.reader.Append(ctx, inst, lines); err != nil {
		t.Fatal(err)
	}
	ended := now.Add(-27 * time.Minute)
	rec := applogs.PodRecord{PodUID: "uid-111", PodName: "web-abc", Service: "web", Revision: 3, Node: "node-a", Source: applogs.SourceAPI, StartedAt: now.Add(-time.Hour), EndedAt: &ended, EndReason: applogs.EndReplaced, Restarts: 1}
	if err := f.db.UpsertLogPod(ctx, f.appID, rec); err != nil {
		t.Fatal(err)
	}
	if err := f.db.AddLogGap(ctx, "uid-111", 4, "Pod was removed before its last output was collected."); err != nil {
		t.Fatal(err)
	}
}

func TestAppLogQueryHistoryMapsStoredLinesAndGaps(t *testing.T) {
	f := newAppLogFixture(t)
	f.seed(t, time.Now().UTC())
	status, out := f.call(t, "POST", "/applications/"+f.appID+"/logs/query", f.reader1, map[string]any{"service": "web", "history": true, "since_seconds": 7200})
	if status != 200 {
		t.Fatalf("status %d: %v", status, out)
	}
	entries := out["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("entries: %v", entries)
	}
	second := entries[1].(map[string]any)
	if second["pod"] != "web-abc" || second["pod_uid"] != "uid-111" || second["revision"] != float64(3) || second["restart"] != float64(1) || second["stream"] != "stderr" || second["severity"] != "ERROR" {
		t.Fatalf("mapped entry: %v", second)
	}
	if entries[2].(map[string]any)["truncated"] != true {
		t.Fatalf("truncated flag lost: %v", entries[2])
	}
	h := out["history"].(map[string]any)
	gaps := h["gaps"].([]any)
	if h["available"] != true || h["collected_through"] == nil || h["retention_days"] != float64(7) || len(gaps) != 1 || gaps[0].(map[string]any)["seconds"] != float64(4) {
		t.Fatalf("history: %v", h)
	}
	pretty, _ := json.MarshalIndent(out, "", "  ")
	t.Logf("PROOF history response:\n%s", pretty)

	// The query predicate and the pod filter apply to stored lines too.
	_, out = f.call(t, "POST", "/applications/"+f.appID+"/logs/query", f.reader1, map[string]any{"service": "web", "history": true, "since_seconds": 7200, "query": "message CONTAINS 'boom'"})
	if n := len(out["entries"].([]any)); n != 1 {
		t.Fatalf("query matched %d entries", n)
	}
	_, out = f.call(t, "POST", "/applications/"+f.appID+"/logs/query", f.reader1, map[string]any{"service": "web", "history": true, "since_seconds": 7200, "pod_uid": "uid-other"})
	if n := len(out["entries"].([]any)); n != 0 {
		t.Fatalf("pod_uid filter returned %d entries", n)
	}
	// A pod name resolves to its UIDs; an unknown name is an empty result with a warning.
	_, out = f.call(t, "POST", "/applications/"+f.appID+"/logs/query", f.reader1, map[string]any{"service": "web", "history": true, "since_seconds": 7200, "pod": "web-abc"})
	if n := len(out["entries"].([]any)); n != 3 {
		t.Fatalf("pod name matched %d entries", n)
	}
	other := applogs.PodRecord{PodUID: "uid-333", PodName: "web-xyz", Service: "web", Source: applogs.SourceFile, StartedAt: time.Now().UTC().Add(-time.Hour)}
	if err := f.db.UpsertLogPod(context.Background(), f.appID, other); err != nil {
		t.Fatal(err)
	}
	if err := f.db.AddLogGap(context.Background(), "uid-333", 9, "Log file rotated before Hakopod read it."); err != nil {
		t.Fatal(err)
	}
	for _, body := range []map[string]any{{"pod": "web-abc"}, {"pod_uid": "uid-111"}} {
		body["service"], body["history"], body["since_seconds"] = "web", true, 7200
		_, out = f.call(t, "POST", "/applications/"+f.appID+"/logs/query", f.reader1, body)
		gaps := out["history"].(map[string]any)["gaps"].([]any)
		if len(gaps) != 1 || gaps[0].(map[string]any)["pod_uid"] != "uid-111" {
			t.Fatalf("gaps leaked across pods for %v: %v", body, gaps)
		}
	}
	_, out = f.call(t, "POST", "/applications/"+f.appID+"/logs/query", f.reader1, map[string]any{"service": "web", "history": true, "since_seconds": 7200})
	if n := len(out["history"].(map[string]any)["gaps"].([]any)); n != 2 {
		t.Fatalf("unfiltered gaps: %d", n)
	}
	status, out = f.call(t, "POST", "/applications/"+f.appID+"/logs/query", f.reader1, map[string]any{"service": "web", "history": true, "since_seconds": 7200, "pod": "web-nope"})
	if status != 200 || len(out["entries"].([]any)) != 0 || len(out["warnings"].([]any)) != 1 {
		t.Fatalf("unknown pod: %d %v", status, out)
	}
	// A page that stops early hands back a cursor to continue.
	_, out = f.call(t, "POST", "/applications/"+f.appID+"/logs/query", f.reader1, map[string]any{"service": "web", "history": true, "since_seconds": 7200, "limit": 2})
	cursor, _ := out["next_cursor"].(string)
	if len(out["entries"].([]any)) != 2 || cursor == "" {
		t.Fatalf("first page: %v", out)
	}
	_, out = f.call(t, "POST", "/applications/"+f.appID+"/logs/query", f.reader1, map[string]any{"service": "web", "history": true, "since_seconds": 7200, "limit": 2, "cursor": cursor})
	if len(out["entries"].([]any)) != 1 {
		t.Fatalf("second page: %v", out)
	}
}

func TestAppLogQueryWithoutHistoryKeepsLiveBehaviour(t *testing.T) {
	f := newAppLogFixture(t)
	f.seed(t, time.Now().UTC())
	// No cluster is configured, so a live query reports that, never stored lines.
	for _, body := range []map[string]any{
		{"service": "web"},
		{"service": "web", "history": false, "from": time.Now().Add(-time.Hour).Format(time.RFC3339)},
	} {
		status, out := f.call(t, "POST", "/applications/"+f.appID+"/logs/query", f.reader1, body)
		if status != 503 || out["error"].(map[string]any)["code"] != "cluster_unavailable" {
			t.Fatalf("live path changed: %d %v", status, out)
		}
	}
	// Live limits still apply: 24 hours at most.
	status, out := f.call(t, "POST", "/applications/"+f.appID+"/logs/query", f.reader1, map[string]any{"service": "web", "since_seconds": 90000})
	if status != 400 || out["error"].(map[string]any)["code"] != "invalid_query_options" {
		t.Fatalf("live validation changed: %d %v", status, out)
	}
}

func TestAppLogQueryHistoryIsScopedToTheAuthorizedApp(t *testing.T) {
	f := newAppLogFixture(t)
	f.seed(t, time.Now().UTC())
	status, out := f.call(t, "POST", "/applications/"+f.otherID+"/logs/query", f.reader1, map[string]any{"service": "web", "history": true})
	if status != 403 {
		t.Fatalf("other app: %d %v", status, out)
	}
	status, _ = f.call(t, "GET", "/applications/"+f.otherID+"/log-sources", f.reader1, nil)
	if status != 403 {
		t.Fatalf("other app sources: %d", status)
	}
	// The second app has no stored lines even though the first one does.
	_, out = f.call(t, "POST", "/applications/"+f.otherID+"/logs/query", f.admin, map[string]any{"service": "web", "history": true, "since_seconds": 7200})
	if n := len(out["entries"].([]any)); n != 0 {
		t.Fatalf("lines leaked across apps: %d", n)
	}
}

func TestAppLogQueryHistoryValidation(t *testing.T) {
	f := newAppLogFixture(t)
	now := time.Now().UTC()
	rfc := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	cases := map[string]map[string]any{
		"to before from":       {"from": rfc(-time.Hour), "to": rfc(-2 * time.Hour)},
		"span over retention":  {"from": rfc(-6 * 24 * time.Hour), "to": rfc(3 * 24 * time.Hour)},
		"from before window":   {"from": rfc(-9 * 24 * time.Hour), "to": rfc(-8 * 24 * time.Hour)},
		"since over retention": {"since_seconds": 8 * 86400},
		"long cursor":          {"cursor": strings.Repeat("a", 513)},
		"bad cursor":           {"cursor": "not*base64"},
		"bad pod uid":          {"pod_uid": "../etc"},
		"limit too high":       {"limit": 2001},
		"bad query":            {"query": "(("},
		"bad service":          {"service": "nope"},
	}
	for name, extra := range cases {
		body := map[string]any{"service": "web", "history": true}
		for k, v := range extra {
			body[k] = v
		}
		status, out := f.call(t, "POST", "/applications/"+f.appID+"/logs/query", f.reader1, body)
		if status != 400 {
			t.Errorf("%s: status %d %v", name, status, out)
		}
	}
}

func TestAppLogQueryHistoryReportsUnavailableStore(t *testing.T) {
	f := newAppLogFixture(t)
	f.reader.Root = t.TempDir() + "/missing"
	status, out := f.call(t, "POST", "/applications/"+f.appID+"/logs/query", f.reader1, map[string]any{"service": "web", "history": true})
	h, _ := out["history"].(map[string]any)
	if status != 200 || h["available"] != false || h["message"] != "Log history is not set up on this server yet. Run installer.sh --upgrade to enable it." || len(out["entries"].([]any)) != 0 {
		t.Fatalf("unavailable store: %d %v", status, out)
	}
	// Turning the feature off is reported the same way.
	f.reader.Root = t.TempDir()
	if _, err := f.db.PutAppLogSettings(context.Background(), store.AppLogSettings{Enabled: false, RetentionDays: 7, MaxMiBPerApp: 512}, 1); err != nil {
		t.Fatal(err)
	}
	_, out = f.call(t, "POST", "/applications/"+f.appID+"/logs/query", f.reader1, map[string]any{"service": "web", "history": true})
	if h, _ = out["history"].(map[string]any); h["available"] != false || h["message"] != "Log history is turned off in installation settings." {
		t.Fatalf("disabled: %v", out)
	}
}

func TestAppLogSourcesListCurrentAndPastPods(t *testing.T) {
	f := newAppLogFixture(t)
	now := time.Now().UTC()
	f.seed(t, now)
	live := applogs.PodRecord{PodUID: "uid-222", PodName: "web-def", Service: "web", Source: applogs.SourceFile, StartedAt: now.Add(-10 * time.Minute)}
	if err := f.db.UpsertLogPod(context.Background(), f.appID, live); err != nil {
		t.Fatal(err)
	}
	status, out := f.call(t, "GET", "/applications/"+f.appID+"/log-sources", f.reader1, nil)
	items, _ := out["items"].([]any)
	if status != 200 || len(items) != 2 || out["available"] != true || out["retention_days"] != float64(7) {
		t.Fatalf("sources: %d %v", status, out)
	}
	byUID := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		byUID[m["pod_uid"].(string)] = m
	}
	if byUID["uid-222"]["current"] != true || byUID["uid-222"]["ended_at"] != nil || byUID["uid-111"]["current"] != false || byUID["uid-111"]["end_reason"] != "replaced" || byUID["uid-111"]["gap_seconds"] != float64(4) {
		t.Fatalf("source fields: %v", byUID)
	}
	for _, it := range items {
		for _, k := range []string{"started_at", "ended_at"} {
			if v, _ := it.(map[string]any)[k].(string); v != "" && !strings.HasSuffix(v, "Z") {
				t.Fatalf("%s not UTC: %s", k, v)
			}
		}
	}
	if status, _ = f.call(t, "GET", "/applications/"+f.appID+"/log-sources?limit=0", f.reader1, nil); status != 400 {
		t.Fatalf("limit=0: %d", status)
	}
	if status, _ = f.call(t, "GET", "/applications/"+f.appID+"/log-sources?from=yesterday", f.reader1, nil); status != 400 {
		t.Fatalf("bad from: %d", status)
	}
	f.reader.Root = t.TempDir() + "/missing"
	_, out = f.call(t, "GET", "/applications/"+f.appID+"/log-sources", f.reader1, nil)
	if out["available"] != false || out["message"] != "Log history is not set up on this server yet. Run installer.sh --upgrade to enable it." || len(out["items"].([]any)) != 0 {
		t.Fatalf("unavailable sources: %v", out)
	}
}

func TestAppLogSettingsAreAdminOnlyAndConflictSafe(t *testing.T) {
	f := newAppLogFixture(t)
	if status, _ := f.call(t, "GET", "/settings/app-logs", f.reader1, nil); status != 403 {
		t.Fatalf("non-admin GET: %d", status)
	}
	if status, _ := f.call(t, "PUT", "/settings/app-logs", f.reader1, map[string]any{"enabled": true, "retention_days": 7, "max_mib_per_app": 512, "expected_revision": 0}); status != 403 {
		t.Fatalf("non-admin PUT: %d", status)
	}
	status, out := f.call(t, "GET", "/settings/app-logs", f.admin, nil)
	if status != 200 || out["enabled"] != true || out["retention_days"] != float64(7) || out["max_mib_per_app"] != float64(512) || out["revision"] != float64(1) {
		t.Fatalf("defaults: %d %v", status, out)
	}
	put := map[string]any{"enabled": true, "retention_days": 14, "max_mib_per_app": 1024, "expected_revision": 1}
	status, out = f.call(t, "PUT", "/settings/app-logs", f.admin, put)
	if status != 200 || out["retention_days"] != float64(14) || out["revision"] != float64(2) {
		t.Fatalf("first PUT: %d %v", status, out)
	}
	if status, _ = f.call(t, "PUT", "/settings/app-logs", f.admin, put); status != 409 {
		t.Fatalf("stale revision: %d", status)
	}
	put["expected_revision"] = 2
	put["retention_days"] = 31
	if status, _ = f.call(t, "PUT", "/settings/app-logs", f.admin, put); status != 400 {
		t.Fatalf("retention out of range: %d", status)
	}
	put["retention_days"], put["max_mib_per_app"] = 14, 10
	if status, _ = f.call(t, "PUT", "/settings/app-logs", f.admin, put); status != 400 {
		t.Fatalf("size out of range: %d", status)
	}
	_, out = f.call(t, "GET", "/settings/app-logs", f.admin, nil)
	if out["retention_days"] != float64(14) || out["revision"] != float64(2) {
		t.Fatalf("saved settings: %v", out)
	}
}
