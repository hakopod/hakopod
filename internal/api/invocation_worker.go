package api

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/invocation"
	"github.com/hakopod/hakopod/internal/store"
)

type invocationRuntime interface {
	StartInvocation(context.Context, invocation.Record, []byte, func(context.Context) error) (invocation.RuntimeState, error)
	ObserveInvocation(context.Context, invocation.Record) (invocation.RuntimeState, error)
	CleanupInvocation(context.Context, invocation.Record) (bool, error)
}

// RunInvocations uses four bounded lanes and one active job per application.
// A process restart resumes receipts; it never creates a new invocation ID.
func (s *Server) RunInvocations(ctx context.Context) {
	if len(s.authEncryptionKey()) != 32 || s.Cluster == nil {
		return
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				claim, err := s.Store.ClaimInvocation(ctx)
				if err == nil && claim != nil {
					s.runInvocation(ctx, claim, s.Cluster)
				}
				if !invocationPause(ctx, time.Second) {
					return
				}
			}
		}()
	}
	wg.Wait()
}
func invocationPause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Server) runInvocation(ctx context.Context, c *store.InvocationClaim, runtime invocationRuntime) {
	defer c.Release()
	for ctx.Err() == nil {
		r := c.Record
		if r.Terminal() {
			// Keep the receipt and input through a short cleanup reconciliation window.
			// This covers an in-flight create whose HTTP response was lost on shutdown.
			clean, err := runtime.CleanupInvocation(ctx, r)
			if err != nil {
				return
			}
			if clean && r.FinishedAt != nil && time.Since(*r.FinishedAt) >= time.Minute {
				_ = c.Cleaned(ctx)
				return
			}
			if !invocationPause(ctx, time.Second) {
				return
			}
			continue
		}
		err := c.Check(ctx)
		cancelled := errors.Is(err, store.ErrInvocationCancelled) || errors.Is(err, store.ErrUnauthorized) || errors.Is(err, store.ErrForbidden)
		if err != nil && !cancelled {
			return
		}
		timeout := time.Duration(r.Source.Services[r.Service].Job.TimeoutSeconds)*time.Second + time.Minute
		expired := r.StartedAt != nil && time.Since(*r.StartedAt) > timeout
		if cancelled || expired {
			// Save the terminal receipt first. cleanup_pending keeps deployment fenced.
			status, message := invocation.Cancelled, "Invocation cancelled; cleanup is pending"
			if expired {
				status, message = invocation.Failed, "Invocation deadline expired; cleanup is pending"
			}
			if c.Save(ctx, invocation.RuntimeState{Status: status, Message: message}, nil) != nil {
				return
			}
			continue
		}
		var state invocation.RuntimeState
		if r.Status == invocation.Starting {
			input, e := invocation.Open(s.authEncryptionKey(), r.ID, "input", r.EncryptedInput)
			if e != nil {
				if c.Save(ctx, invocation.RuntimeState{Status: invocation.Failed, Message: "Invocation input could not be decrypted"}, nil) != nil {
					return
				}
				continue
			}
			state, err = runtime.StartInvocation(ctx, r, input, c.Check)
			clear(input)
		} else {
			state, err = runtime.ObserveInvocation(ctx, r)
		}
		logsUnavailable := err != nil && (state.Status == invocation.Succeeded || state.Status == invocation.Failed)
		if logsUnavailable {
			// A completed Job can have unavailable logs (for example ImagePullBackOff).
			// Persist a failed receipt instead of retaining the application forever.
			state.Status = invocation.Failed
			state.LogTruncated = true
			err = nil
		}
		if err != nil {
			if state.NamespaceUID != "" || state.RuntimeUID != "" {
				state.Status = r.Status
				state.Message = "Invocation reconciliation is pending"
				if c.Save(ctx, state, nil) != nil {
					return
				}
			}
			if !invocationPause(ctx, 2*time.Second) {
				return
			}
			continue
		}
		switch state.Status {
		case invocation.Succeeded:
			state.Message = "Invocation completed"
		case invocation.Failed:
			state.Message = "Invocation failed"
		default:
			state.Message = "Invocation is running"
		}
		if logsUnavailable {
			state.Message = "Invocation finished but its complete logs are unavailable"
		}
		var encrypted []byte
		if state.Status == invocation.Succeeded || state.Status == invocation.Failed {
			encrypted, err = invocation.Seal(s.authEncryptionKey(), r.ID, "logs", state.Log)
			clear(state.Log)
			if err != nil {
				return
			}
		}
		if c.Save(ctx, state, encrypted) != nil {
			return
		}
		if !invocationPause(ctx, time.Second) {
			return
		}
	}
}
