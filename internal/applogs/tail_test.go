package applogs

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/logmask"
)

func rec(sec int, stream, tag, msg string) string {
	return fmt.Sprintf("2026-03-01T10:00:%02d.123456789Z %s %s %s\n", sec, stream, tag, msg)
}

type fixture struct {
	t    *testing.T
	tl   FileTailer
	dir  string
	read func(cur FileCursor, max int64) ([]Line, FileCursor, bool)
}

func newFixture(t *testing.T) *fixture {
	root := t.TempDir()
	f := &fixture{t: t, tl: FileTailer{Root: root}}
	f.dir = filepath.Join(f.tl.PodDir("hp-app", "web-1", "uid1"), "web")
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.read = func(cur FileCursor, max int64) ([]Line, FileCursor, bool) {
		lines, next, gap, err := f.tl.Read("hp-app", "web-1", "uid1", "web", 0, cur, max)
		if err != nil {
			t.Fatal(err)
		}
		return lines, next, gap
	}
	return f
}

func (f *fixture) write(name, s string) {
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(s), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) appendTo(name, s string) {
	fh, err := os.OpenFile(filepath.Join(f.dir, name), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		f.t.Fatal(err)
	}
	defer fh.Close()
	fh.WriteString(s)
}

func msgs(ls []Line) []string {
	out := []string{}
	for _, l := range ls {
		out = append(out, l.Message)
	}
	return out
}

func same(t *testing.T, got []Line, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if g := msgs(got); !reflect.DeepEqual(g, want) {
		t.Fatalf("lines = %q, want %q", g, want)
	}
}

func TestParseCRI(t *testing.T) {
	tests := []struct {
		name, in, stream, msg string
		partial, bad          bool
	}{
		{"final", "2026-03-01T10:00:01.5Z stdout F hello world\n", "stdout", "hello world", false, false},
		{"partial", "2026-03-01T10:00:01Z stderr P half", "stderr", "half", true, false},
		{"extra flags", "2026-03-01T10:00:01Z stdout P:x half", "stdout", "half", true, false},
		{"empty message", "2026-03-01T10:00:01Z stdout F \n", "stdout", "", false, false},
		{"no message or space", "2026-03-01T10:00:01Z stdout F\n", "stdout", "", false, false},
		{"bad time", "yesterday stdout F x\n", "", "", false, true},
		{"bad stream", "2026-03-01T10:00:01Z stdin F x\n", "", "", false, true},
		{"bad tag", "2026-03-01T10:00:01Z stdout X x\n", "", "", false, true},
		{"garbage", "garbage\n", "", "", false, true},
		{"empty", "", "", "", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, stream, partial, msg, err := ParseCRI([]byte(tc.in))
			if (err != nil) != tc.bad {
				t.Fatalf("err = %v, want bad=%v", err, tc.bad)
			}
			if !tc.bad && (stream != tc.stream || partial != tc.partial || string(msg) != tc.msg) {
				t.Fatalf("got %q %v %q", stream, partial, msg)
			}
		})
	}
}

func TestTailIncrementalAndHalfRecord(t *testing.T) {
	f := newFixture(t)
	f.write("0.log", rec(1, "stdout", "F", "one")+rec(2, "stderr", "F", "two")+"2026-03-01T10:00:03Z stdout F thr")
	lines, cur, gap := f.read(FileCursor{}, 0)
	same(t, lines, "one", "two")
	if gap || lines[1].Stream != "stderr" || lines[0].Time.Second() != 1 {
		t.Fatalf("gap=%v lines=%+v", gap, lines)
	}
	want := int64(len(rec(1, "stdout", "F", "one") + rec(2, "stderr", "F", "two")))
	if cur.Offset != want || cur.Inode == 0 || cur.File != "0.log" {
		t.Fatalf("cursor = %+v, want offset %d", cur, want)
	}
	// Nothing new: the half record is not consumed and the offset stays.
	lines, again, _ := f.read(cur, 0)
	same(t, lines)
	if again.Offset != cur.Offset {
		t.Fatalf("offset moved to %d", again.Offset)
	}
	f.appendTo("0.log", "ee\n"+rec(4, "stdout", "F", "four"))
	lines, cur2, _ := f.read(cur, 0)
	same(t, lines, "three", "four")
	if cur2.Offset <= cur.Offset {
		t.Fatal("offset did not advance")
	}
}

func TestTailPartialsAcrossCalls(t *testing.T) {
	f := newFixture(t)
	first := rec(1, "stdout", "F", "before") + rec(2, "stdout", "P", "SECRET-hel") + rec(3, "stdout", "P", "lo ")
	f.write("0.log", first)
	lines, cur, _ := f.read(FileCursor{}, 0)
	same(t, lines, "before")
	// The cursor points back at the start of the P sequence and holds no text.
	if want := int64(len(rec(1, "stdout", "F", "before"))); cur.Offset != want {
		t.Fatalf("offset = %d, want %d", cur.Offset, want)
	}
	if js, _ := json.Marshal(cur); strings.Contains(string(js), "SECRET") || strings.Contains(string(js), "U0VDUkVU") {
		t.Fatalf("cursor carries message bytes: %s", js)
	}
	f.appendTo("0.log", rec(4, "stdout", "F", "world")+rec(5, "stdout", "F", "next"))
	lines, cur, _ = f.read(cur, 0)
	same(t, lines, "SECRET-hello world", "next")
	if lines[0].Time.Second() != 2 {
		t.Fatalf("joined line time = %v, want the first record's", lines[0].Time)
	}
	if cur.Offset != int64(len(first+rec(4, "stdout", "F", "world")+rec(5, "stdout", "F", "next"))) {
		t.Fatalf("offset = %d", cur.Offset)
	}
}

func TestTailLongLines(t *testing.T) {
	f := newFixture(t)
	big := strings.Repeat("é", 20000) // 40 KB, more than the 16 KiB storage cut
	f.write("0.log", rec(1, "stdout", "F", big)+
		rec(2, "stdout", "F", strings.Repeat("x", 70<<10))+
		rec(3, "stdout", "F", strings.Repeat("y", logmask.MaxLineBytes))+
		rec(4, "stdout", "F", "after"))
	lines, _, _ := f.read(FileCursor{}, 0)
	if len(lines) != 4 || lines[0].Message != big || lines[0].Truncated {
		t.Fatalf("40 KB line was cut: %d lines", len(lines))
	}
	if lines[1].Message != withheldMessage || !lines[1].Truncated || lines[1].Time.Second() != 2 {
		t.Fatalf("70 KiB line: %+v", lines[1])
	}
	if len(lines[2].Message) != logmask.MaxLineBytes || lines[2].Truncated || lines[3].Message != "after" {
		t.Fatal("line of exactly 64 KiB must be kept")
	}
}

func TestTailStarvationGuard(t *testing.T) {
	f := newFixture(t)
	// Many P records totalling over 64 KiB, split over several calls, then F.
	chunk := strings.Repeat("z", 10000)
	var content string
	for i := 0; i < 8; i++ {
		content += rec(1, "stdout", "P", chunk)
	}
	f.write("0.log", content)
	lines, cur, _ := f.read(FileCursor{}, 0)
	same(t, lines, withheldMessage)
	if !lines[0].Truncated || !cur.Skip {
		t.Fatalf("cursor = %+v", cur)
	}
	f.appendTo("0.log", rec(2, "stdout", "P", chunk)+rec(3, "stdout", "F", "tail of it")+rec(4, "stdout", "F", "next"))
	lines, cur, _ = f.read(cur, 0)
	same(t, lines, "next")
	if cur.Skip {
		t.Fatal("skip not cleared after F")
	}
	// A sequence larger than maxBytes still completes in one call.
	f2 := newFixture(t)
	f2.write("0.log", rec(1, "stdout", "P", "aaa")+rec(2, "stdout", "P", "bbb")+rec(3, "stdout", "F", "ccc")+rec(4, "stdout", "F", "d"))
	lines, cur, _ = f2.read(FileCursor{}, 1)
	same(t, lines, "aaabbbccc")
	lines, _, _ = f2.read(cur, 1)
	same(t, lines, "d")
}

func TestTailRotationBetweenPartialAndFinal(t *testing.T) {
	f := newFixture(t)
	f.write("0.log", rec(1, "stdout", "F", "before")+rec(2, "stdout", "P", "token=SUPERSE"))
	lines, cur, _ := f.read(FileCursor{}, 0)
	same(t, lines, "before")
	rotatedName := "0.log.20260301-100005"
	os.Rename(filepath.Join(f.dir, "0.log"), filepath.Join(f.dir, rotatedName))
	f.write("0.log", rec(3, "stdout", "F", "CRET123")+rec(4, "stdout", "F", "next"))
	// Read stops with the sequence open only if the chain ends first; here it
	// completes, so one whole line comes back.
	res, err := f.tl.ReadN("hp-app", "web-1", "uid1", "web", 0, cur, 0)
	if err != nil || res.Gap {
		t.Fatalf("err=%v gap=%v", err, res.Gap)
	}
	same(t, res.Lines, "token=SUPERSECRET123", "next")
	if !res.AtEnd {
		t.Fatal("expected AtEnd")
	}

	// The new live file has not been written yet: the cursor stays in the
	// rotated file, by inode, at the start of the sequence.
	g := newFixture(t)
	g.write("0.log", rec(1, "stdout", "P", "token=SUPERSE"))
	_, c0, _ := g.read(FileCursor{}, 0)
	os.Rename(filepath.Join(g.dir, "0.log"), filepath.Join(g.dir, rotatedName))
	res, _ = g.tl.ReadN("hp-app", "web-1", "uid1", "web", 0, c0, 0)
	if len(res.Lines) != 0 || res.AtEnd || res.Next.File != rotatedName || res.Next.Offset != 0 || res.Next.Inode != c0.Inode {
		t.Fatalf("res = %+v", res)
	}
	g.write("0.log", rec(2, "stdout", "F", "CRET123"))
	res, _ = g.tl.ReadN("hp-app", "web-1", "uid1", "web", 0, res.Next, 0)
	same(t, res.Lines, "token=SUPERSECRET123")

	// If the rotated file is gzipped away first, nothing partial is returned.
	h := newFixture(t)
	h.write("0.log", rec(1, "stdout", "P", "token=SUPERSE"))
	_, c1, _ := h.read(FileCursor{}, 0)
	os.Rename(filepath.Join(h.dir, "0.log"), filepath.Join(h.dir, rotatedName))
	os.Rename(filepath.Join(h.dir, rotatedName), filepath.Join(h.dir, rotatedName+".gz"))
	h.write("0.log", rec(2, "stdout", "F", "CRET123")+rec(3, "stdout", "F", "next"))
	lines, _, gap := h.read(c1, 0)
	if !gap || len(lines) != 2 || lines[0].Message != unknownStartMessage || !lines[0].Truncated || lines[1].Message != "next" {
		t.Fatalf("gap=%v lines=%+v", gap, lines)
	}
}

func TestTailContinuationAtStart(t *testing.T) {
	f := newFixture(t)
	// A rotated sibling exists, so the live file may begin mid-line.
	f.write("0.log.20260301-000000.gz", "x")
	f.write("0.log", rec(1, "stdout", "F", "maybe the end of a line")+rec(2, "stdout", "F", "whole"))
	lines, _, _ := f.read(FileCursor{}, 0)
	if len(lines) != 2 || lines[0].Message != unknownStartMessage || !lines[0].Truncated || lines[1].Message != "whole" {
		t.Fatalf("lines = %+v", lines)
	}
	// No sibling: the file starts at the beginning of the container's output.
	g := newFixture(t)
	g.write("0.log", rec(1, "stdout", "F", "first"))
	lines, _, _ = g.read(FileCursor{}, 0)
	same(t, lines, "first")
}

func TestTailAtEndAndConsumed(t *testing.T) {
	f := newFixture(t)
	big := rec(1, "stdout", "F", strings.Repeat("x", 70<<10))
	f.write("0.log", big+rec(2, "stdout", "F", "after"))
	rd := func(cur FileCursor, max int64) TailResult {
		res, err := f.tl.ReadN("hp-app", "web-1", "uid1", "web", 0, cur, max)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	// The budget is used up entirely by the skipped over-long line.
	res := rd(FileCursor{}, 1000)
	same(t, res.Lines, withheldMessage)
	if res.AtEnd || res.Consumed != int64(len(big)) {
		t.Fatalf("AtEnd=%v consumed=%d want %d", res.AtEnd, res.Consumed, len(big))
	}
	res = rd(res.Next, 0)
	same(t, res.Lines, "after")
	if !res.AtEnd || res.Consumed != int64(len(rec(2, "stdout", "F", "after"))) {
		t.Fatalf("AtEnd=%v consumed=%d", res.AtEnd, res.Consumed)
	}
	// An open sequence at the end of the live file is not the end.
	f.appendTo("0.log", rec(3, "stdout", "P", "half"))
	if res = rd(res.Next, 0); res.AtEnd || len(res.Lines) != 0 {
		t.Fatalf("res = %+v", res)
	}
	// Mid-file with a small budget.
	if res = rd(FileCursor{}, 1); res.AtEnd {
		t.Fatal("AtEnd mid-file")
	}
}

func TestTailSanitise(t *testing.T) {
	f := newFixture(t)
	f.write("0.log", rec(1, "stdout", "F", "a\xffb\x00c\r")+"not a record\n"+rec(2, "stdout", "F", "ok"))
	lines, _, _ := f.read(FileCursor{}, 0)
	same(t, lines, "a�bc", "ok")
	if malformedRecords.Load() == 0 {
		t.Fatal("malformed record not counted")
	}
}

func TestTailRotation(t *testing.T) {
	f := newFixture(t)
	f.write("0.log", rec(1, "stdout", "F", "a")+rec(2, "stdout", "F", "b"))
	lines, cur, _ := f.read(FileCursor{}, 0)
	same(t, lines, "a", "b")
	// More lines land in the old file, then kubelet rotates and a new file starts.
	f.appendTo("0.log", rec(3, "stdout", "F", "c")+rec(4, "stdout", "F", "d"))
	if err := os.Rename(filepath.Join(f.dir, "0.log"), filepath.Join(f.dir, "0.log.20260301-100005")); err != nil {
		t.Fatal(err)
	}
	f.write("0.log", rec(5, "stdout", "F", "e")+rec(6, "stdout", "F", "f"))
	lines, cur, gap := f.read(cur, 0)
	same(t, lines, "c", "d", "e", "f")
	if gap || cur.File != "0.log" {
		t.Fatalf("gap=%v cursor=%+v", gap, cur)
	}
	lines, _, _ = f.read(cur, 0)
	same(t, lines)
}

func TestTailRotatedFileGone(t *testing.T) {
	f := newFixture(t)
	f.write("0.log", rec(1, "stdout", "F", "a"))
	_, cur, _ := f.read(FileCursor{}, 0)
	old := filepath.Join(f.dir, "0.log.20260301-100005")
	f.appendTo("0.log", rec(2, "stdout", "F", "lost"))
	os.Rename(filepath.Join(f.dir, "0.log"), old)
	// Gzip it and remove the plain file: the tailer must say so.
	gz, _ := os.Create(old + ".gz")
	zw := gzip.NewWriter(gz)
	zw.Write([]byte("x"))
	zw.Close()
	gz.Close()
	os.Remove(old)
	f.write("0.log", rec(3, "stdout", "F", "new"))
	lines, next, gap := f.read(cur, 0)
	same(t, lines, unknownStartMessage) // "new" may continue a line that was lost
	if !gap {
		t.Fatal("expected gap")
	}
	if _, _, gap = f.read(next, 0); gap {
		t.Fatal("gap repeated")
	}
	// With an older plain rotated file still present it is read first.
	f.write("0.log.20260301-100001", rec(1, "stdout", "F", "kept"))
	lines, _, gap = f.read(cur, 0)
	same(t, lines, unknownStartMessage, "new")
	if !gap {
		t.Fatal("expected gap")
	}
}

func TestTailTruncation(t *testing.T) {
	f := newFixture(t)
	f.write("0.log", rec(1, "stdout", "F", "a long first line")+rec(2, "stdout", "F", "second"))
	_, cur, _ := f.read(FileCursor{}, 0)
	// Same inode, now shorter than the offset.
	f.write("0.log", rec(1, "stdout", "F", "x"))
	lines, next, gap := f.read(cur, 0)
	same(t, lines, "x")
	if !gap || next.Offset != int64(len(rec(1, "stdout", "F", "x"))) {
		t.Fatalf("gap=%v cursor=%+v", gap, next)
	}
}

func TestTailMaxBytes(t *testing.T) {
	f := newFixture(t)
	var all []string
	var content string
	for i := 0; i < 10; i++ {
		m := fmt.Sprintf("line-%02d", i)
		all = append(all, m)
		content += rec(i, "stdout", "F", m)
	}
	f.write("0.log", content)
	one := int64(len(rec(0, "stdout", "F", "line-00")))
	var got []Line
	var cur FileCursor
	for i := 0; i < 20; i++ {
		lines, next, _ := f.read(cur, 3*one)
		if len(lines) > 3 {
			t.Fatalf("call %d returned %d lines for a 3 line budget", i, len(lines))
		}
		got = append(got, lines...)
		cur = next
	}
	same(t, got, all...)
	// A budget smaller than one record still makes progress.
	lines, _, _ := f.read(FileCursor{}, 1)
	same(t, lines, "line-00")
}

func TestTailPodDirAndExists(t *testing.T) {
	root := t.TempDir()
	tl := FileTailer{Root: root}
	if got, want := tl.PodDir("ns", "p", "u"), filepath.Join(root, "ns_p_u"); got != want {
		t.Fatalf("PodDir = %q", got)
	}
	if tl.Exists("ns", "p", "u") {
		t.Fatal("exists before create")
	}
	os.Mkdir(tl.PodDir("ns", "p", "u"), 0o755)
	if !tl.Exists("ns", "p", "u") || tl.Exists("..", "p", "u") {
		t.Fatal("Exists wrong")
	}
	t.Setenv("HAKOPOD_POD_LOG_ROOT", "/x/pods")
	if got := (FileTailer{}).PodDir("a", "b", "c"); got != "/x/pods/a_b_c" {
		t.Fatalf("env root: %q", got)
	}
	if _, _, _, err := tl.Read("ns", "p", "u", "../etc", 0, FileCursor{}, 0); err == nil {
		t.Fatal("expected error for unsafe container")
	}
	if lines, _, gap, err := tl.Read("ns", "gone", "u", "c", 0, FileCursor{}, 0); err != nil || gap || len(lines) != 0 {
		t.Fatalf("missing dir: %v %v", gap, err)
	}
}

func TestTailReadFinal(t *testing.T) {
	f := newFixture(t)
	f.write("0.log", rec(1, "stdout", "F", "one")+rec(2, "stdout", "P", "last ")+rec(3, "stdout", "P", "words")+"2026-03-01T10:00:04Z stdout F unterminated")
	rd := func(final bool, cur FileCursor) TailResult {
		fn := f.tl.ReadN
		if final {
			fn = f.tl.ReadFinal
		}
		res, err := fn("hp-app", "web-1", "uid1", "web", 0, cur, 0)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	// ReadN waits for the F record and the end of the last record.
	if res := rd(false, FileCursor{}); len(res.Lines) != 1 || res.AtEnd {
		t.Fatalf("ReadN: %+v", res)
	}
	// ReadFinal returns everything, with nothing left open.
	res := rd(true, FileCursor{})
	same(t, res.Lines, "one", "last wordsunterminated")
	if !res.AtEnd || res.Next.Offset != int64(len(rec(1, "stdout", "F", "one")+rec(2, "stdout", "P", "last ")+rec(3, "stdout", "P", "words")+"2026-03-01T10:00:04Z stdout F unterminated")) {
		t.Fatalf("res = %+v", res)
	}
	if again := rd(true, res.Next); len(again.Lines) != 0 || !again.AtEnd {
		t.Fatalf("again = %+v", again)
	}
	// A sequence that never gets its F is emitted whole.
	g := newFixture(t)
	g.write("0.log", rec(1, "stdout", "P", "cut ")+rec(2, "stdout", "P", "off"))
	res, _ = g.tl.ReadFinal("hp-app", "web-1", "uid1", "web", 0, FileCursor{}, 0)
	same(t, res.Lines, "cut off")
	if !res.AtEnd {
		t.Fatal("expected AtEnd")
	}
}

func TestTailInodeReuse(t *testing.T) {
	// Pretend every file has the same inode, as when the kernel hands the
	// inode of a deleted rotated file to the new live file.
	orig := fileIdentity
	fileIdentity = func(path string, fi os.FileInfo) (uint64, string) {
		_, first := orig(path, fi)
		return 42, first
	}
	t.Cleanup(func() { fileIdentity = orig })

	f := newFixture(t)
	f.write("0.log", rec(1, "stdout", "F", "a")+rec(2, "stdout", "F", "b"))
	_, cur, _ := f.read(FileCursor{}, 0)
	if cur.First == "" || cur.Inode != 42 {
		t.Fatalf("cursor = %+v", cur)
	}
	os.Remove(filepath.Join(f.dir, "0.log"))
	f.write("0.log", rec(5, "stdout", "F", "new1")+rec(6, "stdout", "F", "new2")+rec(7, "stdout", "F", "new3"))
	lines, next, gap := f.read(cur, 0)
	if !gap || len(lines) != 3 || lines[0].Message != unknownStartMessage || lines[1].Message != "new2" {
		t.Fatalf("gap=%v lines=%+v", gap, lines)
	}
	if next.First == cur.First {
		t.Fatal("cursor kept the old identity")
	}
	// An old cursor without First is matched by inode once, then filled in.
	old := FileCursor{File: "0.log", Inode: 42, Offset: next.Offset}
	f.appendTo("0.log", rec(8, "stdout", "F", "new4"))
	lines, upgraded, gap := f.read(old, 0)
	same(t, lines, "new4")
	if gap || upgraded.First != next.First {
		t.Fatalf("gap=%v cursor=%+v", gap, upgraded)
	}
	// A new file with no first record yet is left alone.
	g := newFixture(t)
	g.write("0.log", rec(1, "stdout", "F", "a"))
	_, c, _ := g.read(FileCursor{}, 0)
	os.Remove(filepath.Join(g.dir, "0.log"))
	g.write("0.log", "")
	res, _ := g.tl.ReadN("hp-app", "web-1", "uid1", "web", 0, c, 0)
	if res.Gap || len(res.Lines) != 0 || res.Next != c {
		t.Fatalf("res = %+v", res)
	}
}
