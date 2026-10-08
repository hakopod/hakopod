package cluster

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type sandboxCountingReader struct{ calls atomic.Int64 }

func (r *sandboxCountingReader) Read(p []byte) (int, error) {
	r.calls.Add(1)
	return copy(p, "input"), io.EOF
}
func TestSandboxHandshakeGatesInputAndStripsFragmentedPrefix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	h := &sandboxHandshake{expected: []byte("HAKOPOD_SESSION_READY " + strings.Repeat("a", 64) + "\n"), ready: make(chan struct{}), output: &out, cancel: cancel}
	reader := &sandboxCountingReader{}
	gated := &sandboxGatedInput{ctx: ctx, ready: h.ready, reader: reader}
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(gated); done <- err }()
	select {
	case <-done:
		t.Fatal("input returned before guard handshake")
	case <-time.After(20 * time.Millisecond):
	}
	for _, v := range h.expected[:len(h.expected)-1] {
		if _, err := h.Write([]byte{v}); err != nil {
			t.Fatal(err)
		}
	}
	if reader.calls.Load() != 0 {
		t.Fatal("caller input read before complete identity handshake")
	}
	if _, err := h.Write([]byte("\npayload")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !h.Verified() || out.String() != "payload" {
		t.Fatal("handshake bytes escaped into caller output")
	}
}
func TestSandboxBadHandshakeCancelsWithoutReadingInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &sandboxHandshake{expected: []byte("expected"), ready: make(chan struct{}), output: io.Discard, cancel: cancel}
	reader := &sandboxCountingReader{}
	gated := &sandboxGatedInput{ctx: ctx, ready: h.ready, reader: reader}
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(gated); done <- err }()
	if _, err := h.Write([]byte("foreign")); err == nil {
		t.Fatal("foreign handshake accepted")
	}
	if err := <-done; err == nil || reader.calls.Load() != 0 || h.Verified() {
		t.Fatal("foreign guard received caller input")
	}
}
func TestSandboxStdoutAndStderrShareBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &sandboxOutputBudget{remaining: 10, cancel: cancel}
	var output bytes.Buffer
	out := &sandboxOutputBound{writer: &output, budget: b}
	stderr := &sandboxOutputBound{writer: io.Discard, budget: b}
	if _, err := out.Write([]byte("12345")); err != nil {
		t.Fatal(err)
	}
	if _, err := stderr.Write([]byte("12345")); err != nil {
		t.Fatal(err)
	}
	if _, err := stderr.Write([]byte("x")); err == nil || !b.Exceeded() || ctx.Err() == nil {
		t.Fatal("stderr escaped shared output limit")
	}
	if output.String() != "12345" {
		t.Fatal("stderr leaked into stdout")
	}
}
func TestSandboxConcurrentOutputCannotExceedBudget(t *testing.T) {
	b := &sandboxOutputBudget{remaining: 1000}
	var ok atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := &sandboxOutputBound{writer: io.Discard, budget: b}
			for j := 0; j < 100; j++ {
				if n, e := w.Write([]byte("x")); e == nil {
					ok.Add(int64(n))
				}
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1000 || !b.Exceeded() {
		t.Fatalf("shared limit failed: %d", ok.Load())
	}
}
func TestSandboxMissingOrPartialHandshakeCancellation(t *testing.T) {
	for _, prefix := range []string{"", "HAKOPOD_SESSION_"} {
		t.Run(prefix, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			h := &sandboxHandshake{expected: []byte("HAKOPOD_SESSION_READY " + strings.Repeat("b", 64) + "\n"), ready: make(chan struct{}), output: io.Discard, cancel: cancel}
			if _, err := h.Write([]byte(prefix)); err != nil {
				t.Fatal(err)
			}
			reader := &sandboxCountingReader{}
			gated := &sandboxGatedInput{ctx: ctx, ready: h.ready, reader: reader}
			done := make(chan error, 1)
			go func() { _, err := io.ReadAll(gated); done <- err }()
			cancel()
			if err := <-done; err == nil || reader.calls.Load() != 0 || h.Verified() {
				t.Fatal("incomplete guard handshake released caller input")
			}
		})
	}
}
