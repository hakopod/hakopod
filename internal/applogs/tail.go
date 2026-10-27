package applogs

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hakopod/hakopod/internal/logmask"
)

const (
	defaultPodLogRoot = "/var/log/pods"
	defaultReadBytes  = 1 << 20
	// recordCap bounds how much of one on-disk record is kept in memory.
	// It leaves room for the timestamp, stream and tag in front of a
	// message of logmask.MaxLineBytes.
	recordCap = logmask.MaxLineBytes + 256
)

// withheldMessage replaces a line longer than logmask.MaxLineBytes. Such a
// line cannot be masked safely, so none of it is kept.
const withheldMessage = "[line withheld: longer than 64 KiB]"

// unknownStartMessage replaces a line whose first part sat in a file that is
// already gone, so the line cannot be masked as a whole.
const unknownStartMessage = "[line withheld: start of line was not collected]"

// malformedRecords counts records skipped because they were not valid CRI.
var malformedRecords atomic.Int64

// MalformedRecords returns how many invalid records every tailer has skipped
// since the process started. Callers can diff it around a Read.
func MalformedRecords() int64 { return malformedRecords.Load() }

// ParseCRI parses one containerd log record:
// "<RFC3339Nano time> <stdout|stderr> <P|F>[:flag...] <message>". A trailing
// newline is removed. partial is true for a P record.
func ParseCRI(record []byte) (t time.Time, stream string, partial bool, msg []byte, err error) {
	record = bytes.TrimSuffix(record, []byte("\n"))
	ts, rest, ok := bytes.Cut(record, []byte(" "))
	if !ok {
		return t, "", false, nil, errors.New("cri record has no stream")
	}
	if t, err = time.Parse(time.RFC3339Nano, string(ts)); err != nil {
		return t, "", false, nil, fmt.Errorf("cri record has a bad time: %w", err)
	}
	st, rest, ok := bytes.Cut(rest, []byte(" "))
	if !ok || (string(st) != "stdout" && string(st) != "stderr") {
		return t, "", false, nil, errors.New("cri record has a bad stream")
	}
	tag, msg, _ := bytes.Cut(rest, []byte(" "))
	flag, _, _ := bytes.Cut(tag, []byte(":"))
	switch string(flag) {
	case "P":
		partial = true
	case "F":
	default:
		return t, "", false, nil, errors.New("cri record has a bad tag")
	}
	return t, string(st), partial, msg, nil
}

// FileCursor says where a tailer stopped. Inode identifies the file across
// kubelet renames; File is only the name it had when last read. It never
// holds message bytes: when a read stops inside an unfinished P sequence,
// Offset points back at the start of that sequence so it is re-read whole.
type FileCursor struct {
	File   string `json:"file"`
	Inode  uint64 `json:"inode"`
	Offset int64  `json:"offset"`
	// Skip is set while the rest of an over-long P sequence is being
	// discarded. The placeholder line was already returned.
	Skip bool `json:"skip,omitempty"`
}

// FileTailer reads kubelet container log files.
type FileTailer struct {
	Root string // defaults to HAKOPOD_POD_LOG_ROOT, then /var/log/pods
}

func (t FileTailer) root() string {
	if t.Root != "" {
		return t.Root
	}
	if v := os.Getenv("HAKOPOD_POD_LOG_ROOT"); v != "" {
		return v
	}
	return defaultPodLogRoot
}

// PodDir returns the kubelet log directory of one pod.
func (t FileTailer) PodDir(namespace, pod, uid string) string {
	return filepath.Join(t.root(), namespace+"_"+pod+"_"+uid)
}

// Exists reports whether the pod's log directory is on this host.
func (t FileTailer) Exists(namespace, pod, uid string) bool {
	if !safeElem(namespace) || !safeElem(pod) || !safeElem(uid) {
		return false
	}
	fi, err := os.Stat(t.PodDir(namespace, pod, uid))
	return err == nil && fi.IsDir()
}

func safeElem(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}

// assembler joins P records into lines within one Read call.
type assembler struct {
	lines  []Line
	buf    []byte
	open   bool // a P record is waiting for its F
	skip   bool // discarding the rest of an over-long sequence
	first  time.Time
	stream string
	// uncertain means the next line to be completed may continue a line
	// whose start was in a file that is gone, so it is withheld.
	uncertain bool
}

func (a *assembler) emit(msg string, trunc bool) {
	if a.uncertain {
		msg, trunc, a.uncertain = unknownStartMessage, true, false
	}
	a.lines = append(a.lines, Line{Time: a.first, Stream: a.stream, Message: msg, Truncated: trunc})
	a.buf, a.open, a.first = nil, false, time.Time{}
}

func (a *assembler) add(t time.Time, stream string, partial bool, msg []byte) {
	if a.skip {
		a.skip = partial
		return
	}
	if !a.open {
		a.first, a.stream = t, stream
	}
	if len(a.buf)+len(msg) > logmask.MaxLineBytes {
		a.emit(withheldMessage, true)
		a.skip = partial
		return
	}
	a.buf = append(a.buf, msg...)
	if partial {
		a.open = true
		return
	}
	a.emit(cleanMessage(a.buf), false)
}

func cleanMessage(b []byte) string {
	b = bytes.ReplaceAll(b, []byte{0}, nil)
	b = bytes.TrimSuffix(b, []byte("\r"))
	return strings.ToValidUTF8(string(b), "\uFFFD")
}

// readRecord returns one record (kept up to recordCap bytes), the bytes it
// occupies on disk, and whether it ended with a newline.
func readRecord(r *bufio.Reader) (rec []byte, n int64, complete bool, err error) {
	for {
		chunk, e := r.ReadSlice('\n')
		n += int64(len(chunk))
		if room := recordCap - len(rec); room > 0 {
			if len(chunk) < room {
				room = len(chunk)
			}
			rec = append(rec, chunk[:room]...)
		}
		switch e {
		case nil:
			return rec, n, true, nil
		case bufio.ErrBufferFull:
		case io.EOF:
			return rec, n, false, nil
		default:
			return rec, n, false, e
		}
	}
}

// TailResult is what ReadN returns. It is not called ReadResult because the segment store already uses that name.
type TailResult struct {
	Lines []Line
	Next  FileCursor
	// Gap is true when data was lost for certain: the cursor's file was gone
	// as plain text, or it was truncated.
	Gap bool
	// AtEnd is true only when everything readable now was read: the end of
	// the live file was reached with no P sequence left open and the byte
	// budget was not what stopped the read.
	AtEnd bool
	// Consumed is the number of raw file bytes read, including skipped,
	// withheld and malformed records. A sequence that was rewound is counted
	// each time it is read.
	Consumed int64
}

// Read is ReadN without the end-of-data information.
func (t FileTailer) Read(namespace, pod, uid, container string, restart int, cur FileCursor, maxBytes int64) (lines []Line, next FileCursor, gap bool, err error) {
	r, err := t.ReadN(namespace, pod, uid, container, restart, cur, maxBytes)
	return r.Lines, r.Next, r.Gap, err
}

// ReadN returns the complete lines after cur and the cursor to resume from.
// It reads about maxBytes of file data; a P sequence that has started is read
// to its F record even past that budget (it is at most 64 KiB, after which it
// is withheld), so a sequence larger than maxBytes cannot stall the cursor.
// A P sequence is carried across files, so a line that kubelet rotation split
// between files is returned whole. A sequence still unfinished when the chain
// ends is left for the next call by moving the cursor back to where it began,
// by inode, so it stays valid after rotation. When the first line read may
// continue a line from a file that is already gone (first read after a
// rotation, or the gap case) it is withheld rather than returned partial. Lines are returned
// whole up to logmask.MaxLineBytes; longer ones become a withheld placeholder.
// gap is true when data was lost for certain: the cursor's file was gone as
// plain text, or it was truncated.
func (t FileTailer) ReadN(namespace, pod, uid, container string, restart int, cur FileCursor, maxBytes int64) (TailResult, error) {
	return t.readN(namespace, pod, uid, container, restart, cur, maxBytes, false)
}

// ReadFinal is ReadN for a container that has stopped writing (terminated,
// or a previous run being drained). The end of the live file is final: an
// unterminated last record is consumed, an open P sequence there is emitted
// as a complete line because it will never get its F record, and AtEnd is
// true once the end is reached.
func (t FileTailer) ReadFinal(namespace, pod, uid, container string, restart int, cur FileCursor, maxBytes int64) (TailResult, error) {
	return t.readN(namespace, pod, uid, container, restart, cur, maxBytes, true)
}

func (t FileTailer) readN(namespace, pod, uid, container string, restart int, cur FileCursor, maxBytes int64, final bool) (res TailResult, err error) {
	res.Next = cur
	if !safeElem(namespace) || !safeElem(pod) || !safeElem(uid) || !safeElem(container) || restart < 0 {
		return res, errors.New("invalid pod log path")
	}
	if maxBytes <= 0 {
		maxBytes = defaultReadBytes
	}
	dir := filepath.Join(t.PodDir(namespace, pod, uid), container)
	liveName := fmt.Sprintf("%d.log", restart)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			res.AtEnd = true
			return res, nil
		}
		return res, err
	}
	var rotated []string // plain rotated files, oldest first (timestamp suffix sorts)
	siblings := false    // any rotated file, plain or gzipped
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, liveName+".") {
			siblings = true
			if !strings.HasSuffix(n, ".gz") {
				rotated = append(rotated, n)
			}
		}
	}
	sort.Strings(rotated)

	a := &assembler{}
	var chain []string
	var start int64
	liveFi, liveErr := os.Stat(filepath.Join(dir, liveName))
	switch {
	case cur.Inode == 0:
		chain = []string{liveName}
		a.uncertain = siblings // the live file may begin mid-line
	case liveErr == nil && inodeOf(liveFi) == cur.Inode:
		chain, start, a.skip = []string{liveName}, cur.Offset, cur.Skip
	default:
		idx := -1
		for i, n := range rotated {
			if fi, e := os.Stat(filepath.Join(dir, n)); e == nil && inodeOf(fi) == cur.Inode {
				idx = i
				break
			}
		}
		if idx >= 0 {
			chain, start, a.skip = rotated[idx:], cur.Offset, cur.Skip
		} else {
			res.Gap, chain = true, rotated
			a.uncertain = true
		}
		if liveErr == nil {
			chain = append(append([]string(nil), chain...), liveName)
		}
	}

	var seq FileCursor // where the open P sequence began
	finish := func() {
		res.Lines = a.lines
		if a.open {
			res.Next = seq // re-read the whole sequence next time
		}
	}
	budget := maxBytes
	for i, name := range chain {
		off := int64(0)
		if i == 0 {
			off = start
		}
		f, e := os.Open(filepath.Join(dir, name))
		if e != nil {
			if os.IsNotExist(e) {
				res.Gap = res.Gap || i > 0
				continue
			}
			finish()
			return res, e
		}
		fi, e := f.Stat()
		if e != nil {
			f.Close()
			finish()
			return res, e
		}
		if off > fi.Size() { // truncated in place
			off, res.Gap = 0, true
			*a = assembler{}
		}
		if _, e = f.Seek(off, io.SeekStart); e != nil {
			f.Close()
			finish()
			return res, e
		}
		r := bufio.NewReaderSize(f, 64<<10)
		var rerr error
		eof := false
		for budget > 0 || a.open {
			rec, n, complete, e := readRecord(r)
			if e != nil {
				rerr = e
				break
			}
			if n == 0 || (!complete && name == liveName && !final) {
				eof = true // nothing left, or a record still being written
				break
			}
			before := off
			off += n
			budget -= n
			res.Consumed += n
			ts, stream, partial, msg, e := ParseCRI(rec)
			if e != nil {
				malformedRecords.Add(1)
			} else {
				wasOpen := a.open
				a.add(ts, stream, partial, msg)
				if a.open && !wasOpen {
					seq = FileCursor{File: name, Inode: inodeOf(fi), Offset: before}
				}
			}
			if !complete {
				eof = true
				break
			}
		}
		f.Close()
		res.Next = FileCursor{File: name, Inode: inodeOf(fi), Offset: off, Skip: a.skip}
		if rerr != nil {
			finish()
			return res, rerr
		}
		if budget <= 0 && !a.open {
			break
		}
		res.AtEnd = eof && i == len(chain)-1
	}
	if final && a.open && res.AtEnd {
		a.emit(cleanMessage(a.buf), false)
	}
	finish()
	res.AtEnd = res.AtEnd && !a.open
	if len(chain) == 0 {
		res.AtEnd = true
	}
	return res, nil
}
