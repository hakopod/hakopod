package cluster

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Stdin must not start while the exec guard is still checking the Pod receipt.
// This handshake protects against accidental replacement; it does not authenticate
// a hostile cluster administrator or an attacker who can replace the guard binary.
type sandboxGatedInput struct {
	ctx    context.Context
	ready  <-chan struct{}
	reader io.Reader
}

func (r *sandboxGatedInput) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	case <-r.ready:
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type sandboxHandshake struct {
	mu       sync.Mutex
	expected []byte
	matched  int
	ready    chan struct{}
	output   io.Writer
	cancel   context.CancelFunc
	verified bool
}

func (h *sandboxHandshake) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	total := len(p)
	if !h.verified {
		n := min(len(p), len(h.expected)-h.matched)
		if !bytes.Equal(p[:n], h.expected[h.matched:h.matched+n]) {
			h.cancel()
			return 0, fmt.Errorf("session guard identity handshake failed")
		}
		h.matched += n
		p = p[n:]
		if h.matched == len(h.expected) {
			h.verified = true
			close(h.ready)
		}
	}
	if len(p) > 0 {
		n, err := h.output.Write(p)
		return total - len(p) + n, err
	}
	return total, nil
}
func (h *sandboxHandshake) Verified() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.verified }

type sandboxInputBound struct {
	reader    io.Reader
	remaining int64
	exceeded  atomic.Bool
	cancel    context.CancelFunc
}

func (b *sandboxInputBound) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.reader == nil {
		return 0, io.EOF
	}
	if b.remaining == 0 {
		var one [1]byte
		n, err := b.reader.Read(one[:])
		if n > 0 {
			b.exceeded.Store(true)
			if b.cancel != nil {
				b.cancel()
			}
			return 0, fmt.Errorf("session input exceeds its byte limit")
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.reader.Read(p)
	b.remaining -= int64(n)
	return n, err
}

// Stdout and stderr share one budget. Reserve bytes before a stream writes them.
type sandboxOutputBudget struct {
	mu        sync.Mutex
	remaining int64
	exceeded  bool
	cancel    context.CancelFunc
}

func (b *sandboxOutputBudget) reserve(n int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if int64(n) > b.remaining {
		b.exceeded = true
		if b.cancel != nil {
			b.cancel()
		}
		return fmt.Errorf("session output exceeds its byte limit")
	}
	b.remaining -= int64(n)
	return nil
}
func (b *sandboxOutputBudget) Exceeded() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.exceeded }

type sandboxOutputBound struct {
	writer io.Writer
	budget *sandboxOutputBudget
}

func (b *sandboxOutputBound) Write(p []byte) (int, error) {
	if err := b.budget.reserve(len(p)); err != nil {
		return 0, err
	}
	n, err := b.writer.Write(p)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}
