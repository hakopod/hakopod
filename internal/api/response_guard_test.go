package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// net.Pipe has no receive buffer: response writes really block until the client
// reads, so the test does not depend on a host's TCP socket-buffer capacity.
type responsePipeListener struct {
	connection chan net.Conn
	closed     chan struct{}
	once       sync.Once
	addr       net.Addr
}

func (l *responsePipeListener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.connection:
		return connection, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *responsePipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}
func (l *responsePipeListener) Addr() net.Addr { return l.addr }

func TestResponseWritesStopOnCancellationAndDeadline(t *testing.T) {
	for _, cancelDuringWrite := range []bool{true, false} {
		name := "write-deadline"
		if cancelDuringWrite {
			name = "authority-cancellation"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			serverConnection, client := net.Pipe()
			listener := &responsePipeListener{connection: make(chan net.Conn, 1), closed: make(chan struct{}), addr: serverConnection.LocalAddr()}
			listener.connection <- serverConnection
			entered := make(chan struct{})
			finished := make(chan error, 1)
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				timeout := 100 * time.Millisecond
				if cancelDuringWrite {
					timeout = 5 * time.Second
				}
				writes := guardResponseWrites(ctx, w, timeout)
				var writeErr error
				defer func() {
					writes.stop()
					finished <- writeErr
				}()
				if !writes.begin() {
					writeErr = errors.New("initial write deadline was unavailable")
					return
				}
				close(entered)
				_, writeErr = io.WriteString(w, strings.Repeat("x", 64<<10))
				if cancelDuringWrite && writes.begin() {
					writeErr = nil // A racing write must not extend revoked authority.
				}
			})}
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(func() {
				_ = client.Close()
				_ = server.Close()
				_ = listener.Close()
			})
			_ = client.SetWriteDeadline(time.Now().Add(time.Second))
			if _, err := io.WriteString(client, "GET / HTTP/1.1\r\nHost: fixture\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("HTTP handler did not start")
			}
			select {
			case err := <-finished:
				t.Fatalf("response did not block on the stalled client: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			if cancelDuringWrite {
				cancel()
			}
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("stalled response ignored its deadline or cancellation")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("stalled response retained its handler after cancellation/deadline")
			}
		})
	}
}
