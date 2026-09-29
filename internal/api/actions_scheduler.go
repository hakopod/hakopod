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
	claim, err := s.Store.ClaimActions(ctx, p.ApplicationID)
	if err != nil || claim == nil {
		return err
	}
	defer claim.Release()
	// Re-read under the application lock so a queued task cannot restore an old
	// deployment's pool configuration or scope.
	pools, err := s.Store.ActionsPools(ctx, p.ApplicationID)
	if err != nil {
		return err
	}
	for _, fresh := range pools {
		if fresh.Service != p.Service {
			continue
		}
		p = fresh
		t := actionsTarget(p)
		t.BeforeStep = claim.Check
		err = s.reconcileActionsPool(ctx, t, p)
		if err != nil {
			// Persist a safe explanation even when the reconciliation timed out.
			message, done := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_ = s.Store.ActionsMessage(message, p, safeActionsError(err))
			done()
		}
		return err
	}
	return nil
}

func (s *Server) actionsProvider(ctx context.Context, t cluster.Target, credential string) (*actions.Client, error) {
	token, err := s.actionRuntime().ActionsCredential(ctx, t, credential)
	if err != nil {
		return nil, err
	}
	return actions.NewWithBudget(token, &s.actionsBudget)
}
