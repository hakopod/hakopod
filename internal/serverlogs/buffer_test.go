package serverlogs

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestBoundAndSnapshotIsolation(t *testing.T) {
	b := New()
	for i := 0; i < 250; i++ {
		fmt.Fprintf(b, "entry %d", i)
	}
	s := b.Snapshot()
	if len(s.Entries) != 200 || s.Entries[0].Message != "entry 50" || s.Entries[199].Message != "entry 249" || s.Source != "process" || s.StartedAt.IsZero() {
		t.Fatal(s)
	}
	s.Entries[0].Message = "changed"
	if b.Snapshot().Entries[0].Message != "entry 50" {
		t.Fatal("snapshot aliases buffer")
	}
}

func TestRedactionBeforeTruncation(t *testing.T) {
	for _, value := range []string{"secret=private", "Authorization: private", "-----BEGIN PRIVATE KEY-----", "glpat-private", strings.Repeat("x", 3000) + " token=private"} {
		b := New()
		b.Write([]byte(value))
		if got := b.Snapshot().Entries[0].Message; got != omitted {
			t.Fatal(got)
		}
	}
	b := New()
	b.Write([]byte("connect https://user:credential@example.test/path"))
	if got := b.Snapshot().Entries[0].Message; strings.Contains(got, "credential") || strings.Contains(got, "user:") {
		t.Fatal(got)
	}
	b.Write([]byte(strings.Repeat("a", 10000)))
	if got := b.Snapshot().Entries[1].Message; !strings.Contains(got, "oversized") {
		t.Fatal(got)
	}
	b.Write([]byte(strings.Repeat("€", 2000)))
	if got := b.Snapshot().Entries[2].Message; len(got) > 2048 || !utf8.ValidString(got) {
		t.Fatal("invalid bounded UTF-8")
	}
}

func TestConcurrentWritersAndReaders(t *testing.T) {
	b := New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b.Write([]byte("API ready"))
				b.Snapshot()
			}
		}()
	}
	wg.Wait()
	if len(b.Snapshot().Entries) != 200 {
		t.Fatal("incorrect concurrent retention")
	}
}

func TestEncodedResponseBound(t *testing.T) {
	b := New()
	for i := 0; i < 200; i++ {
		b.Write([]byte(strings.Repeat("\x00", 2048)))
	}
	encoded, err := json.Marshal(b.Snapshot())
	if err != nil || len(encoded) > 512<<10 {
		t.Fatalf("response exceeds bound: %d %v", len(encoded), err)
	}
	if len(b.Snapshot().Entries) == 0 {
		t.Fatal("no bounded entries retained")
	}
}
