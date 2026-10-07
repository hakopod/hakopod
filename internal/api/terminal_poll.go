package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type terminalPollContextKey struct{}

const terminalPollBytes = 64 << 10

type terminalFrame struct {
	Cursor int64           `json:"cursor"`
	Data   json.RawMessage `json:"data"`
}
type terminalPollBuffer struct {
	mu     sync.Mutex
	frames []terminalFrame
	bytes  int
	cursor int64
	done   bool
}

func (b *terminalPollBuffer) append(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cursor++
	b.frames = append(b.frames, terminalFrame{b.cursor, append(json.RawMessage(nil), data...)})
	b.bytes += len(data)
	for (b.bytes > terminalPollBytes || len(b.frames) > 256) && len(b.frames) > 0 {
		b.bytes -= len(b.frames[0].Data)
		b.frames = b.frames[1:]
	}
}

type terminalPollWriter struct {
	header http.Header
	buffer *terminalPollBuffer
}

func (w *terminalPollWriter) Header() http.Header              { return w.header }
func (w *terminalPollWriter) WriteHeader(int)                  {}
func (w *terminalPollWriter) Flush()                           {}
func (w *terminalPollWriter) SetWriteDeadline(time.Time) error { return nil }
func (w *terminalPollWriter) Write(p []byte) (int, error) {
	// Canonical terminalOutput emits each complete JSON frame in one write.
	if len(p) > 8 && string(p[:6]) == "data: " {
		raw := p[6 : len(p)-2]
		if json.Valid(raw) {
			w.buffer.append(raw)
		}
	}
	if len(p) > 0 && p[0] == '{' && json.Valid(p) {
		w.buffer.append(p)
	}
	return len(p), nil
}
func (s *Server) terminalPoll(w http.ResponseWriter, r *http.Request) {
	cursor, err := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	if r.URL.Query().Get("cursor") == "" {
		cursor = 0
		err = nil
	}
	if err != nil || cursor < 0 {
		problem(w, 400, "invalid_cursor", "Supply a nonnegative terminal cursor")
		return
	}
	x, _, ok := s.terminalFor(w, r)
	if !ok {
		return
	}
	x.mu.Lock()
	if x.poll == nil {
		if x.started {
			x.mu.Unlock()
			problem(w, 409, "already_connected", "This terminal already uses streaming output")
			return
		}
		x.poll = &terminalPollBuffer{}
		request := r.Clone(context.WithValue(context.WithoutCancel(r.Context()), terminalPollContextKey{}, true))
		writer := &terminalPollWriter{header: http.Header{}, buffer: x.poll}
		go s.terminalOutput(writer, request)
	}
	buffer := x.poll
	x.mu.Unlock()
	out, err := buffer.sample(cursor)
	if err != nil {
		problem(w, 400, "invalid_cursor", "Cursor is ahead of terminal output")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, 200, out)
}
func (b *terminalPollBuffer) sample(cursor int64) (map[string]any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cursor > b.cursor {
		return nil, errors.New("cursor ahead of output")
	}
	frames := []terminalFrame{}
	for _, frame := range b.frames {
		if frame.Cursor > cursor {
			frames = append(frames, terminalFrame{frame.Cursor, append(json.RawMessage(nil), frame.Data...)})
		}
	}
	truncated := len(b.frames) > 0 && cursor < b.frames[0].Cursor-1
	return map[string]any{"frames": frames, "next_cursor": strconv.FormatInt(b.cursor, 10), "truncated": truncated, "done": b.done}, nil
}
