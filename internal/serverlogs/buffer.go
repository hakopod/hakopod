// Package serverlogs retains a bounded, redacted view of this API process's logs.
package serverlogs

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const capacity = 200
const maxEntry = 2048
const omitted = "[credential-related entry omitted; inspect the host logs if needed]"

var credential = regexp.MustCompile(`(?i)(password|secret|token|authorization|cookie|private.key|gh[pousr]_|glpat-)`)
var userinfo = regexp.MustCompile(`(?i)(?:postgres(?:ql)?|https?)://[^\s@/]+:[^\s@/]+@`)

type Entry struct {
	Timestamp string `json:"timestamp"`
	Message   string `json:"message"`
}

type Snapshot struct {
	Entries    []Entry   `json:"entries"`
	ObservedAt time.Time `json:"observed_at"`
	Source     string    `json:"source"`
	StartedAt  time.Time `json:"started_at"`
}

type Buffer struct {
	mu          sync.Mutex
	entries     [capacity]Entry
	next, count int
	started     time.Time
}

func New() *Buffer { return &Buffer{started: time.Now().UTC()} }

// Write accepts one structured logging record per write, as slog's handler
// emits it. Oversized records are omitted, not partially retained. This writer
// never reads host files or stores the unredacted record.
func (b *Buffer) Write(p []byte) (int, error) {
	message := "[oversized log entry omitted; inspect the host logs if needed]"
	if len(p) <= 8192 {
		message = strings.TrimSpace(string(p))
		if credential.MatchString(message) {
			message = omitted
		} else {
			message = userinfo.ReplaceAllString(message, "[redacted]@")
			if len(message) > maxEntry {
				message = message[:maxEntry]
				for !utf8.ValidString(message) {
					message = message[:len(message)-1]
				}
			}
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries[b.next] = Entry{Timestamp: strconv.FormatInt(time.Now().UnixMicro(), 10), Message: strings.Clone(message)}
	b.next = (b.next + 1) % capacity
	if b.count < capacity {
		b.count++
	}
	return len(p), nil
}

func (b *Buffer) Snapshot() Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	entries := make([]Entry, b.count)
	for i := range entries {
		entries[i] = b.entries[(b.next-b.count+i+capacity)%capacity]
	}
	// Bound encoded JSON too: escapes can expand otherwise short log messages.
	size, first := 0, len(entries)
	for first > 0 {
		encoded, _ := json.Marshal(entries[first-1])
		if size+len(encoded)+1 > 480<<10 {
			break
		}
		size += len(encoded) + 1
		first--
	}
	return Snapshot{Entries: entries[first:], ObservedAt: time.Now().UTC(), Source: "process", StartedAt: b.started}
}
