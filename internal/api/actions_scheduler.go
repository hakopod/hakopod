package api

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
)

const actionsWorkers = 4

type actionsWarmupKey struct{}
type actionsWarmup struct{ pending bool }

func (*actionsWarmup) Error() string { return "Runner pool still has empty job slots" }
func requestActionsWarmup(ctx context.Context) {
	if warm, ok := ctx.Value(actionsWarmupKey{}).(*actionsWarmup); ok {
		warm.pending = true
	}
}

type actionsSchedule interface {
	NextActionsPool(context.Context) (*store.ActionsPoolWork, error)
	CompleteActionsPool(context.Context, store.ActionsPoolWork, time.Time, bool) error
}

func runActionsWorkers(ctx context.Context, schedule actionsSchedule, reconcile func(context.Context, store.ActionsPool) error) {
	runActionsWorkersWithClock(ctx, schedule, reconcile, time.Now)
}

func runActionsWorkersWithClock(ctx context.Context, schedule actionsSchedule, reconcile func(context.Context, store.ActionsPool) error, now func() time.Time) {
	var workers sync.WaitGroup
	for i := 0; i < actionsWorkers; i++ {
		workers.Go(func() {
			for ctx.Err() == nil {
				lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
				work, err := schedule.NextActionsPool(lookup)
				cancel()
				if err != nil || work == nil {
					timer := time.NewTimer(time.Second)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
					continue
				}
				step, stop := context.WithTimeout(ctx, 45*time.Second)
				err = reconcile(step, work.Pool)
				stop()
				next := now().Add(10 * time.Second)
				var retry *actions.RetryError
				progress := err == nil
				var warm *actionsWarmup
				if errors.As(err, &warm) {
					next = now().Add(time.Second)
					progress = true
				}
				if errors.As(err, &retry) {
					next = maxTime(now().Add(time.Second), retry.At)
					progress = retry.Progress
				}
				finish, done := context.WithTimeout(ctx, 5*time.Second)
				_ = schedule.CompleteActionsPool(finish, *work, next, progress)
				done()
			}
		})
	}
	workers.Wait()
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (s *Server) reconcileScheduledActions(ctx context.Context, p store.ActionsPool) error {
	warm := &actionsWarmup{}
	ctx = context.WithValue(ctx, actionsWarmupKey{}, warm)
	claim, err := s.Store.ClaimActions(ctx, p.ApplicationID)
	if err != nil || claim == nil {
		return err
	}
	defer claim.Release()
	// Re-read under the application lock so a queued task cannot restore an old
	// deployment's pool configuration or scope.
	fresh, err := s.Store.ActionsPool(ctx, p.ApplicationID, p.Service)
	if err != nil || fresh == nil {
		return err
	}
	p = *fresh
	t := actionsTarget(p)
	t.BeforeStep = claim.Check
	err = s.reconcileActionsPool(ctx, t, p)
	if err != nil {
		// Persist a safe explanation even when the reconciliation timed out.
		message, done := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		text := safeActionsError(err)
		var retry *actions.RetryError
		if errors.As(err, &retry) && retry.Local {
			text = ""
		}
		_ = s.Store.ActionsMessage(message, p, text)
		done()
	}
	if err == nil && warm.pending {
		return warm
	}
	return err
}

func (s *Server) actionsProvider(ctx context.Context, t cluster.Target, credential string) (*actions.Client, error) {
	token, err := s.actionRuntime().ActionsCredential(ctx, t, credential)
	if err != nil {
		return nil, err
	}
	return actions.NewWithBudget(token, &s.actionsBudget)
}
