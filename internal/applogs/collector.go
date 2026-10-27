package applogs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Pod, Container and Settings mirror the cluster and store types. They are
// defined here because applogs cannot import cluster or store (both import
// applogs); the API package adapts between them.
type Container struct {
	Name                string
	Restart             int
	Running, Terminated bool
	Reason              string
	ExitCode            int32
	PreviousRestart     int
	PreviousReason      string
	PreviousExitCode    int32
}

type Pod struct {
	ApplicationID, Service, Namespace, Name, UID, Node, Reason string
	Revision                                                   int64
	StartedAt                                                  time.Time
	Terminating                                                bool
	Containers                                                 []Container
}

type Settings struct {
	Enabled       bool
	RetentionDays int
	MaxMiBPerApp  int
}

// Masker masks one pod's lines. InPrivateKey and ResumePrivateKey let the
// collector carry a private-key block across a restart or a rebuild.
type Masker interface {
	Line(string) string
	InPrivateKey() bool
	ResumePrivateKey()
	Added() []string // values learned from ::add-mask:: commands
	Add(string)
}

type Cluster interface {
	AppLogPods(ctx context.Context, limit int) ([]Pod, error)
	PodLogSince(ctx context.Context, namespace, pod, container string, previous bool, since time.Time, limitBytes int64) ([]byte, error)
	AppLogMasker(ctx context.Context, namespace, pod string) (Masker, error)
}

type Records interface {
	ClaimAppLogCollector(ctx context.Context, owner string, lease time.Duration) (bool, error)
	UpsertLogPod(ctx context.Context, applicationID string, p PodRecord) error
	EndOpenLogPods(ctx context.Context, applicationID string, seen []string, at time.Time, reason EndReason) error
	AddLogGap(ctx context.Context, podUID string, seconds int, note string) error
	LogCursor(ctx context.Context, key string) (json.RawMessage, bool, error)
	SaveLogCursor(ctx context.Context, key, podUID string, cursor json.RawMessage) error
	DeleteLogCursorsForEndedPods(ctx context.Context, before time.Time) (int64, error)
	PruneLogPods(ctx context.Context, before time.Time) (int64, error)
	Settings(ctx context.Context) (Settings, error)
	ReleaseAppLogCollector(ctx context.Context, owner string) error
	OpenLogPodCursorTimes(ctx context.Context, applicationID string, notIn []string) (map[string]time.Time, error)
}

const (
	leaseFor          = 30 * time.Second
	instanceBytes     = 4 << 20
	tickBytes         = 16 << 20
	maxReaders        = 4
	maxPods           = 2000
	readChunk         = 1 << 20 // bytes per read call; bounds memory per reader
	maxMaskLine       = 64 << 10
	maskRefresh       = 10 * time.Minute
	lineWithheld      = "[line withheld: longer than 64 KiB]"
	housekeepEvery    = 10 * time.Minute
	settingsEvery     = time.Minute
	NoteFileGap       = "Log file rotated before Hakopod read it."
	NoteRestartGap    = "Container restarted more than once between collections; earlier runs were not collected."
	NoteRestartingGap = "Hakopod was restarting; output in this period may be missing."
	NoteBurstGap      = "Burst of output within one second exceeded what Hakopod reads; some lines were skipped."
	NoteAPIGap        = "Pod was removed before its last output was collected."
)

// Collector copies pod logs into the Store while pods are alive. Only the
// process holding the database lease collects.
type Collector struct {
	Cluster Cluster
	Records Records
	Store   *Store
	Tailer  FileTailer
	Owner   string
	Now     func() time.Time

	// Overrides for tests; zero means the package limits.
	TickBytes, InstanceBytes int64

	mu          sync.Mutex
	masks       map[string]maskEntry
	tracked     map[string]*trackedPod
	swept       map[string]bool
	rot         int
	set         Settings
	setAt       time.Time
	housekeptAt time.Time
	permWarned  bool
}

type maskEntry struct {
	m  Masker
	at time.Time
}

type trackedPod struct {
	fails      map[string]int // failed reads of a previous run, by cursor key
	raw        map[string]rawGroup
	pod        Pod
	src        Source
	lastRead   time.Time
	drained    bool // read while the pod was already terminating
	maskFailed bool
	fileDenied bool // /var/log/pods is not readable for this pod; use the API
	upsertAt   time.Time
	rec        PodRecord
	last       map[string]time.Time // last stored line time per cursor key
}

type spend struct {
	mu   sync.Mutex
	left int64
}

func (b *spend) take(n int64) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.left {
		n = b.left
	}
	if n < 0 {
		n = 0
	}
	b.left -= n
	return n
}

func (b *spend) remaining() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.left
}

func (b *spend) give(n int64) {
	b.mu.Lock()
	b.left += n
	b.mu.Unlock()
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Collector) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.masks, c.tracked, c.swept = map[string]maskEntry{}, map[string]*trackedPod{}, map[string]bool{}
}

// Tick runs one pass. Without the lease it does nothing.
func (c *Collector) Tick(ctx context.Context) error {
	ok, err := c.Records.ClaimAppLogCollector(ctx, c.Owner, leaseFor)
	if err != nil {
		return err
	}
	if !ok {
		if c.tracked != nil {
			c.reset()
		}
		return nil
	}
	if c.tracked == nil {
		c.reset()
	}
	set := c.loadSettings(ctx)
	c.housekeep(ctx, set)
	if !set.Enabled {
		return nil
	}
	pods, err := c.Cluster.AppLogPods(ctx, maxPods)
	if err != nil {
		return err
	}
	c.collect(ctx, pods)
	c.finishAbsent(ctx, pods)
	return c.Store.SealIdle(ctx)
}

// Run ticks every PollInterval until ctx ends, then seals idle segments.
func (c *Collector) Run(ctx context.Context) {
	t := time.NewTicker(PollInterval)
	defer t.Stop()
	lastErr := ""
	for {
		if err := c.Tick(ctx); err != nil && ctx.Err() == nil && err.Error() != lastErr {
			lastErr = err.Error()
			slog.Warn("application log collection hit a problem", "error", lastErr)
		}
		select {
		case <-ctx.Done():
			end, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			_ = c.Store.SealIdle(end)
			_ = c.Records.ReleaseAppLogCollector(end, c.Owner) // lets another process collect at once
			return
		case <-t.C:
		}
	}
}

func (c *Collector) loadSettings(ctx context.Context) Settings {
	if !c.setAt.IsZero() && c.now().Sub(c.setAt) < settingsEvery {
		return c.set
	}
	if s, err := c.Records.Settings(ctx); err == nil {
		c.set = s
	} else if c.setAt.IsZero() {
		c.set = Settings{Enabled: true, RetentionDays: 7, MaxMiBPerApp: 512}
	}
	c.setAt = c.now()
	return c.set
}

func (c *Collector) housekeep(ctx context.Context, s Settings) {
	if !c.housekeptAt.IsZero() && c.now().Sub(c.housekeptAt) < housekeepEvery {
		return
	}
	c.housekeptAt = c.now()
	keep := time.Duration(s.RetentionDays) * 24 * time.Hour
	before := c.now().Add(-keep)
	if _, err := c.Store.Prune(ctx, keep, int64(s.MaxMiBPerApp)<<20); err != nil {
		slog.Warn("application log pruning failed", "error", err.Error())
	}
	if _, err := c.Records.PruneLogPods(ctx, before); err != nil {
		slog.Warn("application pod history pruning failed", "error", err.Error())
	}
	if _, err := c.Records.DeleteLogCursorsForEndedPods(ctx, before); err != nil {
		slog.Warn("application log cursor pruning failed", "error", err.Error())
	}
}

// order rotates pods across ticks for fairness and puts terminating pods first.
func (c *Collector) order(pods []Pod) []Pod {
	sort.Slice(pods, func(i, j int) bool { return pods[i].UID < pods[j].UID })
	out := make([]Pod, 0, len(pods))
	if len(pods) > 0 {
		off := c.rot % len(pods)
		out = append(append(out, pods[off:]...), pods[:off]...)
	}
	c.rot++
	sort.SliceStable(out, func(i, j int) bool { return out[i].Terminating && !out[j].Terminating })
	return out
}

func (c *Collector) collect(ctx context.Context, pods []Pod) {
	total := c.TickBytes
	if total <= 0 {
		total = tickBytes
	}
	b := &spend{left: total}
	var dying, calm []Pod
	for _, p := range c.order(pods) {
		if p.Terminating {
			dying = append(dying, p)
		} else {
			calm = append(calm, p)
		}
	}
	// Terminating pods claim the budget first, so their last output wins over
	// the fair share; the rest then split what is left.
	c.runPods(ctx, dying, b, c.instCap())
	n := 0
	for _, p := range calm {
		n += len(p.Containers)
	}
	share := b.remaining() / int64(max(n, 1))
	if share < 1 {
		share = 1
	}
	if share > c.instCap() {
		share = c.instCap()
	}
	c.runPods(ctx, calm, b, share)
}

func (c *Collector) runPods(ctx context.Context, pods []Pod, b *spend, share int64) {
	sem := make(chan struct{}, maxReaders)
	var wg sync.WaitGroup
	for _, p := range pods {
		tr := c.track(p)
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			c.collectPod(ctx, tr, b, share)
		}()
	}
	wg.Wait()
}

func (c *Collector) track(p Pod) *trackedPod {
	c.mu.Lock()
	defer c.mu.Unlock()
	tr := c.tracked[p.UID]
	if tr == nil {
		tr = &trackedPod{last: map[string]time.Time{}, fails: map[string]int{}, raw: map[string]rawGroup{}}
		c.tracked[p.UID] = tr
	}
	tr.pod = p
	return tr
}

func (c *Collector) instCap() int64 {
	if c.InstanceBytes > 0 {
		return c.InstanceBytes
	}
	return instanceBytes
}

// maskerFor caches one masker per pod UID and rebuilds it every maskRefresh so
// rotated secrets are picked up. ponytail: a rebuild or a process restart in the
// middle of a private-key block loses the masker's block state, so the rest of
// that block could be stored unmasked.
func (c *Collector) maskerFor(ctx context.Context, p Pod) (Masker, error) {
	c.mu.Lock()
	e, ok := c.masks[p.UID]
	c.mu.Unlock()
	// Never rebuild inside a private-key block: the new masker would not know
	// it is in one. Try again on a later tick.
	if ok && (c.now().Sub(e.at) < maskRefresh || e.m.InPrivateKey()) {
		return e.m, nil
	}
	f, err := c.Cluster.AppLogMasker(ctx, p.Namespace, p.Name)
	if err != nil {
		if ok {
			return e.m, nil // keep the working masker; retry on the next tick
		}
		return nil, err
	}
	if ok { // keep what the old masker learned from the log itself
		for _, v := range e.m.Added() {
			f.Add(v)
		}
	}
	c.mu.Lock()
	c.masks[p.UID] = maskEntry{f, c.now()}
	c.mu.Unlock()
	return f, nil
}

func (c *Collector) collectPod(ctx context.Context, tr *trackedPod, b *spend, share int64) {
	p := tr.pod
	tr.src = SourceAPI
	if !tr.fileDenied && c.Tailer.Exists(p.Namespace, p.Name, p.UID) {
		tr.src = SourceFile
	}
	c.upsert(ctx, tr)
	// Never store a line we could not mask: skip the pod and retry next tick.
	mask, err := c.maskerFor(ctx, p)
	if err != nil {
		tr.maskFailed = true
		slog.Warn("application log masking is not ready, skipping pod this tick", "pod", p.Name)
		return
	}
	tr.maskFailed = false
	limit := min(share, c.instCap())
	allEOF, allDone := true, len(p.Containers) > 0
	for _, k := range p.Containers {
		eof, err := c.collectContainer(ctx, tr, k, mask, b, limit)
		if err != nil {
			slog.Warn("application log read failed", "pod", p.Name, "container", k.Name, "error", err.Error())
		}
		allEOF = allEOF && eof && err == nil
		allDone = allDone && k.Terminated
	}
	// drained: every container has stopped and everything it wrote was read.
	tr.drained = allDone && allEOF
}

func (c *Collector) upsert(ctx context.Context, tr *trackedPod) {
	p := tr.pod
	rec := PodRecord{PodUID: p.UID, PodName: p.Name, Service: p.Service, Revision: p.Revision, Node: p.Node, Source: tr.src, StartedAt: p.StartedAt.UTC()}
	for _, k := range p.Containers {
		if k.Restart > rec.Restarts {
			rec.Restarts = k.Restart
		}
	}
	if p.Reason == "Evicted" {
		at := c.now().UTC()
		rec.EndedAt, rec.EndReason = &at, EndEvicted
	}
	same := tr.rec.PodUID != "" && rec.Revision == tr.rec.Revision && rec.Node == tr.rec.Node && rec.Source == tr.rec.Source && rec.Restarts == tr.rec.Restarts && (rec.EndedAt == nil) == (tr.rec.EndedAt == nil)
	if same && c.now().Sub(tr.upsertAt) < time.Minute {
		return
	}
	if err := c.Records.UpsertLogPod(ctx, p.ApplicationID, rec); err == nil {
		tr.rec, tr.upsertAt = rec, c.now()
	}
}

// collectContainer reads one container. After a restart the previous run is
// read to its end first; the new run's cursor is saved only once that is done,
// so a budget stop keeps both runs pending for the next tick.
func (c *Collector) collectContainer(ctx context.Context, tr *trackedPod, k Container, mask Masker, b *spend, limit int64) (bool, error) {
	p := tr.pod
	if k.Restart > 0 {
		_, found, err := c.Records.LogCursor(ctx, cursorKey(p.UID, k.Name, k.Restart))
		if err != nil {
			return false, err
		}
		if !found {
			done, err := c.drainPrevious(ctx, tr, k, mask, b, limit)
			if err != nil || !done {
				return false, err
			}
			if err := c.Records.SaveLogCursor(ctx, cursorKey(p.UID, k.Name, k.Restart), p.UID, emptyCursor(tr.src)); err != nil {
				return false, err
			}
		}
	}
	return c.readInstance(ctx, tr, k, k.Restart, p.Terminating, mask, b, limit)
}

// drainPrevious reports whether the previous run is finished (or can never be
// read, in which case a gap is recorded).
func (c *Collector) drainPrevious(ctx context.Context, tr *trackedPod, k Container, mask Masker, b *spend, limit int64) (bool, error) {
	p, prev := tr.pod, k.Restart-1
	key := cursorKey(p.UID, k.Name, prev)
	_, prevKnown, err := c.Records.LogCursor(ctx, key)
	if err != nil {
		return false, err
	}
	skipped := 0
	if k.Restart > 1 && !prevKnown {
		skipped = c.sinceLastLine(tr)
	}
	eof, err := c.readInstance(ctx, tr, k, prev, true, mask, b, limit)
	if err != nil {
		tr.fails[key]++
		if !errors.Is(err, fs.ErrNotExist) && tr.fails[key] < 3 {
			return false, err
		}
		// The previous run can no longer be read.
		c.addGap(ctx, tr, c.sinceLine(tr, key), NoteFileGap)
		eof = true
	}
	if eof && skipped > 0 {
		_ = c.Records.AddLogGap(ctx, p.UID, skipped, NoteRestartGap)
	}
	return eof, nil
}

func (c *Collector) lastLine(tr *trackedPod, key string) time.Time {
	if t := tr.last[key]; !t.IsZero() {
		return t
	}
	return tr.pod.StartedAt
}

func (c *Collector) sinceLine(tr *trackedPod, key string) time.Duration {
	return c.now().Sub(c.lastLine(tr, key))
}

// sinceLastLine is seconds since the newest line collected for this pod, 1..3600.
func (c *Collector) sinceLastLine(tr *trackedPod) int {
	from := tr.pod.StartedAt
	for _, t := range tr.last {
		if t.After(from) {
			from = t
		}
	}
	return int(min(max(c.now().Sub(from), time.Second), time.Hour) / time.Second)
}

func (c *Collector) addGap(ctx context.Context, tr *trackedPod, d time.Duration, note string) {
	secs := int(min(max(d, time.Second), 24*time.Hour) / time.Second)
	_ = c.Records.AddLogGap(ctx, tr.pod.UID, secs, note)
}

func cursorKey(uid, container string, restart int) string {
	return fmt.Sprintf("%s|%s|%d", uid, container, restart)
}

// apiCursor remembers the last line time and how many lines at exactly that
// time were stored (Seen), with a hash of their masked text, so a re-read of
// the boundary second neither loses nor repeats lines.
type apiCursor struct {
	Since time.Time `json:"since"`
	Seen  int       `json:"seen,omitempty"`
	Hash  string    `json:"hash,omitempty"`
}

// chain hashes messages in order, starting from prev, so a prefix hash can be
// extended without seeing the prefix again.
func chain(prev string, msgs ...string) string {
	h := prev
	for _, m := range msgs {
		sum := sha256.Sum256([]byte(h + "\n" + m))
		h = hex.EncodeToString(sum[:])
	}
	return h
}

// rawGroup is the in-memory, never persisted, hash of the unmasked lines at the
// cursor's boundary time. It lets a re-read be recognised before masking.
type rawGroup struct {
	Since time.Time
	Seen  int
	Hash  string
}

func emptyCursor(s Source) json.RawMessage {
	if s == SourceAPI {
		return json.RawMessage(`{"since":"0001-01-01T00:00:00Z"}`)
	}
	return json.RawMessage(`{}`)
}

// maskLine masks the complete line, then cuts it to MaxLineBytes. Lines longer
// than the masker accepts are replaced and never passed to it, because an
// oversized line would permanently disable the masker.
func maskLine(mask func(string) string, l Line) Line {
	l.Time = l.Time.UTC()
	if len(l.Message) > maxMaskLine {
		l.Message, l.Truncated = lineWithheld, false
		return l
	}
	l.Message = mask(l.Message)
	if len(l.Message) > MaxLineBytes {
		cut := MaxLineBytes
		for cut > 0 && !utf8.RuneStart(l.Message[cut]) {
			cut--
		}
		l.Message, l.Truncated = l.Message[:cut], true
	}
	return l
}

// readInstance reads one container run in chunks. drain reads on to the end
// within the instance cap; otherwise it stops after limit bytes. It reports
// whether the run was read to the end of its data.
func (c *Collector) readInstance(ctx context.Context, tr *trackedPod, k Container, restart int, drain bool, mask Masker, b *spend, limit int64) (bool, error) {
	p := tr.pod
	key := cursorKey(p.UID, k.Name, restart)
	inst := Instance{ApplicationID: p.ApplicationID, Service: p.Service, PodUID: p.UID, PodName: p.Name, Container: k.Name, Restart: restart, Revision: p.Revision, Node: p.Node}
	cur, _, err := c.Records.LogCursor(ctx, key)
	if err != nil {
		return false, err
	}
	if len(cur) == 0 {
		cur = emptyCursor(tr.src)
	}
	if pkSet(cur) && !mask.InPrivateKey() {
		mask.ResumePrivateKey() // the process restarted inside a private-key block
	}
	previous := restart < k.Restart
	left := limit
	if drain {
		left = c.instCap()
	}
	for {
		want := min(readChunk, left)
		if drain { // beyond the fair share, but never more than half of what is left
			want = min(want, max(b.remaining()/2, 1))
		}
		got := b.take(want)
		if got == 0 {
			return false, nil
		}
		lines, next, gap, eof, spent, err := c.step(ctx, tr, k, restart, previous, cur, got, mask, b)
		if spent > got {
			b.take(spent - got) // a started P sequence may be read past the budget
		}
		b.give(max(got-spent, 0))
		left -= spent
		if errors.Is(err, fs.ErrPermission) && tr.src == SourceFile {
			c.fileDenied(tr)
			cur = emptyCursor(SourceAPI)
			continue
		}
		if err != nil {
			return false, err
		}
		if gap {
			c.fileGap(ctx, tr, key, lines)
		}
		if len(lines) > 0 {
			if err := c.Store.Append(ctx, inst, lines); err != nil {
				return false, err
			}
			tr.last[key] = lines[len(lines)-1].Time
			tr.lastRead = c.now()
		}
		next = withPK(next, mask.InPrivateKey())
		same := bytes.Equal(next, cur)
		// The cursor is saved only after Append succeeded.
		if !same {
			if err := c.Records.SaveLogCursor(ctx, key, p.UID, next); err != nil {
				return false, err
			}
			cur = next
		}
		if eof {
			return true, nil
		}
		if len(lines) == 0 && same { // caught up for now; not the end of the data
			return false, nil
		}
		if spent == 0 { // no progress: the budget is smaller than one record
			return false, nil
		}
		if left <= 0 {
			return false, nil
		}
	}
}

// pkSet and withPK keep one boolean, never text, in the cursor JSON: whether
// the instance's output ended inside a private-key block.
func pkSet(cur json.RawMessage) bool {
	var f struct {
		PK bool `json:"pk"`
	}
	return json.Unmarshal(cur, &f) == nil && f.PK
}

func withPK(cur json.RawMessage, pk bool) json.RawMessage {
	m := map[string]json.RawMessage{}
	if json.Unmarshal(cur, &m) != nil {
		return cur
	}
	if pk {
		m["pk"] = json.RawMessage("true")
	} else {
		delete(m, "pk")
	}
	out, err := json.Marshal(m)
	if err != nil {
		return cur
	}
	return out
}

// fileDenied moves a pod to the API source for the rest of its life.
func (c *Collector) fileDenied(tr *trackedPod) {
	tr.fileDenied, tr.src = true, SourceAPI
	c.mu.Lock()
	warn := !c.permWarned
	c.permWarned = true
	c.mu.Unlock()
	if warn {
		slog.Warn("Hakopod cannot read /var/log/pods; using the Kubernetes API for logs. Run installer.sh --upgrade to grant read access.")
	}
}

// fileGap records a rotation gap, with or without lines after it.
func (c *Collector) fileGap(ctx context.Context, tr *trackedPod, key string, lines []Line) {
	to := c.now()
	if len(lines) > 0 {
		to = lines[0].Time
	}
	c.addGap(ctx, tr, to.Sub(c.lastLine(tr, key)), NoteFileGap)
}

// step does one bounded read and returns masked lines, the next cursor, whether
// the tailer reported a gap, whether the end of data was reached, and the bytes
// to charge against the budget (what the tailer consumed, or the API output).
func (c *Collector) step(ctx context.Context, tr *trackedPod, k Container, restart int, previous bool, cur json.RawMessage, max int64, mask Masker, b *spend) (lines []Line, next json.RawMessage, gap, eof bool, spent int64, err error) {
	p := tr.pod
	if tr.src == SourceFile {
		var fc FileCursor
		if err = json.Unmarshal(cur, &fc); err != nil {
			return
		}
		// A stopped container or a finished run has no more output coming, so an
		// open P record at its end is emitted as a line (ReadFinal).
		read := c.Tailer.ReadN
		if previous || k.Terminated {
			read = c.Tailer.ReadFinal
		}
		r, rerr := read(p.Namespace, p.Name, p.UID, k.Name, restart, fc, max)
		if rerr != nil {
			err = rerr
			return
		}
		lines = r.Lines
		for i := range lines {
			lines[i] = maskLine(mask.Line, lines[i])
		}
		next, err = json.Marshal(r.Next)
		return lines, next, r.Gap, r.AtEnd, r.Consumed, err
	}
	var ac apiCursor
	if err = json.Unmarshal(cur, &ac); err != nil {
		return
	}
	key := cursorKey(p.UID, k.Name, restart)
	fresh := func() func(string) string {
		m, err := c.Cluster.AppLogMasker(ctx, p.Namespace, p.Name)
		if err != nil {
			return nil
		}
		return m.Line
	}
	out, full, err := c.apiRead(ctx, p, k, previous, ac.Since, max)
	if err != nil {
		return
	}
	lines, nc, rg := parseAPI(out, ac, tr.raw[key], mask.Line, fresh)
	spent = int64(len(out))
	if len(lines) == 0 && full {
		// No progress and the output filled the limit: SinceTime has second
		// precision, so retry once with the instance cap, paid from the tick budget.
		if cap := c.instCap(); max < cap {
			extra := b.take(cap)
			if extra < cap { // cannot afford the retry: try again next tick
				b.give(extra)
				return nil, cur, false, false, spent, nil
			}
			if out, full, err = c.apiRead(ctx, p, k, previous, ac.Since, cap); err != nil {
				b.give(extra)
				return
			}
			b.give(extra - int64(len(out)))
			lines, nc, rg = parseAPI(out, ac, tr.raw[key], mask.Line, fresh)
		}
		if len(lines) == 0 && full {
			c.addGap(ctx, tr, time.Second, NoteBurstGap)
			nc, rg = apiCursor{Since: ac.Since.UTC().Truncate(time.Second).Add(time.Second)}, rawGroup{}
		}
	}
	tr.raw[key] = rg
	next, err = json.Marshal(nc)
	return lines, next, false, !full, spent, err
}

// apiRead returns whole lines only; a line cut by the byte limit is dropped and
// read next time.
func (c *Collector) apiRead(ctx context.Context, p Pod, k Container, previous bool, since time.Time, max int64) ([]byte, bool, error) {
	out, err := c.Cluster.PodLogSince(ctx, p.Namespace, p.Name, k.Name, previous, since, max)
	if err != nil {
		return nil, false, err
	}
	if int64(len(out)) < max {
		return out, false, nil
	}
	if i := bytes.LastIndexByte(out, '\n'); i >= 0 {
		return out[:i+1], true, nil
	}
	return nil, true, nil
}

type apiLine struct {
	t    time.Time
	msg  string
	over bool
}

// parseAPI reads "<RFC3339Nano> message" lines at or after the cursor. Lines at
// exactly cur.Since are a re-read, and are recognised before masking so stored
// lines never pass through the stateful masker twice: first by the in-memory raw
// hash, else (after a process restart) by the persisted masked hash over a
// throwaway masker. On any mismatch every line is kept (a duplicate is
// acceptable, a lost line is not). Each line is assembled (up to 64 KiB),
// masked as a whole, then cut.
func parseAPI(out []byte, cur apiCursor, rg rawGroup, mask func(string) string, fresh func() func(string) string) ([]Line, apiCursor, rawGroup) {
	var raw []apiLine
	for _, rec := range bytes.Split(out, []byte{'\n'}) {
		rec = bytes.TrimSuffix(rec, []byte{'\r'})
		if len(rec) == 0 {
			continue
		}
		ts, msg, _ := strings.Cut(string(rec), " ")
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil { // untimestamped continuation of the previous line
			if n := len(raw); n > 0 && !raw[n-1].over {
				if len(raw[n-1].msg)+len(rec)+1 > maxMaskLine {
					raw[n-1].over = true
				} else {
					raw[n-1].msg += "\n" + string(rec)
				}
			}
			continue
		}
		if t.Before(cur.Since) {
			continue
		}
		raw = append(raw, apiLine{t: t.UTC(), msg: strings.ToValidUTF8(strings.ReplaceAll(msg, "\x00", ""), "\uFFFD"), over: len(msg) > maxMaskLine})
	}
	if len(raw) == 0 {
		return nil, cur, rg
	}
	msgs := func(ls []apiLine) []string {
		m := make([]string, len(ls))
		for i, l := range ls {
			m[i] = l.msg
		}
		return m
	}
	maskOne := func(f func(string) string, r apiLine) Line {
		if r.over {
			return Line{Time: r.t, Stream: "stdout", Message: lineWithheld}
		}
		return maskLine(f, Line{Time: r.t, Stream: "stdout", Message: r.msg})
	}
	atSince := 0
	for atSince < len(raw) && raw[atSince].t.Equal(cur.Since) {
		atSince++
	}
	skip := 0
	switch {
	case rg.Seen > 0 && rg.Seen == cur.Seen && rg.Since.Equal(cur.Since) && atSince >= rg.Seen && chain("", msgs(raw[:rg.Seen])...) == rg.Hash:
		skip = rg.Seen
	case rg.Seen == 0 && cur.Seen > 0 && atSince >= cur.Seen && fresh != nil:
		if f := fresh(); f != nil {
			masked := make([]string, cur.Seen)
			for i := range masked {
				masked[i] = maskOne(f, raw[i]).Message
			}
			if chain("", masked...) == cur.Hash {
				skip = cur.Seen
			}
		}
	}
	lines := make([]Line, 0, len(raw)-skip)
	for _, r := range raw[skip:] {
		lines = append(lines, maskOne(mask, r))
	}
	last := raw[len(raw)-1].t
	tail := len(raw)
	for tail > 0 && raw[tail-1].t.Equal(last) {
		tail--
	}
	next := apiCursor{Since: last}
	if skip > 0 && last.Equal(cur.Since) { // the group keeps growing: extend its hash
		next.Seen, next.Hash = len(raw), chain(cur.Hash, lineMsgs(lines)...)
	} else {
		group := lines[max(tail-skip, 0):]
		next.Seen, next.Hash = len(group), chain("", lineMsgs(group)...)
	}
	return lines, next, rawGroup{Since: last, Seen: len(raw) - tail, Hash: chain("", msgs(raw[tail:])...)}
}

func lineMsgs(ls []Line) []string {
	m := make([]string, len(ls))
	for i, l := range ls {
		m[i] = l.Message
	}
	return m
}

// endReason picks the most specific reason from the pod's last known state.
func endReason(p Pod) EndReason {
	if p.Reason == "Evicted" {
		return EndEvicted
	}
	all, any := len(p.Containers) > 0, false
	for _, k := range p.Containers {
		if !k.Terminated {
			all = false
			continue
		}
		any = true
		if k.Reason == "OOMKilled" {
			return EndOOMKilled
		}
	}
	if p.Terminating || !any { // a rollout's SIGTERM exit is not a failure
		return EndReplaced
	}
	for _, k := range p.Containers {
		if k.Terminated && k.ExitCode != 0 {
			return EndError
		}
	}
	if all {
		return EndCompleted
	}
	return EndReplaced
}

// finishAbsent records pods that were tracked but are no longer listed.
func (c *Collector) finishAbsent(ctx context.Context, pods []Pod) {
	live := map[string]bool{}
	byApp := map[string][]string{}
	for _, p := range pods {
		live[p.UID] = true
		byApp[p.ApplicationID] = append(byApp[p.ApplicationID], p.UID)
	}
	var gone []string
	for uid := range c.tracked {
		if !live[uid] {
			gone = append(gone, uid)
		}
	}
	sort.Strings(gone)
	goneApp := map[string]string{}
	for _, uid := range gone {
		goneApp[uid] = c.tracked[uid].pod.ApplicationID
	}
	now := c.now().UTC()
	for app, uids := range byApp {
		if !c.swept[app] {
			c.swept[app] = true
			c.restartGaps(ctx, app, uids, now)
			_ = c.Records.EndOpenLogPods(ctx, app, uids, now, EndReplaced)
		}
	}
	for _, uid := range gone {
		tr := c.tracked[uid]
		c.finalDrain(ctx, tr)
		c.recordVanishGap(ctx, tr, now)
		app := tr.pod.ApplicationID
		seen := append([]string{}, byApp[app]...)
		for _, other := range gone {
			if o := goneApp[other]; other != uid && o == app {
				seen = append(seen, other)
			}
		}
		if err := c.Records.EndOpenLogPods(ctx, app, seen, now, endReason(tr.pod)); err != nil {
			continue // keep tracking so the next tick tries again
		}
		delete(c.tracked, uid)
		delete(c.masks, uid)
	}
}

// restartGaps notes output that may be missing for pods closed by the first
// sweep after this process started: from their last cursor update until now.
func (c *Collector) restartGaps(ctx context.Context, app string, seen []string, now time.Time) {
	times, err := c.Records.OpenLogPodCursorTimes(ctx, app, seen)
	if err != nil {
		return
	}
	for uid, at := range times {
		secs := int(min(max(now.Sub(at), time.Second), time.Hour) / time.Second)
		_ = c.Records.AddLogGap(ctx, uid, secs, NoteRestartingGap)
	}
}

func (c *Collector) finalDrain(ctx context.Context, tr *trackedPod) {
	mask := c.masks[tr.pod.UID].m
	if tr.src != SourceFile || mask == nil || !c.Tailer.Exists(tr.pod.Namespace, tr.pod.Name, tr.pod.UID) {
		return
	}
	b := &spend{left: c.instCap()}
	for _, k := range tr.pod.Containers {
		_, _ = c.readInstance(ctx, tr, k, k.Restart, true, mask, b, c.instCap())
	}
}

func (c *Collector) recordVanishGap(ctx context.Context, tr *trackedPod, now time.Time) {
	switch {
	case tr.maskFailed || c.masks[tr.pod.UID].m == nil:
		from := tr.lastRead
		if from.IsZero() {
			from = tr.pod.StartedAt
		}
		if secs := int(now.Sub(from).Seconds()); secs > 0 {
			_ = c.Records.AddLogGap(ctx, tr.pod.UID, secs, NoteAPIGap)
		}
	case tr.src == SourceAPI && !tr.drained:
		_ = c.Records.AddLogGap(ctx, tr.pod.UID, int(PollInterval/time.Second), NoteAPIGap)
	}
}
