package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/applogs"
)

var logBase = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func logTime(m int) time.Time { return logBase.Add(time.Duration(m) * time.Minute) }

func logApp(t *testing.T, db *Store, idem string) string {
	t.Helper()
	p := bootstrapPrincipal(t, db)
	d, err := db.Accept(context.Background(), p, "demo", "development", emptyTestSpec(), 0, idem)
	if err != nil {
		t.Fatal(err)
	}
	return d.ApplicationID
}

func seg(app, id, pod string, first, last int, bytes int64) applogs.Segment {
	return applogs.Segment{ID: id, Path: id + ".ndjson", FirstTime: logTime(first), LastTime: logTime(last), Lines: 1, Bytes: bytes,
		Instance: applogs.Instance{ApplicationID: app, Service: "api", PodUID: pod, PodName: pod + "-name", Container: "app"}}
}

func ids(s []applogs.Segment) string {
	out := []string{}
	for _, x := range s {
		out = append(out, x.ID)
	}
	return strings.Join(out, ",")
}

func TestAppLogMigrationApplies(t *testing.T) {
	db := isolatedDatabase(t)
	var n int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM app_log_collector WHERE id=1").Scan(&n); err != nil || n != 1 {
		t.Fatal("collector row missing", n, err)
	}
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM information_schema.tables WHERE table_name IN ('app_log_segments','app_log_pods','app_log_cursors')").Scan(&n); err != nil || n != 3 {
		t.Fatal("tables missing", n, err)
	}
}

func TestAppLogSegments(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	app := logApp(t, db, "applog-seg-1")
	for _, g := range []applogs.Segment{seg(app, "a", "p1", 0, 10, 100), seg(app, "b", "p1", 20, 30, 200), seg(app, "c", "p2", 40, 50, 300)} {
		if err := db.SaveSegment(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	// Upsert by ID updates in place.
	up := seg(app, "a", "p1", 0, 12, 150)
	up.Sealed = true
	if err := db.SaveSegment(ctx, up); err != nil {
		t.Fatal(err)
	}
	all, _ := db.Segments(ctx, applogs.SegmentQuery{ApplicationID: app, From: logTime(-5), To: logTime(100), Limit: 500})
	if ids(all) != "a,b,c" || !all[0].Sealed || all[0].Bytes != 150 {
		t.Fatal("upsert or order wrong", ids(all))
	}
	// Overlap is inclusive on both edges.
	for _, c := range []struct {
		from, to int
		want     string
	}{{12, 20, "a,b"}, {13, 19, ""}, {30, 40, "b,c"}, {31, 39, ""}, {-9, 0, "a"}, {50, 60, "c"}} {
		got, err := db.Segments(ctx, applogs.SegmentQuery{ApplicationID: app, From: logTime(c.from), To: logTime(c.to), Limit: 500})
		if err != nil || ids(got) != c.want {
			t.Fatal("overlap", c, ids(got), err)
		}
	}
	got, _ := db.Segments(ctx, applogs.SegmentQuery{ApplicationID: app, From: logTime(-5), To: logTime(100), PodUIDs: []string{"p2"}, Limit: 500})
	if ids(got) != "c" {
		t.Fatal("pod filter", ids(got))
	}
	got, _ = db.Segments(ctx, applogs.SegmentQuery{ApplicationID: app, From: logTime(-5), To: logTime(100), Services: []string{"web"}, Limit: 500})
	if len(got) != 0 {
		t.Fatal("service filter")
	}
	got, _ = db.Segments(ctx, applogs.SegmentQuery{ApplicationID: app, From: logTime(-5), To: logTime(100), Limit: -3})
	if len(got) != 1 {
		t.Fatal("limit should clamp to 1", len(got))
	}

	exp, _ := db.ExpiredSegments(ctx, logTime(35), 10)
	if ids(exp) != "a,b" {
		t.Fatal("expired", ids(exp))
	}
	over, _ := db.ApplicationsOverBytes(ctx, 650, 10)
	if len(over) != 0 {
		t.Fatal("650 bytes is not over 650", over)
	}
	if over, _ = db.ApplicationsOverBytes(ctx, 649, 10); len(over) != 1 || over[0] != app {
		t.Fatal("over bytes", over)
	}
	old, _ := db.OldestSegments(ctx, app, 2)
	if ids(old) != "a,b" {
		t.Fatal("oldest", ids(old))
	}
	if err := db.DeleteSegments(ctx, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if left, _ := db.OldestSegments(ctx, app, 10); ids(left) != "c" {
		t.Fatal("delete", ids(left))
	}
	if err := db.DeleteSegments(ctx, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAppLogCascadeOnApplicationDelete(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	app := logApp(t, db, "applog-cascade")
	if err := db.SaveSegment(ctx, seg(app, "a", "p1", 0, 1, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertLogPod(ctx, app, applogs.PodRecord{PodUID: "p1", PodName: "n", Service: "api", Source: applogs.SourceFile, StartedAt: logTime(0)}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"DELETE FROM deployment_events WHERE deployment_id IN (SELECT id FROM deployments WHERE application_id=$1)", "DELETE FROM deployments WHERE application_id=$1", "DELETE FROM applications WHERE id=$1"} {
		if _, err := db.Pool.Exec(ctx, q, app); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM app_log_segments)+(SELECT count(*) FROM app_log_pods)").Scan(&n); err != nil || n != 0 {
		t.Fatal("rows survived application delete", n, err)
	}
}

func TestAppLogPodRules(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	app := logApp(t, db, "applog-pods")
	pod := applogs.PodRecord{PodUID: "p1", PodName: "web-1", Service: "api", Revision: 3, Node: "n1", Source: applogs.SourceFile, StartedAt: logTime(0), Restarts: 2}
	if err := db.UpsertLogPod(ctx, app, pod); err != nil {
		t.Fatal(err)
	}
	get := func() applogs.PodRecord {
		p, err := db.LogPods(ctx, app, logTime(-10), logTime(1000), 10)
		if err != nil || len(p) != 1 {
			t.Fatal("pods", len(p), err)
		}
		return p[0]
	}
	// Zero revision and lower restarts never overwrite.
	pod.Revision, pod.Restarts, pod.Node = 0, 1, "n2"
	end := logTime(10)
	pod.EndedAt, pod.EndReason = &end, applogs.EndReplaced
	if err := db.UpsertLogPod(ctx, app, pod); err != nil {
		t.Fatal(err)
	}
	g := get()
	if g.Revision != 3 || g.Restarts != 2 || g.Node != "n2" || g.EndedAt == nil || g.EndReason != applogs.EndReplaced {
		t.Fatalf("bad merge %+v", g)
	}
	// ended_at is never cleared or moved earlier.
	pod.EndedAt, pod.EndReason = nil, ""
	_ = db.UpsertLogPod(ctx, app, pod)
	early := logTime(5)
	pod.EndedAt, pod.EndReason = &early, applogs.EndDeleted
	_ = db.UpsertLogPod(ctx, app, pod)
	g = get()
	if g.EndedAt == nil || !g.EndedAt.Equal(end) || g.EndReason != applogs.EndReplaced {
		t.Fatalf("ended_at moved %+v", g)
	}
	// Specific reason upgrades replaced; a vaguer one does not downgrade it.
	pod.EndedAt, pod.EndReason = &end, applogs.EndOOMKilled
	_ = db.UpsertLogPod(ctx, app, pod)
	pod.EndReason = applogs.EndReplaced
	_ = db.UpsertLogPod(ctx, app, pod)
	if g = get(); g.EndReason != applogs.EndOOMKilled {
		t.Fatal("reason", g.EndReason)
	}
	pod.EndReason = applogs.EndEvicted
	_ = db.UpsertLogPod(ctx, app, pod)
	if g = get(); g.EndReason != applogs.EndOOMKilled {
		t.Fatal("specific reason replaced by another", g.EndReason)
	}
}

func TestAppLogEndOpenPodsAndOverlap(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	app := logApp(t, db, "applog-open")
	mk := func(uid string, start int, end *time.Time) {
		if err := db.UpsertLogPod(ctx, app, applogs.PodRecord{PodUID: uid, PodName: uid, Service: "api", Source: applogs.SourceAPI, StartedAt: logTime(start), EndedAt: end, EndReason: map[bool]applogs.EndReason{true: applogs.EndCompleted}[end != nil]}); err != nil {
			t.Fatal(err)
		}
	}
	e := logTime(20)
	mk("old", 0, &e)
	mk("keep", 5, nil)
	mk("gone", 6, nil)
	if err := db.EndOpenLogPods(ctx, app, []string{"keep"}, logTime(30), applogs.EndReplaced); err != nil {
		t.Fatal(err)
	}
	pods, _ := db.LogPods(ctx, app, logTime(-100), logTime(100), 10)
	by := map[string]applogs.PodRecord{}
	for _, p := range pods {
		by[p.PodUID] = p
	}
	if by["keep"].EndedAt != nil || by["gone"].EndReason != applogs.EndReplaced || by["old"].EndReason != applogs.EndCompleted || !by["old"].EndedAt.Equal(e) {
		t.Fatalf("end open %+v", by)
	}
	// Seen nil ends every open pod.
	if err := db.EndOpenLogPods(ctx, app, nil, logTime(31), applogs.EndDeleted); err != nil {
		t.Fatal(err)
	}
	if p, _ := db.LogPods(ctx, app, logTime(-100), logTime(100), 10); p[0].EndedAt == nil && p[1].EndedAt == nil {
		t.Fatal("nil seen should end all")
	}
	// Overlap: old lived 0..20, gone 6..30, keep 5..31.
	for _, c := range []struct {
		from, to, want int
	}{{-10, -1, 0}, {-10, 0, 1}, {20, 25, 3}, {31, 40, 1}, {32, 40, 0}, {-100, 100, 3}} {
		got, _ := db.LogPods(ctx, app, logTime(c.from), logTime(c.to), 10)
		if len(got) != c.want {
			t.Fatal("pod overlap", c, len(got))
		}
	}
	if got, _ := db.LogPods(ctx, app, logTime(-100), logTime(100), 0); len(got) != 1 {
		t.Fatal("limit clamp", len(got))
	}
	if n, err := db.PruneLogPods(ctx, logTime(25)); err != nil || n != 1 {
		t.Fatal("prune pods", n, err)
	}
}

func TestAppLogGapAccumulatesAndTrims(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	app := logApp(t, db, "applog-gap")
	_ = db.UpsertLogPod(ctx, app, applogs.PodRecord{PodUID: "p1", PodName: "p1", Service: "api", Source: applogs.SourceFile, StartedAt: logTime(0)})
	_ = db.AddLogGap(ctx, "p1", 5, "first")
	_ = db.AddLogGap(ctx, "p1", 100000, strings.Repeat("x", 300))
	_ = db.AddLogGap(ctx, "p1", -4, strings.Repeat("y", 300))
	var secs int
	var note string
	if err := db.Pool.QueryRow(ctx, "SELECT gap_seconds,gap_note FROM app_log_pods WHERE pod_uid='p1'").Scan(&secs, &note); err != nil {
		t.Fatal(err)
	}
	if secs != 5+86400 || note != strings.Repeat("y", 200) {
		t.Fatal("gap", secs, len(note))
	}
}

func TestAppLogCursorsAndLease(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	if _, ok, err := db.LogCursor(ctx, "k"); ok || err != nil {
		t.Fatal("missing cursor", ok, err)
	}
	_ = db.SaveLogCursor(ctx, "k", "p1", json.RawMessage(`{"offset":1}`))
	_ = db.SaveLogCursor(ctx, "k", "p1", json.RawMessage(`{"offset":2}`))
	c, ok, err := db.LogCursor(ctx, "k")
	if err != nil || !ok || !strings.Contains(string(c), "2") {
		t.Fatal("cursor", string(c), ok, err)
	}
	if n, _ := db.DeleteLogCursorsBefore(ctx, time.Now().Add(-time.Hour)); n != 0 {
		t.Fatal("fresh cursor deleted")
	}
	if n, _ := db.DeleteLogCursorsBefore(ctx, time.Now().Add(time.Hour)); n != 1 {
		t.Fatal("old cursor kept", n)
	}

	if ok, err := db.ClaimAppLogCollector(ctx, "a", time.Minute); !ok || err != nil {
		t.Fatal("first claim", ok, err)
	}
	if ok, _ := db.ClaimAppLogCollector(ctx, "b", time.Minute); ok {
		t.Fatal("second owner got the lease")
	}
	if ok, _ := db.ClaimAppLogCollector(ctx, "a", time.Minute); !ok {
		t.Fatal("owner could not renew")
	}
	if ok, _ := db.ClaimAppLogCollector(ctx, "a", -time.Second); !ok {
		t.Fatal("renew")
	}
	if ok, _ := db.ClaimAppLogCollector(ctx, "b", time.Minute); !ok {
		t.Fatal("expired lease not taken")
	}
}

func TestAppLogSettings(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	_, _ = db.Pool.Exec(ctx, "DELETE FROM installation_settings WHERE name='app_logs'")
	v, rev, err := db.AppLogSettings(ctx)
	if err != nil || rev != 0 || !v.Enabled || v.RetentionDays != 7 || v.MaxMiBPerApp != 512 {
		t.Fatal("defaults", v, rev, err)
	}
	for _, bad := range []AppLogSettings{{true, 0, 512}, {true, 31, 512}, {true, 7, 63}, {true, 7, 4097}} {
		if _, err := db.PutAppLogSettings(ctx, bad, 0); !errors.Is(err, ErrInput) {
			t.Fatal("accepted", bad, err)
		}
	}
	rev, err = db.PutAppLogSettings(ctx, AppLogSettings{false, 3, 64}, 0)
	if err != nil || rev != 1 {
		t.Fatal("first put", rev, err)
	}
	if _, err = db.PutAppLogSettings(ctx, AppLogSettings{true, 4, 100}, 0); !errors.Is(err, ErrConflict) {
		t.Fatal("stale create", err)
	}
	if _, err = db.PutAppLogSettings(ctx, AppLogSettings{true, 4, 100}, 9); !errors.Is(err, ErrConflict) {
		t.Fatal("stale update", err)
	}
	if rev, err = db.PutAppLogSettings(ctx, AppLogSettings{true, 4, 100}, 1); err != nil || rev != 2 {
		t.Fatal("update", rev, err)
	}
	if v, rev, _ = db.AppLogSettings(ctx); !v.Enabled || v.RetentionDays != 4 || rev != 2 {
		t.Fatal("read back", v, rev)
	}
}

func TestAppLogCursorsForEndedPods(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	app := logApp(t, db, "applog-cursor-ended")
	end := logTime(5)
	_ = db.UpsertLogPod(ctx, app, applogs.PodRecord{PodUID: "live", PodName: "live", Service: "api", Source: applogs.SourceFile, StartedAt: logTime(0)})
	_ = db.UpsertLogPod(ctx, app, applogs.PodRecord{PodUID: "dead", PodName: "dead", Service: "api", Source: applogs.SourceFile, StartedAt: logTime(0), EndedAt: &end, EndReason: applogs.EndReplaced})
	for _, k := range []string{"live", "dead", "orphan"} {
		if err := db.SaveLogCursor(ctx, k+"|c|0", k, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE app_log_cursors SET updated_at=now()-interval '30 days'"); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().Add(-time.Hour)
	// The dead pod ended in 2026-01 and orphans are old; compare against a far-future cutoff for ended_at.
	n, err := db.DeleteLogCursorsForEndedPods(ctx, cutoff.AddDate(1, 0, 0))
	if err != nil || n != 2 {
		t.Fatal("deleted", n, err)
	}
	if _, ok, _ := db.LogCursor(ctx, "live|c|0"); !ok {
		t.Fatal("live pod cursor deleted")
	}
}

func TestAppLogReleaseAndCursorTimes(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	if ok, _ := db.ClaimAppLogCollector(ctx, "a", time.Minute); !ok {
		t.Fatal("claim")
	}
	_ = db.ReleaseAppLogCollector(ctx, "b")
	if ok, _ := db.ClaimAppLogCollector(ctx, "b", time.Minute); ok {
		t.Fatal("non-owner release freed the lease")
	}
	_ = db.ReleaseAppLogCollector(ctx, "a")
	if ok, _ := db.ClaimAppLogCollector(ctx, "b", time.Minute); !ok {
		t.Fatal("lease not free after release")
	}

	app := logApp(t, db, "applog-cursor-times")
	end := logTime(5)
	for _, u := range []string{"cur", "nocur", "skip"} {
		_ = db.UpsertLogPod(ctx, app, applogs.PodRecord{PodUID: u, PodName: u, Service: "api", Source: applogs.SourceFile, StartedAt: logTime(0)})
	}
	_ = db.UpsertLogPod(ctx, app, applogs.PodRecord{PodUID: "ended", PodName: "e", Service: "api", Source: applogs.SourceFile, StartedAt: logTime(0), EndedAt: &end, EndReason: applogs.EndReplaced})
	_ = db.SaveLogCursor(ctx, "cur|c|0", "cur", json.RawMessage(`{}`))
	old := logTime(1)
	_, _ = db.Pool.Exec(ctx, "UPDATE app_log_cursors SET updated_at=$1", old)
	_ = db.SaveLogCursor(ctx, "cur|c|1", "cur", json.RawMessage(`{}`))
	_, _ = db.Pool.Exec(ctx, "UPDATE app_log_pods SET last_seen=$1 WHERE pod_uid='nocur'", logTime(2))
	got, err := db.OpenLogPodCursorTimes(ctx, app, []string{"skip"})
	if err != nil || len(got) != 2 {
		t.Fatal("times", got, err)
	}
	if !got["cur"].After(old) {
		t.Fatal("should use newest cursor time", got["cur"])
	}
	if !got["nocur"].Equal(logTime(2)) {
		t.Fatal("last_seen fallback", got["nocur"])
	}
}
