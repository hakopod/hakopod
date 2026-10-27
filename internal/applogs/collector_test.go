package applogs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/logmask"
)

type collIndex struct {
	mu   sync.Mutex
	segs map[string]Segment
}

func (x *collIndex) SaveSegment(_ context.Context, s Segment) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.segs == nil {
		x.segs = map[string]Segment{}
	}
	x.segs[s.ID] = s
	return nil
}
func (x *collIndex) Segments(_ context.Context, q SegmentQuery) ([]Segment, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	var out []Segment
	for _, s := range x.segs {
		if s.Instance.ApplicationID == q.ApplicationID {
			out = append(out, s)
		}
	}
	return out, nil
}
func (x *collIndex) DeleteSegments(context.Context, []string) error { return nil }
func (x *collIndex) ExpiredSegments(context.Context, time.Time, int) ([]Segment, error) {
	return nil, nil
}
func (x *collIndex) ApplicationsOverBytes(context.Context, int64, int) ([]string, error) {
	return nil, nil
}
func (x *collIndex) OldestSegments(context.Context, string, int) ([]Segment, error) { return nil, nil }

type end struct {
	App    string
	Seen   []string
	Reason EndReason
}
type gap struct {
	UID, Note string
	Seconds   int
}

type collRecords struct {
	mu      sync.Mutex
	deny    bool
	cursors map[string]json.RawMessage
	pods    map[string]PodRecord
	ends    []end
	gaps    []gap
	open    map[string]time.Time
	release string
}

func (r *collRecords) ClaimAppLogCollector(context.Context, string, time.Duration) (bool, error) {
	return !r.deny, nil
}
func (r *collRecords) UpsertLogPod(_ context.Context, _ string, p PodRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pods[p.PodUID] = p
	return nil
}
func (r *collRecords) EndOpenLogPods(_ context.Context, app string, seen []string, _ time.Time, reason EndReason) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ends = append(r.ends, end{app, seen, reason})
	return nil
}
func (r *collRecords) AddLogGap(_ context.Context, uid string, secs int, note string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gaps = append(r.gaps, gap{uid, note, secs})
	return nil
}
func (r *collRecords) LogCursor(_ context.Context, key string) (json.RawMessage, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.cursors[key]
	return v, ok, nil
}
func (r *collRecords) SaveLogCursor(_ context.Context, key, _ string, c json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cursors[key] = c
	return nil
}
func (r *collRecords) DeleteLogCursorsForEndedPods(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (r *collRecords) PruneLogPods(context.Context, time.Time) (int64, error) { return 0, nil }
func (r *collRecords) ReleaseAppLogCollector(_ context.Context, owner string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.release = owner
	return nil
}
func (r *collRecords) OpenLogPodCursorTimes(context.Context, string, []string) (map[string]time.Time, error) {
	return r.open, nil
}
func (r *collRecords) Settings(context.Context) (Settings, error) {
	return Settings{Enabled: true, RetentionDays: 7, MaxMiBPerApp: 512}, nil
}

type collCluster struct {
	mu        sync.Mutex
	pods      []Pod
	maskErr   error
	listed    int
	api       map[string][]Line // "ns/pod/container/previous" -> all lines the API holds
	apiCalls  int
	maskCalls int
	lines     map[string]int
}

func (c *collCluster) AppLogPods(context.Context, int) ([]Pod, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listed++
	return append([]Pod(nil), c.pods...), nil
}
func (c *collCluster) PodLogSince(_ context.Context, ns, pod, container string, previous bool, since time.Time, limit int64) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiCalls++
	var b strings.Builder
	for _, l := range c.api[fmt.Sprintf("%s/%s/%s/%v", ns, pod, container, previous)] {
		if !l.Time.Before(since.Truncate(time.Second)) { // SinceTime has second precision
			fmt.Fprintf(&b, "%s %s\n", l.Time.Format(time.RFC3339Nano), l.Message)
		}
	}
	out := []byte(b.String())
	if int64(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (c *collCluster) AppLogMasker(context.Context, string, string) (Masker, error) {
	c.mu.Lock()
	c.maskCalls++
	c.mu.Unlock()
	if c.maskErr != nil {
		return nil, c.maskErr
	}
	return countMasker{logmask.New("SECRET-xyz"), c}, nil // a fresh masker, as after a process restart
}
func (c *collCluster) set(p ...Pod) { c.mu.Lock(); c.pods = p; c.mu.Unlock() }

type countMasker struct {
	Masker
	c *collCluster
}

func (m countMasker) Line(s string) string {
	m.c.mu.Lock()
	m.c.lines[s]++
	m.c.mu.Unlock()
	return m.Masker.Line(s)
}

type harness struct {
	t    *testing.T
	c    *Collector
	cl   *collCluster
	rec  *collRecords
	logs string
}

func newHarness(t *testing.T) *harness {
	logs := t.TempDir()
	h := &harness{t: t, logs: logs, cl: &collCluster{api: map[string][]Line{}, lines: map[string]int{}},
		rec: &collRecords{cursors: map[string]json.RawMessage{}, pods: map[string]PodRecord{}}}
	h.c = &Collector{Cluster: h.cl, Records: h.rec, Store: &Store{Root: t.TempDir(), Index: &collIndex{}}, Tailer: FileTailer{Root: logs}, Owner: "test"}
	return h
}

func (h *harness) tick() {
	h.t.Helper()
	if err := h.c.Tick(context.Background()); err != nil {
		h.t.Fatal(err)
	}
}

var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// writeLog appends CRI lines (second offsets from base) to a pod's log file.
func (h *harness) writeLog(uid, container string, restart int, from int, msgs ...string) {
	h.t.Helper()
	dir := filepath.Join(h.logs, "hp-a_"+uid+"_"+uid, container)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%d.log", restart)), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	for i, m := range msgs {
		fmt.Fprintf(f, "%s stdout F %s\n", base.Add(time.Duration(from+i)*time.Second).Format(time.RFC3339Nano), m)
	}
}

func (h *harness) stored(app string) []string {
	h.t.Helper()
	res, err := h.c.Store.Read(context.Background(), ReadQuery{ApplicationID: app, From: base.Add(-time.Hour), To: base.Add(24 * time.Hour), Limit: MaxQueryLines})
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, e := range res.Entries {
		out = append(out, fmt.Sprintf("%s/r%d:%s", e.Instance.PodName, e.Instance.Restart, e.Line.Message))
	}
	return out
}

func pod(uid, app string, ks ...Container) Pod {
	return Pod{ApplicationID: app, Service: "web", Namespace: "hp-a", Name: uid, UID: uid, StartedAt: base, Containers: ks}
}
func run(restart int) Container { return Container{Name: "app", Restart: restart, Running: true} }

func want(t *testing.T, got []string, exp ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(exp, "|") {
		t.Fatalf("stored lines\n got %v\nwant %v", got, exp)
	}
}

func TestCollectorFileIncrementalReads(t *testing.T) {
	h := newHarness(t)
	h.cl.set(pod("u1", "app1", run(0)))
	h.writeLog("u1", "app", 0, 0, "one", "two")
	h.tick()
	h.tick() // nothing new: no duplicates
	h.writeLog("u1", "app", 0, 2, "three")
	h.tick()
	want(t, h.stored("app1"), "u1/r0:one", "u1/r0:two", "u1/r0:three")
	if h.rec.pods["u1"].Source != SourceFile {
		t.Fatalf("source %v", h.rec.pods["u1"].Source)
	}
}

func TestTickRestartDrainsPreviousInstanceFirst(t *testing.T) {
	h := newHarness(t)
	h.cl.set(pod("u1", "app1", run(0)))
	h.writeLog("u1", "app", 0, 0, "boot")
	h.tick()
	h.writeLog("u1", "app", 0, 1, "last words before crash")
	h.writeLog("u1", "app", 1, 2, "booted again")
	h.cl.set(pod("u1", "app1", run(1)))
	h.tick()
	want(t, h.stored("app1"), "u1/r0:boot", "u1/r0:last words before crash", "u1/r1:booted again")
	if h.rec.pods["u1"].Restarts != 1 {
		t.Fatalf("restarts %d", h.rec.pods["u1"].Restarts)
	}
}

func TestTickTerminatingPodIsDrainedBeyondFairShare(t *testing.T) {
	h := newHarness(t)
	h.c.TickBytes = 4000 // two pods: 2000 bytes each
	var many []string
	for i := 0; i < 20; i++ {
		many = append(many, fmt.Sprintf("line-%02d-%s", i, strings.Repeat("x", 90)))
	}
	dying := pod("dying", "app1", run(0))
	dying.Terminating = true
	h.cl.set(pod("calm", "app2", run(0)), dying)
	h.writeLog("dying", "app", 0, 0, many...)
	h.writeLog("calm", "app", 0, 0, many...)
	h.tick()
	if n := len(h.stored("app1")); n != 20 {
		t.Fatalf("terminating pod stored %d of 20 lines", n)
	}
	if n := len(h.stored("app2")); n == 0 || n >= 20 {
		t.Fatalf("calm pod should be limited to its share, stored %d", n)
	}
}

func TestEndReasonMapping(t *testing.T) {
	term := func(reason string, code int32) Container {
		return Container{Name: "app", Terminated: true, Reason: reason, ExitCode: code}
	}
	cases := []struct {
		name string
		p    Pod
		want EndReason
	}{
		{"evicted", Pod{Reason: "Evicted", Containers: []Container{run(0)}}, EndEvicted},
		{"oom", Pod{Containers: []Container{term("OOMKilled", 137)}}, EndOOMKilled},
		{"error", Pod{Containers: []Container{term("Error", 1)}}, EndError},
		{"completed", Pod{Containers: []Container{term("Completed", 0)}}, EndCompleted},
		{"replaced", Pod{Containers: []Container{run(0)}}, EndReplaced},
		{"rollout sigterm", Pod{Terminating: true, Containers: []Container{term("Error", 143)}}, EndReplaced},
	}
	for _, c := range cases {
		if got := endReason(c.p); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// TestTickPodGoneRecordsReasonsAndGaps is the restart-plus-terminate scenario:
// a file pod crashes and restarts, then terminates; an API pod vanishes
// between polls.
func TestTickPodGoneRecordsReasonsAndGaps(t *testing.T) {
	h := newHarness(t)
	h.cl.set(pod("u1", "app1", run(0)), pod("u2", "app1", run(0)))
	h.writeLog("u1", "app", 0, 0, "boot")
	h.cl.api["hp-a/u2/app/false"] = []Line{{Time: base.Add(500 * time.Millisecond), Message: "api boot"}}
	// u2 has no log directory, so it is read through the API.
	if err := os.RemoveAll(filepath.Join(h.logs, "hp-a_u2_u2")); err != nil {
		t.Fatal(err)
	}
	h.tick()
	h.writeLog("u1", "app", 0, 1, "OOM about to happen")
	h.writeLog("u1", "app", 1, 2, "restarted")
	h.cl.set(pod("u1", "app1", run(1)), pod("u2", "app1", run(0)))
	h.tick()
	dying := pod("u1", "app1", Container{Name: "app", Restart: 1, Terminated: true, Reason: "OOMKilled", ExitCode: 137})
	dying.Terminating = true
	h.writeLog("u1", "app", 1, 3, "final output while terminating")
	h.cl.set(dying, pod("u2", "app1", run(0)))
	h.tick()
	h.cl.set() // both pods are deleted
	h.tick()

	for _, l := range h.stored("app1") {
		t.Logf("stored: %s", l)
	}
	for _, e := range h.rec.ends {
		t.Logf("ended: app=%s seen=%v reason=%q", e.App, e.Seen, e.Reason)
	}
	for _, g := range h.rec.gaps {
		t.Logf("gap: pod=%s seconds=%d note=%q", g.UID, g.Seconds, g.Note)
	}
	want(t, h.stored("app1"), "u1/r0:boot", "u2/r0:api boot", "u1/r0:OOM about to happen", "u1/r1:restarted", "u1/r1:final output while terminating")
	reasons := map[string]EndReason{}
	for _, e := range h.rec.ends {
		for _, other := range []string{"u1", "u2"} {
			seen := false
			for _, s := range e.Seen {
				seen = seen || s == other
			}
			if !seen && e.Reason != EndReplaced || !seen && other == "u2" {
				reasons[other] = e.Reason
			}
		}
	}
	if reasons["u1"] != EndOOMKilled || reasons["u2"] != EndReplaced {
		t.Fatalf("reasons %v", reasons)
	}
	if len(h.rec.gaps) != 1 || h.rec.gaps[0].UID != "u2" || h.rec.gaps[0].Note != NoteAPIGap || h.rec.gaps[0].Seconds != 5 {
		t.Fatalf("gaps %+v (only the API pod may have a gap; the drained file pod has none)", h.rec.gaps)
	}
}

func TestCollectorAPISourceAdvancesSinceWithoutDuplicates(t *testing.T) {
	h := newHarness(t)
	h.cl.set(pod("u1", "app1", run(0)))
	all := func(n int) []Line {
		var l []Line
		for i := 0; i < n; i++ {
			l = append(l, Line{Time: base.Add(time.Duration(i) * time.Second), Message: fmt.Sprintf("m%d", i)})
		}
		return l
	}
	key := "hp-a/u1/app/false"
	h.cl.api[key] = all(2)
	h.tick()
	h.cl.api[key] = all(3) // the API repeats the boundary line
	h.tick()
	h.tick()
	want(t, h.stored("app1"), "u1/r0:m0", "u1/r0:m1", "u1/r0:m2")
	var c apiCursor
	if err := json.Unmarshal(h.rec.cursors["u1|app|0"], &c); err != nil || !c.Since.Equal(base.Add(2*time.Second)) {
		t.Fatalf("cursor %s %v", h.rec.cursors["u1|app|0"], err)
	}
	if h.rec.pods["u1"].Source != SourceAPI {
		t.Fatalf("source %v", h.rec.pods["u1"].Source)
	}
}

func TestCollectorMaskerFailureStoresNothing(t *testing.T) {
	h := newHarness(t)
	h.cl.maskErr = fmt.Errorf("secret unreadable")
	h.cl.set(pod("u1", "app1", run(0)))
	h.writeLog("u1", "app", 0, 0, "token SECRET-xyz")
	h.tick()
	h.tick()
	if got := h.stored("app1"); len(got) != 0 {
		t.Fatalf("stored %v", got)
	}
	if len(h.rec.cursors) != 0 {
		t.Fatalf("cursors saved %v", h.rec.cursors)
	}
	h.cl.set() // the pod vanishes before masking ever worked
	h.tick()
	if len(h.rec.gaps) != 1 || h.rec.gaps[0].Note != NoteAPIGap {
		t.Fatalf("gaps %+v", h.rec.gaps)
	}
	// Recovery: a pod whose masker works again is collected normally.
	h.cl.maskErr = nil
	h.cl.set(pod("u1", "app1", run(0)))
	h.tick()
	want(t, h.stored("app1"), "u1/r0:token ***")
}

func TestCollectorMasksSecretsBeforeStoring(t *testing.T) {
	h := newHarness(t)
	h.cl.set(pod("u1", "app1", run(0)), pod("u2", "app1", run(0)))
	h.writeLog("u1", "app", 0, 0, "password=SECRET-xyz")
	h.cl.api["hp-a/u2/app/false"] = []Line{{Time: base, Message: "key SECRET-xyz"}}
	os.RemoveAll(filepath.Join(h.logs, "hp-a_u2_u2"))
	h.tick()
	for _, l := range h.stored("app1") {
		if strings.Contains(l, "SECRET-xyz") || !strings.Contains(l, "***") {
			t.Fatalf("unmasked line %q", l)
		}
	}
	filepath.Walk(h.c.Store.Root, func(p string, i os.FileInfo, _ error) error {
		if i != nil && !i.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "SECRET-xyz") {
				t.Fatalf("secret on disk in %s", p)
			}
		}
		return nil
	})
}

func TestTickWithoutLeaseCollectsNothing(t *testing.T) {
	h := newHarness(t)
	h.rec.deny = true
	h.cl.set(pod("u1", "app1", run(0)))
	h.writeLog("u1", "app", 0, 0, "one")
	h.tick()
	if h.cl.listed != 0 || len(h.stored("app1")) != 0 || len(h.rec.pods) != 0 {
		t.Fatal("collected without the lease")
	}
}

func TestTickBudgetIsSharedFairlyBetweenNoisyApps(t *testing.T) {
	h := newHarness(t)
	h.c.TickBytes = 3000
	var many []string
	for i := 0; i < 100; i++ {
		many = append(many, fmt.Sprintf("n%03d-%s", i, strings.Repeat("y", 90)))
	}
	h.cl.set(pod("a", "app1", run(0)), pod("b", "app2", run(0)))
	h.writeLog("a", "app", 0, 0, many...)
	h.writeLog("b", "app", 0, 0, many...)
	h.tick()
	a, b := len(h.stored("app1")), len(h.stored("app2"))
	if a == 0 || b == 0 || a == 100 || b == 100 || a > 2*b || b > 2*a {
		t.Fatalf("unfair or unbounded: app1=%d app2=%d", a, b)
	}
}

func TestTickPermissionDeniedFallsBackToAPI(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	h := newHarness(t)
	h.cl.set(pod("u1", "app1", run(0)))
	h.writeLog("u1", "app", 0, 0, "from file")
	if err := os.Chmod(filepath.Join(h.logs, "hp-a_u1_u1", "app", "0.log"), 0); err != nil {
		t.Fatal(err)
	}
	h.cl.api["hp-a/u1/app/false"] = []Line{{Time: base, Message: "from api"}}
	h.tick()
	h.tick()
	want(t, h.stored("app1"), "u1/r0:from api")
	if h.cl.apiCalls < 2 || !h.c.permWarned || h.rec.pods["u1"].Source != SourceAPI {
		t.Fatalf("api calls %d warned %v source %v", h.cl.apiCalls, h.c.permWarned, h.rec.pods["u1"].Source)
	}
}

func TestCollectorAPISameTimestampLinesAreNeitherLostNorRepeated(t *testing.T) {
	h := newHarness(t)
	h.cl.set(pod("u1", "app1", run(0)))
	key := "hp-a/u1/app/false"
	same := func(msgs ...string) []Line {
		var l []Line
		for _, m := range msgs {
			l = append(l, Line{Time: base, Message: m})
		}
		return l
	}
	h.cl.api[key] = same("A", "B")
	h.tick()
	h.cl.api[key] = same("A", "B", "C") // C shares the boundary timestamp
	h.tick()
	h.tick()
	want(t, h.stored("app1"), "u1/r0:A", "u1/r0:B", "u1/r0:C")
	// A re-read that does not match the stored prefix keeps everything.
	h.cl.api[key] = same("X", "Y", "Z")
	h.tick()
	if n := len(h.stored("app1")); n != 6 {
		t.Fatalf("a mismatching re-read must store all lines, stored %d", n)
	}
}

func TestTickRestartJumpRecordsGapForSkippedRuns(t *testing.T) {
	h := newHarness(t)
	h.cl.set(pod("u1", "app1", run(0)))
	h.writeLog("u1", "app", 0, 0, "boot")
	h.tick()
	h.writeLog("u1", "app", 2, 5, "previous run")
	h.writeLog("u1", "app", 3, 9, "current run")
	h.cl.set(pod("u1", "app1", run(3)))
	h.tick()
	want(t, h.stored("app1"), "u1/r0:boot", "u1/r2:previous run", "u1/r3:current run")
	if len(h.rec.gaps) != 1 || h.rec.gaps[0].Note != NoteRestartGap || h.rec.gaps[0].Seconds != 3600 {
		t.Fatalf("gaps %+v", h.rec.gaps)
	}
	h.tick()
	if len(h.rec.gaps) != 1 {
		t.Fatalf("gap repeated: %+v", h.rec.gaps)
	}
}

func TestCollectorMasksCompleteLineBeforeCutting(t *testing.T) {
	h := newHarness(t)
	h.cl.set(pod("u1", "app1", run(0)))
	long := strings.Repeat("a", MaxLineBytes-4) + "SECRET-xyz" + strings.Repeat("b", 100)
	huge := strings.Repeat("c", maxMaskLine+10)
	h.cl.api["hp-a/u1/app/false"] = []Line{{Time: base, Message: long}, {Time: base.Add(time.Second), Message: huge}}
	h.tick()
	res, err := h.c.Store.Read(context.Background(), ReadQuery{ApplicationID: "app1", From: base.Add(-time.Hour), To: base.Add(time.Hour), Limit: 10})
	if err != nil || len(res.Entries) != 2 {
		t.Fatalf("entries %v err %v", res.Entries, err)
	}
	first, second := res.Entries[0].Line, res.Entries[1].Line
	if !first.Truncated || len(first.Message) > MaxLineBytes || strings.Contains(first.Message, "SECR") || strings.Contains(first.Message, "xyz") {
		t.Fatalf("secret fragment survived the cut (len %d)", len(first.Message))
	}
	if second.Message != lineWithheld {
		t.Fatalf("oversized line stored as %.40q", second.Message)
	}
}

func TestTickRestartDrainContinuesAcrossTicksWhenBudgetStopsIt(t *testing.T) {
	h := newHarness(t)
	h.cl.set(pod("u1", "app1", run(0)))
	h.writeLog("u1", "app", 0, 0, "boot")
	h.tick()
	var many []string
	for i := 0; i < 20; i++ {
		many = append(many, fmt.Sprintf("old-%02d-%s", i, strings.Repeat("z", 90)))
	}
	h.writeLog("u1", "app", 0, 1, many...)
	h.writeLog("u1", "app", 1, 30, "new run")
	h.cl.set(pod("u1", "app1", run(1)))
	h.c.TickBytes = 1000
	h.tick()
	if _, ok := h.rec.cursors["u1|app|1"]; ok {
		t.Fatal("new run was started before the previous run was fully read")
	}
	for i := 0; i < 20 && len(h.stored("app1")) < 22; i++ {
		h.tick()
	}
	got := h.stored("app1")
	if len(got) != 22 || got[0] != "u1/r0:boot" || got[21] != "u1/r1:new run" {
		t.Fatalf("stored %d lines: first %q last %q", len(got), got[0], got[len(got)-1])
	}
}

func TestCollectorAPIBurstWithinOneSecondIsRecordedAsGap(t *testing.T) {
	h := newHarness(t)
	h.c.InstanceBytes, h.c.TickBytes = 1000, 4000
	h.cl.set(pod("u1", "app1", run(0)))
	var burst []Line
	for i := 0; i < 100; i++ {
		burst = append(burst, Line{Time: base.Add(time.Duration(i) * time.Millisecond), Message: "burst"})
	}
	key := "hp-a/u1/app/false"
	h.cl.api[key] = burst
	h.tick()
	h.tick()
	h.cl.api[key] = append(burst, Line{Time: base.Add(2 * time.Second), Message: "after the burst"})
	h.tick()
	h.tick()
	found := false
	for _, g := range h.rec.gaps {
		found = found || g.Note == NoteBurstGap
	}
	got := h.stored("app1")
	if !found || got[len(got)-1] != "u1/r0:after the burst" {
		t.Fatalf("gaps %+v last %q", h.rec.gaps, got[len(got)-1])
	}
}

func TestCollectorRebuildsMaskerEveryTenMinutes(t *testing.T) {
	h := newHarness(t)
	now := base
	h.c.Now = func() time.Time { return now }
	h.cl.set(pod("u1", "app1", run(0)))
	h.writeLog("u1", "app", 0, 0, "one")
	h.tick()
	h.tick()
	if h.cl.maskCalls != 1 {
		t.Fatalf("masker built %d times", h.cl.maskCalls)
	}
	now = now.Add(11 * time.Minute)
	h.tick()
	if h.cl.maskCalls != 2 {
		t.Fatalf("masker built %d times after 11 minutes", h.cl.maskCalls)
	}
}

func TestTickVanishedAPIPodThatFinishedHasNoGap(t *testing.T) {
	h := newHarness(t)
	done := Container{Name: "app", Terminated: true, Reason: "Completed"}
	h.cl.set(pod("u1", "app1", done))
	h.cl.api["hp-a/u1/app/false"] = []Line{{Time: base, Message: "all done"}}
	h.tick()
	h.cl.set()
	h.tick()
	if len(h.rec.gaps) != 0 {
		t.Fatalf("false gap %+v", h.rec.gaps)
	}
}

// TestCollectorPeakHeap measures extra heap while 20 noisy pods are collected.
func TestCollectorPeakHeap(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector makes the masker too slow for a 20 pod load; run without -race")
	}
	h := newHarness(t)
	var pods []Pod
	line := strings.Repeat("m", 200)
	for i := 0; i < 20; i++ {
		uid := fmt.Sprintf("noisy%02d", i)
		pods = append(pods, pod(uid, fmt.Sprintf("app%d", i%5), run(0)))
		var msgs []string
		for j := 0; j < 10000; j++ { // about 2.4 MiB of CRI records per pod
			msgs = append(msgs, line)
		}
		h.writeLog(uid, "app", 0, 0, msgs...)
	}
	h.cl.set(pods...)
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	baseline := ms.HeapAlloc
	var peak uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak {
					peak = m.HeapAlloc
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()
	h.tick()
	h.tick()
	close(stop)
	<-done
	extra := int64(peak) - int64(baseline)
	t.Logf("baseline heap %d KiB, peak heap %d KiB, extra %.1f MiB", baseline>>10, peak>>10, float64(extra)/(1<<20))
	if extra > 48<<20 {
		t.Fatalf("collector used %d MiB of extra heap", extra>>20)
	}
}

func TestCollectorFirstSweepRecordsRestartGapAndShutdownReleasesLease(t *testing.T) {
	h := newHarness(t)
	now := base.Add(time.Hour)
	h.c.Now = func() time.Time { return now }
	h.rec.open = map[string]time.Time{"old": now.Add(-120 * time.Second), "older": now.Add(-5 * time.Hour)}
	h.cl.set(pod("u1", "app1", run(0)))
	h.tick()
	h.tick() // the sweep happens once per app per process
	got := map[string]int{}
	for _, g := range h.rec.gaps {
		if g.Note != NoteRestartingGap {
			t.Fatalf("unexpected gap %+v", g)
		}
		got[g.UID] += g.Seconds
	}
	if len(h.rec.gaps) != 2 || got["old"] != 120 || got["older"] != 3600 {
		t.Fatalf("gaps %+v", h.rec.gaps)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.c.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	if h.rec.release != "test" {
		t.Fatalf("lease not released, owner %q", h.rec.release)
	}
}

func TestCollectorPrivateKeyBlockSurvivesProcessRestart(t *testing.T) {
	h := newHarness(t)
	h.cl.set(pod("u1", "app1", run(0)))
	h.writeLog("u1", "app", 0, 0, "before", "-----BEGIN PRIVATE KEY-----", "BODYONE")
	h.tick()
	if !pkSet(h.rec.cursors["u1|app|0"]) {
		t.Fatalf("cursor lacks the pk flag: %s", h.rec.cursors["u1|app|0"])
	}
	// A new process: fresh collector and fresh masker, same store and cursors.
	h2 := &Collector{Cluster: h.cl, Records: h.rec, Store: h.c.Store, Tailer: h.c.Tailer, Owner: "test"}
	h.writeLog("u1", "app", 0, 3, "BODYTWO", "-----END PRIVATE KEY-----", "after")
	if err := h2.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := h.stored("app1")
	for _, l := range got {
		if strings.Contains(l, "BODY") {
			t.Fatalf("key body stored: %q", l)
		}
	}
	if len(got) != 6 || got[5] != "u1/r0:after" || pkSet(h.rec.cursors["u1|app|0"]) {
		t.Fatalf("stored %v cursor %s", got, h.rec.cursors["u1|app|0"])
	}
}

func TestCollectorDoesNotRebuildMaskerInsideAPrivateKeyBlock(t *testing.T) {
	h := newHarness(t)
	now := base
	h.c.Now = func() time.Time { return now }
	h.cl.set(pod("u1", "app1", run(0)))
	h.writeLog("u1", "app", 0, 0, "-----BEGIN PRIVATE KEY-----", "BODY")
	h.tick()
	now = now.Add(11 * time.Minute)
	h.tick()
	if h.cl.maskCalls != 1 {
		t.Fatalf("masker rebuilt inside a key block (%d builds)", h.cl.maskCalls)
	}
	h.writeLog("u1", "app", 0, 2, "-----END PRIVATE KEY-----")
	h.tick()
	h.tick()
	if h.cl.maskCalls != 2 {
		t.Fatalf("masker not rebuilt after the block ended (%d builds)", h.cl.maskCalls)
	}
}

func (h *harness) writeRaw(uid, container string, restart int, text string) {
	h.t.Helper()
	dir := filepath.Join(h.logs, "hp-a_"+uid+"_"+uid, container)
	os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%d.log", restart)), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(text)
}

func TestTickPreviousRunWithOpenRecordIsEmittedAndFinished(t *testing.T) {
	h := newHarness(t)
	h.cl.set(pod("u1", "app1", run(0)))
	h.writeLog("u1", "app", 0, 0, "boot")
	h.tick()
	// The crashed run ended in the middle of a line: it will never be finished.
	h.writeRaw("u1", "app", 0, base.Add(time.Second).Format(time.RFC3339Nano)+" stdout P half a li\n")
	h.writeLog("u1", "app", 1, 5, "new run")
	h.cl.set(pod("u1", "app1", run(1)))
	h.tick()
	want(t, h.stored("app1"), "u1/r0:boot", "u1/r0:half a li", "u1/r1:new run")
	if _, ok := h.rec.cursors["u1|app|1"]; !ok {
		t.Fatal("new run cursor missing")
	}
}

func TestTickBudgetIsChargedByBytesConsumed(t *testing.T) {
	h := newHarness(t)
	h.c.TickBytes = 100 << 10
	h.cl.set(pod("u1", "app1", run(0)))
	big := strings.Repeat("w", 70<<10) // withheld by the tailer: tiny output, large read
	h.writeLog("u1", "app", 0, 0, big, big, big)
	h.tick()
	if n := len(h.stored("app1")); n == 0 || n == 3 {
		t.Fatalf("one tick stored %d of 3 oversized lines; the budget must follow bytes read", n)
	}
	for i := 0; i < 5; i++ {
		h.tick()
	}
	if n := len(h.stored("app1")); n != 3 {
		t.Fatalf("stored %d", n)
	}
}

func TestCollectorBurstRetryIsChargedAndGapFollowsARealRetry(t *testing.T) {
	h := newHarness(t)
	h.c.InstanceBytes, h.c.TickBytes = 4000, 1000
	h.cl.set(pod("u1", "app1", run(0)))
	var burst []Line
	for i := 0; i < 300; i++ {
		burst = append(burst, Line{Time: base.Add(time.Duration(i) * time.Millisecond), Message: "burst"})
	}
	h.cl.api["hp-a/u1/app/false"] = burst
	for i := 0; i < 4; i++ {
		h.tick()
	}
	for _, g := range h.rec.gaps {
		if g.Note == NoteBurstGap {
			t.Fatal("gap recorded although the retry was never affordable")
		}
	}
	h.c.TickBytes = 20000
	for i := 0; i < 6; i++ {
		h.tick()
	}
	found := false
	for _, g := range h.rec.gaps {
		found = found || g.Note == NoteBurstGap
	}
	if !found {
		t.Fatal("gap missing after an affordable retry")
	}
}

func TestCollectorBoundaryLinesAreNotMaskedTwice(t *testing.T) {
	h := newHarness(t)
	h.cl.set(pod("u1", "app1", run(0)))
	key := "hp-a/u1/app/false"
	at := func(s int, m string) Line { return Line{Time: base.Add(time.Duration(s) * time.Second), Message: m} }
	h.cl.api[key] = []Line{at(0, "-----BEGIN PRIVATE KEY-----"), at(0, "BODYLINE")}
	h.tick()
	h.cl.api[key] = append(h.cl.api[key], at(0, "BODY2"), at(1, "-----END PRIVATE KEY-----"), at(2, "after"))
	h.tick()
	if h.cl.maskCalls != 1 || h.cl.lines["-----BEGIN PRIVATE KEY-----"] != 1 || h.cl.lines["BODYLINE"] != 1 {
		t.Fatalf("stored lines were masked again: builds %d begin %d body %d", h.cl.maskCalls, h.cl.lines["-----BEGIN PRIVATE KEY-----"], h.cl.lines["BODYLINE"])
	}
	for _, l := range h.stored("app1") {
		if strings.Contains(l, "BODY") {
			t.Fatalf("key body stored: %q", l)
		}
	}
	if len(h.stored("app1")) != 5 {
		t.Fatalf("stored %v", h.stored("app1"))
	}
	// After a process restart only the persisted masked hash is left; a
	// throwaway masker recognises the stored prefix.
	h.cl.api[key] = append(h.cl.api[key], at(2, "after too"))
	h2 := &Collector{Cluster: h.cl, Records: h.rec, Store: h.c.Store, Tailer: h.c.Tailer, Owner: "test"}
	if err := h2.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.stored("app1"); len(got) != 6 || got[5] != "u1/r0:after too" {
		t.Fatalf("restart re-read stored %v", got)
	}
}

func TestCollectorMaskRefreshKeepsValuesLearnedFromTheLog(t *testing.T) {
	h := newHarness(t)
	now := base
	h.c.Now = func() time.Time { return now }
	h.cl.set(pod("u1", "app1", run(0)))
	h.writeLog("u1", "app", 0, 0, "::add-mask::learnedvalue99")
	h.tick()
	now = now.Add(11 * time.Minute)
	h.writeLog("u1", "app", 0, 1, "leak learnedvalue99 here")
	h.tick()
	if h.cl.maskCalls != 2 {
		t.Fatalf("masker builds %d", h.cl.maskCalls)
	}
	for _, l := range h.stored("app1") {
		if strings.Contains(l, "learnedvalue99") {
			t.Fatalf("learned mask lost after refresh: %q", l)
		}
	}
}
