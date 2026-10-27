package applogs

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"container/heap"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Segment file layout and crash behaviour.
//
// Each Instance has at most one open "active" file,
// <app>/<YYYYMMDD>/<pod>_<container>_<restart>_<id>.ndjson, written in append
// mode. When it reaches SegmentSealBytes, or is older than SegmentSealAge
// (measured from creation), it is sealed: gzipped to <name>.ndjson.gz through a
// temp file and rename, fsynced, the plain file removed, then the index row is
// saved with Sealed=true and the compressed size.
//
// If the process dies part way through, the next start copes without any
// repair step: a row that still says unsealed names the plain path, and readers
// use the plain file if it exists, or the .gz next to it if sealing got that
// far. Either way the lines stay readable. A plain file left behind by a crash
// is never written to again (a new segment is started) and is removed by Prune
// like any other segment.
//
// Within one segment, lines are expected in non-decreasing time order, which is
// how the collector writes them. Read does not rely on that: it keeps the best
// Limit+1 lines seen across all matching segments.

const (
	defaultRoot     = "/var/lib/hakopod/app-logs"
	rootEnv         = "HAKOPOD_APP_LOG_DIR"
	maxOpenSegments = 256
	indexPageSize   = 500
	maxIndexPages   = 20
	maxJSONLine     = 1 << 20 // a MaxLineBytes message can grow ~6x when JSON escaped
	pruneBatch      = 200
	pruneRounds     = 50
	sizeBatch       = 1 // the index cannot report totals, so delete one and ask again
	sizeRounds      = 500
	overApps        = 50
	maxOrphanDirs   = 20
	maxRootEntries  = 1000
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Index is the small PostgreSQL index behind the segment files. It is
// implemented by the store package.
type Index interface {
	SaveSegment(ctx context.Context, s Segment) error
	Segments(ctx context.Context, q SegmentQuery) ([]Segment, error)
	DeleteSegments(ctx context.Context, ids []string) error
	ExpiredSegments(ctx context.Context, before time.Time, limit int) ([]Segment, error)
	ApplicationsOverBytes(ctx context.Context, max int64, limit int) ([]string, error)
	OldestSegments(ctx context.Context, applicationID string, limit int) ([]Segment, error)
}

// SegmentQuery returns segments whose [FirstTime, LastTime] overlaps
// [From, To], ordered by FirstTime ascending. Limit is clamped to 1..500.
type SegmentQuery struct {
	ApplicationID string
	From, To      time.Time
	PodUIDs       []string
	Services      []string
	Limit         int
}

// Store keeps log segments on disk. It is safe for concurrent use.
type Store struct {
	Root  string
	Index Index
	Now   func() time.Time

	mu        sync.Mutex
	open      map[string]*activeSeg
	maxOpen   int   // tests only; 0 means maxOpenSegments
	scanLimit int64 // tests only; 0 means MaxQueryScanBytes
}

type activeSeg struct {
	seg     Segment
	f       *os.File
	created time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) root() string {
	if s.Root != "" {
		return s.Root
	}
	if v := os.Getenv(rootEnv); v != "" {
		return v
	}
	return defaultRoot
}

// Available returns nil only if the log directory exists and is writable.
func (s *Store) Available() error {
	root := s.root()
	st, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("the application log directory %s is not available: %v", root, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("the application log path %s is not a directory", root)
	}
	f, err := os.CreateTemp(root, ".probe-*")
	if err != nil {
		return fmt.Errorf("the application log directory %s is not writable: %v", root, err)
	}
	f.Close()
	os.Remove(f.Name())
	return nil
}

func validName(kind, v string) error {
	if !namePattern.MatchString(v) || strings.Contains(v, "..") || v == "." {
		return fmt.Errorf("the %s is not safe to use in a log file path", kind)
	}
	return nil
}

func validInstance(i Instance) error {
	if err := validName("application ID", i.ApplicationID); err != nil {
		return err
	}
	if err := validName("pod UID", i.PodUID); err != nil {
		return err
	}
	if err := validName("container name", i.Container); err != nil {
		return err
	}
	if i.Restart < 0 {
		return errors.New("the container restart count cannot be negative")
	}
	return nil
}

// abs resolves an index path (relative to Root) and refuses anything that
// would leave Root.
func (s *Store) abs(rel string) (string, error) {
	p := filepath.FromSlash(rel)
	if p == "" || !filepath.IsLocal(p) {
		return "", errors.New("a stored log path points outside the log directory")
	}
	return filepath.Join(s.root(), p), nil
}

func newID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("could not create a log segment ID: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Append writes lines to the instance's active segment and updates its index
// row. The collector serialises calls per instance.
func (s *Store) Append(ctx context.Context, inst Instance, lines []Line) error {
	if err := validInstance(inst); err != nil {
		return err
	}
	if len(lines) == 0 {
		return nil
	}
	if s.Index == nil {
		return errors.New("the application log store has no index")
	}
	var buf bytes.Buffer
	first, last := lines[0].Time, lines[0].Time
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			return fmt.Errorf("could not encode a log line: %w", err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
		if l.Time.Before(first) {
			first = l.Time
		}
		if l.Time.After(last) {
			last = l.Time
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	key := fmt.Sprintf("%s|%s|%s|%d", inst.ApplicationID, inst.PodUID, inst.Container, inst.Restart)
	a, err := s.activeLocked(ctx, key, inst)
	if err != nil {
		return err
	}
	if _, err := a.f.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("could not write application logs: %w", err)
	}
	if a.seg.Lines == 0 || first.Before(a.seg.FirstTime) {
		a.seg.FirstTime = first
	}
	if last.After(a.seg.LastTime) {
		a.seg.LastTime = last
	}
	a.seg.Lines += int64(len(lines))
	a.seg.Bytes += int64(buf.Len())
	if err := s.Index.SaveSegment(ctx, a.seg); err != nil {
		return err
	}
	if a.seg.Bytes >= SegmentSealBytes || s.now().Sub(a.created) >= SegmentSealAge {
		return s.sealLocked(ctx, key, a)
	}
	return nil
}

func (s *Store) activeLocked(ctx context.Context, key string, inst Instance) (*activeSeg, error) {
	if a := s.open[key]; a != nil {
		return a, nil
	}
	if s.open == nil {
		s.open = map[string]*activeSeg{}
	}
	max := s.maxOpen
	if max <= 0 {
		max = maxOpenSegments
	}
	for len(s.open) >= max {
		var ok string
		for k, a := range s.open {
			if ok == "" || a.created.Before(s.open[ok].created) {
				ok = k
			}
		}
		if err := s.sealLocked(ctx, ok, s.open[ok]); err != nil {
			return nil, err
		}
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := s.now()
	rel := filepath.ToSlash(filepath.Join(inst.ApplicationID, now.UTC().Format("20060102"),
		fmt.Sprintf("%s_%s_%d_%s.ndjson", inst.PodUID, inst.Container, inst.Restart, id)))
	p, err := s.abs(rel)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, fmt.Errorf("could not create the application log directory: %w", err)
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("could not open an application log file: %w", err)
	}
	a := &activeSeg{f: f, created: now, seg: Segment{ID: id, Instance: inst, Path: rel}}
	s.open[key] = a
	return a, nil
}

// sealLocked gzips an active segment. The segment leaves the open map even on
// failure; a plain file left behind stays readable.
func (s *Store) sealLocked(ctx context.Context, key string, a *activeSeg) error {
	delete(s.open, key)
	if err := a.f.Close(); err != nil {
		return fmt.Errorf("could not close an application log file: %w", err)
	}
	plain, err := s.abs(a.seg.Path)
	if err != nil {
		return err
	}
	gz := plain + ".gz"
	if err := gzipFile(plain, gz); err != nil {
		return err
	}
	if err := os.Remove(plain); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not remove a sealed log file: %w", err)
	}
	st, err := os.Stat(gz)
	if err != nil {
		return err
	}
	a.seg.Path += ".gz"
	a.seg.Sealed = true
	a.seg.Bytes = st.Size()
	return s.Index.SaveSegment(ctx, a.seg)
}

func gzipFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("could not open a log file to seal it: %w", err)
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("could not create a sealed log file: %w", err)
	}
	defer func() {
		if err != nil {
			out.Close()
			os.Remove(tmp)
		}
	}()
	zw := gzip.NewWriter(out)
	if _, err = io.Copy(zw, in); err != nil {
		return fmt.Errorf("could not compress a log file: %w", err)
	}
	if err = zw.Close(); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// SealIdle seals every open segment older than SegmentSealAge.
func (s *Store) SealIdle(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	for k, a := range s.open {
		if s.now().Sub(a.created) >= SegmentSealAge {
			if err := s.sealLocked(ctx, k, a); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

// ReadQuery selects lines. A zero To means now; a zero From means one
// DefaultRetention before To.
type ReadQuery struct {
	ApplicationID string
	From, To      time.Time
	PodUIDs       []string
	Services      []string
	Limit         int
	Cursor        string
	Descending    bool
}

type Entry struct {
	Instance Instance
	Line     Line
}

type ReadResult struct {
	Entries     []Entry
	NextCursor  string
	ScanLimited bool
}

type cursor struct {
	T time.Time `json:"t"`
	S string    `json:"s"`
	I int       `json:"i"`
}

func (a cursor) cmp(b cursor) int {
	if c := a.T.Compare(b.T); c != 0 {
		return c
	}
	if c := strings.Compare(a.S, b.S); c != 0 {
		return c
	}
	return a.I - b.I
}

type item struct {
	k cursor
	e Entry
}

// topN is a heap whose root is the worst item kept so far.
type topN struct {
	items []item
	desc  bool
}

func (h *topN) Len() int { return len(h.items) }
func (h *topN) Less(i, j int) bool {
	c := h.items[i].k.cmp(h.items[j].k)
	if h.desc {
		return c < 0
	}
	return c > 0
}
func (h *topN) Swap(i, j int) { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *topN) Push(x any)    { h.items = append(h.items, x.(item)) }
func (h *topN) Pop() any {
	n := len(h.items) - 1
	x := h.items[n]
	h.items = h.items[:n]
	return x
}

// Read returns lines in [From, To] ordered by time (then segment ID and line
// position), merged across instances. Memory is bounded by Limit+1 lines.
func (s *Store) Read(ctx context.Context, q ReadQuery) (ReadResult, error) {
	var res ReadResult
	if s.Index == nil {
		return res, errors.New("the application log store has no index")
	}
	if err := validName("application ID", q.ApplicationID); err != nil {
		return res, err
	}
	limit := min(max(q.Limit, 1), MaxQueryLines)
	if q.Limit <= 0 {
		limit = MaxQueryLines // unset means as many as allowed
	}
	to := q.To
	if to.IsZero() {
		to = s.now()
	}
	from := q.From
	if from.IsZero() {
		from = to.Add(-DefaultRetention)
	}
	var cur *cursor
	if q.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		cur = &cursor{}
		if err != nil || json.Unmarshal(raw, cur) != nil {
			return res, errors.New("the log cursor is not valid; start the query again without it")
		}
		if !q.Descending && cur.T.After(from) {
			from = cur.T
		}
		if q.Descending && cur.T.Before(to) {
			to = cur.T
		}
	}
	if to.Before(from) {
		return res, nil
	}
	segs, truncated, err := s.matching(ctx, q, from, to)
	if err != nil {
		return res, err
	}
	res.ScanLimited = truncated
	sort.Slice(segs, func(i, j int) bool {
		a, b := segs[i], segs[j]
		if q.Descending {
			if !a.LastTime.Equal(b.LastTime) {
				return a.LastTime.After(b.LastTime)
			}
		} else if !a.FirstTime.Equal(b.FirstTime) {
			return a.FirstTime.Before(b.FirstTime)
		}
		return a.ID < b.ID
	})
	want := limit + 1
	scanLimit := s.scanLimit
	if scanLimit <= 0 {
		scanLimit = MaxQueryScanBytes
	}
	// start is the position this page resumes from. A page must return a line
	// or a cursor strictly after start, in query order.
	start := cursor{T: from.Add(-time.Nanosecond), S: "~", I: math.MaxInt}
	if q.Descending {
		start = cursor{T: to.Add(time.Nanosecond), S: "", I: -1}
	}
	if cur != nil {
		start = *cur
	}
	pass := func(budget int64) ([]item, *cursor, bool, error) {
		h := &topN{desc: q.Descending}
		var scanned int64
		var unscanned []Segment
		for i, seg := range segs {
			if err := ctx.Err(); err != nil {
				return nil, nil, false, err
			}
			if len(h.items) >= want { // later segments cannot beat what we hold
				worst := h.items[0].k.T
				if (!q.Descending && seg.FirstTime.After(worst)) || (q.Descending && seg.LastTime.Before(worst)) {
					break
				}
			}
			b := budget
			if i == 0 {
				b = math.MaxInt64 // one segment is bounded by SegmentSealBytes
			}
			hit, err := s.scanSegment(seg, from, to, cur, &scanned, b, h, want)
			if err != nil {
				return nil, nil, false, err
			}
			if hit {
				unscanned = segs[i:]
				break
			}
		}
		items := h.items
		sort.Slice(items, func(i, j int) bool {
			if q.Descending {
				return items[i].k.cmp(items[j].k) > 0
			}
			return items[i].k.cmp(items[j].k) < 0
		})
		if len(unscanned) == 0 {
			return items, nil, false, nil
		}
		// Unscanned segments may hold lines on this side of anything we saw
		// from them, so only lines strictly before their earliest start (in
		// query order) are safe to return. The cursor sits just before that
		// start, so the next page resumes exactly there.
		var boundary cursor
		var edge time.Time
		if q.Descending {
			edge = unscanned[0].LastTime
			for _, u := range unscanned {
				if u.LastTime.After(edge) {
					edge = u.LastTime
				}
			}
			boundary = cursor{T: edge.Add(time.Nanosecond), S: "", I: -1}
		} else {
			edge = unscanned[0].FirstTime
			for _, u := range unscanned {
				if u.FirstTime.Before(edge) {
					edge = u.FirstTime
				}
			}
			boundary = cursor{T: edge.Add(-time.Nanosecond), S: "~", I: math.MaxInt}
		}
		kept := items[:0]
		for _, it := range items {
			if (!q.Descending && it.k.T.Before(edge)) || (q.Descending && it.k.T.After(edge)) {
				kept = append(kept, it)
			}
		}
		return kept, &boundary, true, nil
	}
	items, boundary, hit, err := pass(scanLimit)
	if err != nil {
		return res, err
	}
	stuck := func() bool {
		if !hit || len(items) > 0 {
			return false
		}
		c := boundary.cmp(start)
		return (!q.Descending && c <= 0) || (q.Descending && c >= 0)
	}
	if stuck() {
		// Segments that began before the resume point must be read from their
		// start, which the budget cannot cover. Read past it, up to a hard cap.
		if items, boundary, hit, err = pass(2 * MaxQueryScanBytes); err != nil {
			return res, err
		}
		if stuck() {
			return res, errors.New("too many log segments overlap this time range to read within the scan limit; narrow the time range")
		}
	}
	res.ScanLimited = res.ScanLimited || hit
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	for _, it := range items {
		res.Entries = append(res.Entries, it.e)
	}
	switch {
	case more && len(items) > 0:
		raw, _ := json.Marshal(items[len(items)-1].k)
		res.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	case boundary != nil:
		raw, _ := json.Marshal(boundary)
		res.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	case len(items) > 0 && res.ScanLimited:
		raw, _ := json.Marshal(items[len(items)-1].k)
		res.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return res, nil
}

// matching pages through the index. The index has no offset, so each page
// restarts From at the last FirstTime seen: every unseen segment overlaps
// that point. It reports truncated if the page cap or a stuck page was hit.
func (s *Store) matching(ctx context.Context, q ReadQuery, from, to time.Time) ([]Segment, bool, error) {
	seen := map[string]bool{}
	var all []Segment
	f := from
	for page := 0; page < maxIndexPages; page++ {
		got, err := s.Index.Segments(ctx, SegmentQuery{ApplicationID: q.ApplicationID, From: f, To: to,
			PodUIDs: q.PodUIDs, Services: q.Services, Limit: indexPageSize})
		if err != nil {
			return nil, false, err
		}
		fresh := 0
		for _, g := range got {
			if !seen[g.ID] {
				seen[g.ID] = true
				all = append(all, g)
				fresh++
			}
		}
		if len(got) < indexPageSize {
			return all, false, nil
		}
		if fresh == 0 {
			return all, true, nil
		}
		if last := got[len(got)-1].FirstTime; last.After(f) {
			f = last
		}
	}
	return all, true, nil
}

// scanSegment streams one segment into h. It reports true when the scan
// budget ran out. Missing files and unreadable lines are skipped.
func (s *Store) scanSegment(seg Segment, from, to time.Time, cur *cursor, scanned *int64, limit int64, h *topN, want int) (bool, error) {
	p, err := s.abs(seg.Path)
	if err != nil {
		return false, nil
	}
	alt := p + ".gz"
	if strings.HasSuffix(p, ".gz") {
		alt = strings.TrimSuffix(p, ".gz")
	}
	var f *os.File
	for _, c := range []string{p, alt} {
		f, err = os.Open(c)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("could not read an application log file: %w", err)
		}
	}
	if f == nil {
		return false, nil // pruned while we were reading
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(f.Name(), ".gz") {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return false, nil
		}
		defer zr.Close()
		r = zr
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxJSONLine)
	for idx := 0; sc.Scan(); idx++ {
		b := sc.Bytes()
		*scanned += int64(len(b)) + 1
		if *scanned > limit {
			return true, nil
		}
		var l Line
		if json.Unmarshal(b, &l) != nil || l.Time.Before(from) || l.Time.After(to) {
			continue // a partial trailing line or a corrupt line
		}
		k := cursor{T: l.Time, S: seg.ID, I: idx}
		desc := h.desc
		if cur != nil {
			if c := k.cmp(*cur); (!desc && c <= 0) || (desc && c >= 0) {
				continue
			}
		}
		it := item{k: k, e: Entry{Instance: seg.Instance, Line: l}}
		if len(h.items) < want {
			heap.Push(h, it)
		} else if c := k.cmp(h.items[0].k); (!desc && c < 0) || (desc && c > 0) {
			h.items[0] = it
			heap.Fix(h, 0)
		}
	}
	return false, nil
}

// Prune removes segments older than retention, then the oldest segments of
// each application over maxAppBytes, then directories of applications with no
// segments (at most maxOrphanDirs per call). It returns the number of segments
// and directories removed. Segments still being written are left alone.
func (s *Store) Prune(ctx context.Context, retention time.Duration, maxAppBytes int64) (int, error) {
	if s.Index == nil {
		return 0, errors.New("the application log store has no index")
	}
	removed := 0
	before := s.now().Add(-retention)
	for round := 0; round < pruneRounds; round++ {
		segs, err := s.Index.ExpiredSegments(ctx, before, pruneBatch)
		if err != nil {
			return removed, err
		}
		n, err := s.drop(ctx, segs)
		removed += n
		if err != nil {
			return removed, err
		}
		if n == 0 || len(segs) < pruneBatch {
			break
		}
	}
	for round := 0; maxAppBytes > 0 && round < sizeRounds; round++ {
		apps, err := s.Index.ApplicationsOverBytes(ctx, maxAppBytes, overApps)
		if err != nil {
			return removed, err
		}
		progress := false
		for _, app := range apps {
			segs, err := s.Index.OldestSegments(ctx, app, sizeBatch)
			if err != nil {
				return removed, err
			}
			n, err := s.drop(ctx, segs)
			removed += n
			progress = progress || n > 0
			if err != nil {
				return removed, err
			}
		}
		if !progress {
			break
		}
	}
	n, err := s.sweepOrphans(ctx)
	return removed + n, err
}

// drop deletes files, then index rows, for segments that are not open.
func (s *Store) drop(ctx context.Context, segs []Segment) (int, error) {
	s.mu.Lock()
	busy := map[string]bool{}
	for _, a := range s.open {
		busy[a.seg.ID] = true
	}
	s.mu.Unlock()
	var ids []string
	var first error
	for _, seg := range segs {
		if busy[seg.ID] {
			continue
		}
		if p, err := s.abs(seg.Path); err == nil {
			ok := true
			for _, c := range []string{p, p + ".gz", strings.TrimSuffix(p, ".gz")} {
				if err := os.Remove(c); err != nil && !os.IsNotExist(err) {
					ok = false
					if first == nil {
						first = fmt.Errorf("could not delete an old log file: %w", err)
					}
				}
			}
			if !ok {
				continue
			}
			if d := filepath.Dir(p); filepath.Dir(d) != d && d != filepath.Clean(s.root()) {
				os.Remove(d) // only succeeds when the day directory is empty
			}
		}
		ids = append(ids, seg.ID)
	}
	if len(ids) > 0 {
		if err := s.Index.DeleteSegments(ctx, ids); err != nil {
			return 0, err
		}
	}
	return len(ids), first
}

func (s *Store) sweepOrphans(ctx context.Context) (int, error) {
	root := s.root()
	d, err := os.Open(root)
	if err != nil {
		return 0, nil // nothing stored yet
	}
	ents, err := d.ReadDir(maxRootEntries)
	d.Close()
	if err != nil && err != io.EOF {
		return 0, nil
	}
	var cands []string
	for _, e := range ents {
		if len(cands) >= maxOrphanDirs {
			break
		}
		if !e.IsDir() || validName("application ID", e.Name()) != nil {
			continue
		}
		segs, err := s.Index.OldestSegments(ctx, e.Name(), 1)
		if err != nil {
			return 0, err
		}
		if len(segs) == 0 {
			cands = append(cands, e.Name())
		}
	}
	removed := 0
	for _, app := range cands {
		n, err := s.removeOrphan(ctx, app)
		removed += n
		if err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// removeOrphan re-checks under the write lock, so a segment that Append just
// created for this application is never deleted.
func (s *Store) removeOrphan(ctx context.Context, app string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.open {
		if a.seg.Instance.ApplicationID == app {
			return 0, nil
		}
	}
	segs, err := s.Index.OldestSegments(ctx, app, 1)
	if err != nil || len(segs) > 0 {
		return 0, err
	}
	if err := os.RemoveAll(filepath.Join(s.root(), app)); err != nil {
		return 0, fmt.Errorf("could not delete the logs of a removed application: %w", err)
	}
	return 1, nil
}
