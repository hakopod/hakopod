package applogs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type segFakeIndex struct {
	mu   sync.Mutex
	segs map[string]Segment
}

func (f *segFakeIndex) SaveSegment(_ context.Context, s Segment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.segs[s.ID] = s
	return nil
}

func (f *segFakeIndex) sorted(keep func(Segment) bool, less func(a, b Segment) bool, limit int) []Segment {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Segment
	for _, s := range f.segs {
		if keep(s) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func segByFirst(a, b Segment) bool {
	if !a.FirstTime.Equal(b.FirstTime) {
		return a.FirstTime.Before(b.FirstTime)
	}
	return a.ID < b.ID
}

func segHas(list []string, v string) bool {
	if len(list) == 0 {
		return true
	}
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (f *segFakeIndex) Segments(_ context.Context, q SegmentQuery) ([]Segment, error) {
	return f.sorted(func(s Segment) bool {
		return s.Instance.ApplicationID == q.ApplicationID && !s.LastTime.Before(q.From) && !s.FirstTime.After(q.To) &&
			segHas(q.PodUIDs, s.Instance.PodUID) && segHas(q.Services, s.Instance.Service)
	}, segByFirst, min(max(q.Limit, 1), 500)), nil
}

func (f *segFakeIndex) DeleteSegments(_ context.Context, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		delete(f.segs, id)
	}
	return nil
}

func (f *segFakeIndex) ExpiredSegments(_ context.Context, before time.Time, limit int) ([]Segment, error) {
	return f.sorted(func(s Segment) bool { return s.LastTime.Before(before) }, segByFirst, limit), nil
}

func (f *segFakeIndex) ApplicationsOverBytes(_ context.Context, max int64, limit int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sum := map[string]int64{}
	for _, s := range f.segs {
		sum[s.Instance.ApplicationID] += s.Bytes
	}
	var out []string
	for a, n := range sum {
		if n > max && len(out) < limit {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *segFakeIndex) OldestSegments(_ context.Context, app string, limit int) ([]Segment, error) {
	return f.sorted(func(s Segment) bool { return s.Instance.ApplicationID == app }, segByFirst, limit), nil
}

var segT0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

type segEnv struct {
	t   *testing.T
	s   *Store
	idx *segFakeIndex
	now time.Time
	ctx context.Context
}

func newSegEnv(t *testing.T) *segEnv {
	e := &segEnv{t: t, idx: &segFakeIndex{segs: map[string]Segment{}}, now: segT0, ctx: context.Background()}
	e.s = &Store{Root: t.TempDir(), Index: e.idx, Now: func() time.Time { return e.now }}
	return e
}

func segInst(app, pod string) Instance {
	return Instance{ApplicationID: app, Service: "web", PodUID: pod, PodName: "web-" + pod, Container: "app"}
}

func segLines(prefix string, n int, start time.Time, step time.Duration) []Line {
	out := make([]Line, n)
	for i := range out {
		out[i] = Line{Time: start.Add(time.Duration(i) * step), Stream: "stdout", Message: fmt.Sprintf("%s%d", prefix, i)}
	}
	return out
}

func (e *segEnv) append(i Instance, l []Line) {
	e.t.Helper()
	if err := e.s.Append(e.ctx, i, l); err != nil {
		e.t.Fatal(err)
	}
}

func (e *segEnv) read(q ReadQuery) ReadResult {
	e.t.Helper()
	if q.ApplicationID == "" {
		q.ApplicationID = "app1"
	}
	if q.From.IsZero() {
		q.From = segT0.Add(-time.Hour)
	}
	if q.To.IsZero() {
		q.To = segT0.Add(time.Hour)
	}
	r, err := e.s.Read(e.ctx, q)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func segMsgs(r ReadResult) []string {
	var out []string
	for _, e := range r.Entries {
		out = append(out, e.Line.Message)
	}
	return out
}

func segEq(t *testing.T, got []string, want string) {
	t.Helper()
	if g := strings.Join(got, ","); g != want {
		t.Fatalf("got %s want %s", g, want)
	}
}

func TestAppendReadOrdered(t *testing.T) {
	e := newSegEnv(t)
	e.append(segInst("app1", "p1"), segLines("a", 3, segT0, time.Second))
	e.append(segInst("app1", "p1"), segLines("b", 2, segT0.Add(10*time.Second), time.Second))
	segEq(t, segMsgs(e.read(ReadQuery{})), "a0,a1,a2,b0,b1")
	if len(e.idx.segs) != 1 {
		t.Fatalf("want one segment, got %d", len(e.idx.segs))
	}
	for _, s := range e.idx.segs {
		if s.Lines != 5 || !s.FirstTime.Equal(segT0) || !s.LastTime.Equal(segT0.Add(11*time.Second)) || s.Bytes == 0 || s.Sealed {
			t.Fatalf("bad row %+v", s)
		}
		fi, err := os.Stat(filepath.Join(e.s.Root, s.Path))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %v %v", fi, err)
		}
		di, _ := os.Stat(filepath.Dir(filepath.Join(e.s.Root, s.Path)))
		if di.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v", di.Mode())
		}
	}
}

func TestReadMergeAcrossInstances(t *testing.T) {
	e := newSegEnv(t)
	e.append(segInst("app1", "p1"), []Line{{Time: segT0, Message: "a0"}, {Time: segT0.Add(2 * time.Second), Message: "a2"}})
	e.append(segInst("app1", "p2"), []Line{{Time: segT0.Add(time.Second), Message: "b1"}, {Time: segT0.Add(3 * time.Second), Message: "b3"}})
	e.append(segInst("app2", "p3"), []Line{{Time: segT0, Message: "other"}})
	r := e.read(ReadQuery{})
	segEq(t, segMsgs(r), "a0,b1,a2,b3")
	if r.Entries[1].Instance.PodUID != "p2" {
		t.Fatal("entry lost its instance")
	}
	segEq(t, segMsgs(e.read(ReadQuery{PodUIDs: []string{"p2"}})), "b1,b3")
}

func TestReadWindow(t *testing.T) {
	e := newSegEnv(t)
	e.append(segInst("app1", "p1"), segLines("m", 10, segT0, time.Second))
	segEq(t, segMsgs(e.read(ReadQuery{From: segT0.Add(3 * time.Second), To: segT0.Add(5 * time.Second)})), "m3,m4,m5")
}

func TestReadCursorEqualTimestamps(t *testing.T) {
	for _, desc := range []bool{false, true} {
		t.Run(fmt.Sprintf("desc=%v", desc), func(t *testing.T) {
			e := newSegEnv(t)
			// 14 lines, all with the same timestamp, over two instances; one
			// instance is sealed so the cursor spans plain and gzip files.
			e.append(segInst("app1", "p1"), segLines("a", 7, segT0, 0))
			e.append(segInst("app1", "p2"), segLines("b", 7, segT0, 0))
			e.now = e.now.Add(SegmentSealAge)
			if err := e.s.SealIdle(e.ctx); err != nil {
				t.Fatal(err)
			}
			e.append(segInst("app1", "p2"), segLines("c", 3, segT0, 0))
			seen := map[string]int{}
			cur, pages := "", 0
			for {
				r := e.read(ReadQuery{Limit: 3, Cursor: cur, Descending: desc})
				for _, m := range segMsgs(r) {
					seen[m]++
				}
				pages++
				if r.NextCursor == "" {
					break
				}
				if pages > 20 {
					t.Fatal("pagination does not end")
				}
				cur = r.NextCursor
			}
			if len(seen) != 17 {
				t.Fatalf("want 17 distinct lines, got %d: %v", len(seen), seen)
			}
			for m, n := range seen {
				if n != 1 {
					t.Fatalf("%s returned %d times", m, n)
				}
			}
			t.Logf("17 lines with one timestamp read in %d pages of 3, no duplicates, no gaps", pages)
		})
	}
	if _, err := newSegEnv(t).s.Read(context.Background(), ReadQuery{ApplicationID: "app1", Cursor: "%%"}); err == nil {
		t.Fatal("bad cursor accepted")
	}
}

func TestReadDescending(t *testing.T) {
	e := newSegEnv(t)
	e.append(segInst("app1", "p1"), segLines("a", 3, segT0, 2*time.Second))
	e.append(segInst("app1", "p2"), segLines("b", 3, segT0.Add(time.Second), 2*time.Second))
	segEq(t, segMsgs(e.read(ReadQuery{Descending: true})), "b2,a2,b1,a1,b0,a0")
	segEq(t, segMsgs(e.read(ReadQuery{Descending: true, Limit: 2})), "b2,a2")
}

func TestSealBySize(t *testing.T) {
	e := newSegEnv(t)
	big := strings.Repeat("x", 15000)
	var l []Line
	for i := 0; i < SegmentSealBytes/15000+5; i++ {
		l = append(l, Line{Time: segT0.Add(time.Duration(i) * time.Millisecond), Message: big})
	}
	i := segInst("app1", "p1")
	e.append(i, l)
	var seg Segment
	for _, s := range e.idx.segs {
		seg = s
	}
	if !seg.Sealed || !strings.HasSuffix(seg.Path, ".ndjson.gz") || seg.Bytes >= SegmentSealBytes {
		t.Fatalf("not sealed: %+v", seg)
	}
	if _, err := os.Stat(filepath.Join(e.s.Root, strings.TrimSuffix(seg.Path, ".gz"))); !os.IsNotExist(err) {
		t.Fatal("plain file still present")
	}
	e.append(i, segLines("n", 1, segT0.Add(time.Hour), 0))
	if len(e.idx.segs) != 2 {
		t.Fatalf("want a new segment after sealing, got %d", len(e.idx.segs))
	}
	r := e.read(ReadQuery{To: segT0.Add(2 * time.Hour), Limit: MaxQueryLines})
	if len(r.Entries) != len(l)+1 || r.Entries[len(l)].Line.Message != "n0" {
		t.Fatalf("got %d entries", len(r.Entries))
	}
}

func TestSealByAge(t *testing.T) {
	e := newSegEnv(t)
	i := segInst("app1", "p1")
	e.append(i, segLines("a", 1, segT0, 0))
	e.now = e.now.Add(SegmentSealAge - time.Second)
	e.append(i, segLines("b", 1, segT0, 0))
	for _, s := range e.idx.segs {
		if s.Sealed {
			t.Fatal("sealed too early")
		}
	}
	e.now = e.now.Add(time.Second)
	e.append(i, segLines("c", 1, segT0, 0))
	for _, s := range e.idx.segs {
		if !s.Sealed || s.Lines != 3 {
			t.Fatalf("want sealed with 3 lines: %+v", s)
		}
	}
	segEq(t, segMsgs(e.read(ReadQuery{})), "a0,b0,c0")
}

func TestSealIdle(t *testing.T) {
	e := newSegEnv(t)
	e.append(segInst("app1", "p1"), segLines("a", 1, segT0, 0))
	if err := e.s.SealIdle(e.ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.s.open) != 1 {
		t.Fatal("young segment sealed")
	}
	e.now = e.now.Add(SegmentSealAge)
	if err := e.s.SealIdle(e.ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.s.open) != 0 {
		t.Fatal("idle segment not sealed")
	}
}

// A restarted process segHas no open map: the active file left by the old process
// is read as plain, and a seal that died before saving the row is read as .gz.
func TestReadAfterRestartAndHalfSeal(t *testing.T) {
	e := newSegEnv(t)
	e.append(segInst("app1", "p1"), segLines("a", 2, segT0, time.Second))
	e.append(segInst("app1", "p2"), segLines("b", 2, segT0.Add(500*time.Millisecond), time.Second))
	var half Segment
	for _, s := range e.idx.segs {
		if s.Instance.PodUID == "p2" {
			half = s
		}
	}
	p := filepath.Join(e.s.Root, half.Path)
	if err := gzipFile(p, p+".gz"); err != nil {
		t.Fatal(err)
	}
	os.Remove(p) // row still says unsealed and names the plain path
	fresh := &Store{Root: e.s.Root, Index: e.idx, Now: e.s.Now}
	r, err := fresh.Read(e.ctx, ReadQuery{ApplicationID: "app1", From: segT0.Add(-time.Hour), To: segT0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	segEq(t, segMsgs(r), "a0,b0,a1,b1")
}

func TestCorruptTrailingLine(t *testing.T) {
	e := newSegEnv(t)
	e.append(segInst("app1", "p1"), segLines("a", 2, segT0, time.Second))
	for _, s := range e.idx.segs {
		f, _ := os.OpenFile(filepath.Join(e.s.Root, s.Path), os.O_APPEND|os.O_WRONLY, 0)
		f.WriteString(`{"ts":"2026-01-01T12:00:09Z","msg":"cut o`)
		f.Close()
	}
	segEq(t, segMsgs(e.read(ReadQuery{})), "a0,a1")
}

func TestScanLimit(t *testing.T) {
	e := newSegEnv(t)
	e.s.scanLimit = 600
	e.append(segInst("app1", "p1"), segLines("a", 20, segT0, time.Second))
	e.append(segInst("app1", "p2"), segLines("b", 20, segT0.Add(time.Minute), time.Second))
	r := e.read(ReadQuery{})
	if !r.ScanLimited || len(r.Entries) != 20 || r.NextCursor == "" {
		t.Fatalf("scan limit not applied: %d entries limited=%v", len(r.Entries), r.ScanLimited)
	}
}

// Overlapping segments and a budget of about two and a half segments: every
// line must still come back exactly once when the cursors are followed.
func TestScanLimitFollowCursors(t *testing.T) {
	for _, desc := range []bool{false, true} {
		t.Run(fmt.Sprintf("desc=%v", desc), func(t *testing.T) {
			e := newSegEnv(t)
			const segs, per = 12, 10
			for j := 0; j < segs; j++ { // segment j covers [5j, 5j+9] ms, so neighbours overlap
				e.append(segInst("app1", fmt.Sprintf("p%d", j)), segLines(fmt.Sprintf("s%d_", j), per, segT0.Add(time.Duration(5*j)*time.Millisecond), time.Millisecond))
			}
			for _, s := range e.idx.segs {
				e.s.scanLimit = s.Bytes * 5 / 2
				break
			}
			seen := map[string]int{}
			cur, pages, limited := "", 0, false
			for {
				r := e.read(ReadQuery{Limit: 7, Cursor: cur, Descending: desc})
				limited = limited || r.ScanLimited
				for _, m := range segMsgs(r) {
					seen[m]++
				}
				pages++
				if r.NextCursor == "" {
					break
				}
				if pages > 200 {
					t.Fatal("pagination does not end")
				}
				cur = r.NextCursor
			}
			if !limited {
				t.Fatal("test never hit the scan limit")
			}
			if len(seen) != segs*per {
				t.Fatalf("want %d lines, got %d", segs*per, len(seen))
			}
			for m, n := range seen {
				if n != 1 {
					t.Fatalf("%s returned %d times", m, n)
				}
			}
		})
	}
}

// Six segments that all cover the same window and a budget of 2.5 segments:
// the first pass cannot make progress, so Read must read past the budget
// instead of returning an empty page with the same cursor forever.
func TestScanLimitHeavyOverlapProgress(t *testing.T) {
	for _, desc := range []bool{false, true} {
		t.Run(fmt.Sprintf("desc=%v", desc), func(t *testing.T) {
			e := newSegEnv(t)
			const segs, per = 6, 20
			for j := 0; j < segs; j++ { // times interleave: segment j owns ms j, j+6, ...
				e.append(segInst("app1", fmt.Sprintf("p%d", j)), segLines(fmt.Sprintf("s%d_", j), per, segT0.Add(time.Duration(j)*time.Millisecond), segs*time.Millisecond))
			}
			for _, s := range e.idx.segs {
				e.s.scanLimit = s.Bytes * 5 / 2
				break
			}
			seen := map[string]int{}
			cur, pages := "", 0
			for {
				r := e.read(ReadQuery{Limit: 7, Cursor: cur, Descending: desc})
				if len(r.Entries) == 0 && (r.NextCursor == "" || r.NextCursor == cur) {
					if r.NextCursor == cur && cur != "" {
						t.Fatalf("page %d made no progress", pages)
					}
				}
				for _, m := range segMsgs(r) {
					seen[m]++
				}
				pages++
				if r.NextCursor == "" {
					break
				}
				if pages > 100 {
					t.Fatal("pagination does not end")
				}
				cur = r.NextCursor
			}
			if len(seen) != segs*per {
				t.Fatalf("want %d lines, got %d", segs*per, len(seen))
			}
			for m, n := range seen {
				if n != 1 {
					t.Fatalf("%s returned %d times", m, n)
				}
			}
		})
	}
}
