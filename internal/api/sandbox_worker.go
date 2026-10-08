package api

import (
	"context"
	"errors"
	"time"

	"github.com/hakopod/hakopod/internal/sandbox"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) sandboxRuntime() sandbox.Runtime {
	if s.sessionTestRuntime != nil {
		return s.sessionTestRuntime
	}
	if s.Cluster == nil {
		return nil
	}
	runtime, _ := any(s.Cluster).(sandbox.Runtime)
	return runtime
}

// RunSessions reconciles short leases so idle kernels do not consume controller lanes.
func (s *Server) RunSessions(ctx context.Context) {
	runtime := s.sandboxRuntime()
	if runtime == nil {
		return
	}
	for lane := 0; lane < 2; lane++ {
		go func() {
			for ctx.Err() == nil {
				record, err := s.Store.ClaimSession(ctx)
				if err == nil {
					s.reconcileSession(ctx, record, runtime)
				}
				if !invocationPause(ctx, time.Second) {
					return
				}
			}
		}()
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_ = s.Store.PruneSessions(ctx)
		}
	}
}
func (s *Server) reconcileSession(ctx context.Context, r sandbox.Record, runtime sandbox.Runtime) {
	step, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	defer func() {
		release, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = s.Store.ReleaseSessionLease(release, r)
	}()
	if r.Status == sandbox.Closing || r.Status == sandbox.Closed {
		clean, err := runtime.CleanupSession(step, r)
		if err == nil && clean {
			_ = s.Store.SessionCleaned(step, r)
		}
		return
	}
	authority := s.Store.CheckSessionLease(step, r)
	if authority != nil {
		if errors.Is(authority, store.ErrForbidden) || errors.Is(authority, store.ErrUnauthorized) || errors.Is(authority, store.ErrConflict) {
			_ = s.Store.RequestSessionCleanup(step, r, "Session authority expired; cleanup is pending")
		}
		return
	}
	if r.CallToken != "" && !time.Now().Before(r.CallUntil) {
		_ = s.Store.RequestSessionCleanup(step, r, "Session call outcome is uncertain; cleanup is pending")
		return
	}
	if r.Status == sandbox.Starting && time.Since(r.CreatedAt) > 2*time.Minute {
		_ = s.Store.RequestSessionCleanup(step, r, "Session startup deadline expired; cleanup is pending")
		return
	}
	var state sandbox.RuntimeState
	var err error
	if r.Status == sandbox.Starting {
		state, err = runtime.StartSession(step, r, func(ctx context.Context) error { return s.Store.CheckSessionLease(ctx, r) })
	} else {
		state, err = runtime.ObserveSession(step, r)
	}
	if state.NamespaceUID != "" {
		if saveErr := s.Store.SaveSessionRuntime(step, r, state); saveErr != nil {
			return
		}
	}
	if err != nil {
		_ = s.Store.RequestSessionCleanup(step, r, "Session runtime is unavailable; cleanup is pending")
	}
}
