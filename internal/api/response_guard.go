package api

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// responseWriteGuard bounds slow response consumers and interrupts an in-flight
// write when authority or request lifetime ends. stop must run before the handler
// returns, so its watcher never touches a reused ResponseWriter.
type responseWriteGuard struct {
	ctx        context.Context
	controller *http.ResponseController
	timeout    time.Duration
	mu         sync.Mutex
	stopped    chan struct{}
	done       chan struct{}
}

func guardResponseWrites(ctx context.Context, w http.ResponseWriter, timeout time.Duration) *responseWriteGuard {
	g := &responseWriteGuard{ctx: ctx, controller: http.NewResponseController(w), timeout: timeout, stopped: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(g.done)
		select {
		case <-ctx.Done():
			g.mu.Lock()
			_ = g.controller.SetWriteDeadline(time.Now())
			g.mu.Unlock()
		case <-g.stopped:
		}
	}()
	return g
}

func (g *responseWriteGuard) begin() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ctx.Err() != nil {
		return false
	}
	// Serialize deadline changes with cancellation: an emit may not replace an
	// expired authority deadline with another full write timeout.
	return g.controller.SetWriteDeadline(time.Now().Add(g.timeout)) == nil
}

func (g *responseWriteGuard) stop() {
	close(g.stopped)
	<-g.done
}
