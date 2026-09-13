// Package worker runs a small, PostgreSQL-backed at-least-once work pool.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
)

type Worker struct {
	Store       *store.Store
	Cluster     *cluster.Client
	Concurrency int
	Timeout     time.Duration
}

var errCancelled = errors.New("cancellation requested; already applied resources remain")
var errAuthority = errors.New("deployment authority revoked or expired; new steps stopped; existing resources remain")
var errDatabase = errors.New("database connectivity lost; operation will resume")

func (w *Worker) Run(ctx context.Context) {
	n := w.Concurrency
	if n < 1 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			timer := time.NewTicker(2 * time.Second)
			defer timer.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				claim, err := w.Store.Claim(ctx)
				if err != nil && ctx.Err() == nil {
					slog.Warn("durable work temporarily unavailable")
				}
				if claim != nil {
					w.run(ctx, claim)
					continue
				}
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
			}
		}()
	}
	wg.Wait()
}
func (w *Worker) run(parent context.Context, c *store.Claim) {
	defer c.Release()
	d := c.Deployment
	a := c.App
	timeout := w.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancelTimeout := context.WithTimeout(parent, timeout)
	defer cancelTimeout()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	emit := func(e cluster.Event) {
		eventCtx, done := context.WithTimeout(ctx, 3*time.Second)
		defer done()
		if err := w.Store.Event(eventCtx, d.ID, e.Type, e.Message, e.Service); err != nil {
			cancel(errDatabase)
		}
	}
	if err := w.Store.Reauthorize(ctx, d.KeyID, a.Project, a.Environment, a.Name); err != nil {
		if errors.Is(err, store.ErrUnauthorized) || errors.Is(err, store.ErrForbidden) {
			w.finish(parent, c, "cancelled", errAuthority.Error(), map[string]any{"status": "cancelled"})
		}
		return
	}
	if d.CancelRequested {
		w.finish(parent, c, "cancelled", errCancelled.Error(), map[string]any{"status": "cancelled"})
		return
	}
	// Guard uses the lock-holding connection, so losing the advisory lock cancels
	// in-flight Kubernetes requests before this worker makes another step.
	guardDone := make(chan struct{})
	go func() {
		defer close(guardDone)
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				checkCtx, done := context.WithTimeout(ctx, 3*time.Second)
				err := c.Check(checkCtx)
				if err == nil {
					err = w.Store.Reauthorize(checkCtx, d.KeyID, a.Project, a.Environment, a.Name)
				}
				if err != nil {
					done()
					if errors.Is(err, store.ErrUnauthorized) || errors.Is(err, store.ErrForbidden) {
						cancel(errAuthority)
					} else {
						cancel(errDatabase)
					}
					return
				}
				current, err := w.Store.Deployment(checkCtx, d.ID)
				done()
				if err != nil {
					cancel(errDatabase)
					return
				}
				if current.CancelRequested {
					cancel(errCancelled)
					return
				}
			}
		}
	}()
	defer func() { cancel(nil); <-guardDone }()
	emit(cluster.Event{Type: "running", Message: "Reconciling immutable application release"})
	previous, err := w.Store.LastHealthy(ctx, a.ID, d.Revision)
	if err != nil {
		cancel(errDatabase)
		return
	}
	resolved := d.ResolvedSpec
	if resolved == nil {
		app, err := w.Cluster.ResolveScoped(ctx, d.Spec, a.Project, a.Environment)
		if err != nil {
			w.handleFailure(parent, ctx, c, err, nil)
			return
		}
		if app, err = c.SetResolved(ctx, app); err != nil {
			cancel(errDatabase)
			return
		}
		resolved = &app
		emit(cluster.Event{Type: "resolved", Message: "All container images resolved to immutable digests"})
	}
	target := cluster.Target{Project: a.Project, Environment: a.Environment, ApplicationID: a.ID, OperationID: d.ID, Revision: d.Revision, Spec: *resolved, Previous: previous}
	target.BeforeStep = func(stepCtx context.Context) error {
		if err := stepCtx.Err(); err != nil {
			return err
		}
		if err := w.Store.Reauthorize(stepCtx, d.KeyID, a.Project, a.Environment, a.Name); err != nil {
			if stepCtx.Err() != nil {
				return stepCtx.Err()
			}
			if errors.Is(err, store.ErrUnauthorized) || errors.Is(err, store.ErrForbidden) {
				cancel(errAuthority)
			} else {
				cancel(errDatabase)
			}
			return context.Cause(ctx)
		}
		current, err := w.Store.Deployment(stepCtx, d.ID)
		if err != nil {
			if stepCtx.Err() != nil {
				return stepCtx.Err()
			}
			cancel(errDatabase)
			return errDatabase
		}
		if current.CancelRequested {
			cancel(errCancelled)
			return errCancelled
		}
		lockCtx, done := context.WithTimeout(stepCtx, 3*time.Second)
		err = c.Check(lockCtx)
		done()
		if err != nil {
			cancel(errDatabase)
			return errDatabase
		}
		return nil
	}
	deployCtx, endDeploy := context.WithTimeout(ctx, timeout*2/3)
	observation, err := w.Cluster.Deploy(deployCtx, target, emit)
	endDeploy()
	if err == nil {
		emit(cluster.Event{Type: "succeeded", Message: "Every service is ready; release completed"})
		if ctx.Err() != nil {
			w.handleFailure(parent, ctx, c, context.Cause(ctx), &observation)
			return
		}
		w.finish(parent, c, "succeeded", "", observation)
		return
	}
	if ctx.Err() != nil {
		w.handleFailure(parent, ctx, c, err, &observation)
		return
	}
	// Kubernetes does not automatically undo a stalled group. Recover the full
	// last successful revision; failed release and recovery stay in its event log.
	message := err.Error()
	emit(cluster.Event{Type: "failed", Message: message})
	if previous != nil {
		emit(cluster.Event{Type: "recovery_started", Message: "Restoring the complete last successful group; multi-service updates are not atomic"})
		// Failed rollout must not exhaust the separate, bounded recovery window.
		recoveryCtx, stop := context.WithTimeout(ctx, timeout/2)
		target.Spec = *previous
		target.Previous = resolved
		recovery, recoveryErr := w.Cluster.Deploy(recoveryCtx, target, emit)
		stop()
		if recoveryErr == nil {
			observation = recovery
			message += "; previous healthy release restored"
			emit(cluster.Event{Type: "recovered", Message: "Previous healthy service configuration restored"})
		} else {
			message += "; recovery failed: " + recoveryErr.Error()
			emit(cluster.Event{Type: "recovery_failed", Message: recoveryErr.Error()})
		}
	}
	if ctx.Err() != nil {
		w.handleFailure(parent, ctx, c, errors.New(message), &observation)
		return
	}
	w.finish(parent, c, "failed", message, observation)
}
func (w *Worker) handleFailure(parent, ctx context.Context, c *store.Claim, err error, result any) {
	cause := context.Cause(ctx)
	if parent.Err() != nil || errors.Is(cause, errDatabase) {
		return
	} // Leave running for restart/resume.
	status := "failed"
	message := err.Error()
	if errors.Is(cause, errCancelled) || errors.Is(cause, errAuthority) {
		status = "cancelled"
		message = cause.Error()
	}
	if result == nil {
		result = map[string]any{"status": status, "observed_at": time.Now().UTC()}
	}
	w.finish(parent, c, status, message, result)
}
func (w *Worker) finish(ctx context.Context, c *store.Claim, status, message string, result any) {
	d := c.Deployment
	finishCtx, done := context.WithTimeout(ctx, 5*time.Second)
	defer done()
	if err := c.Finish(finishCtx, status, message, result); err != nil {
		slog.Warn("operation finalization deferred until database recovery", "operation", d.ID)
		return
	}
	if status != "succeeded" {
		_ = w.Store.Event(finishCtx, d.ID, status, message, "")
	}
	slog.Info("deployment finished", "operation", d.ID, "status", status)
}

// Resync is bounded and reads only managed applications. Kubernetes is the
// source of runtime truth; PostgreSQL snapshots include their observation time.
func (w *Worker) Resync(ctx context.Context) {
	timer := time.NewTicker(15 * time.Second)
	defer timer.Stop()
	cursor := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			rows, err := w.Store.Pool.Query(ctx, "SELECT id FROM applications WHERE id>$1 ORDER BY id LIMIT 50", cursor)
			if err != nil {
				continue
			}
			ids := []string{}
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					ids = append(ids, id)
				}
			}
			rows.Close()
			if len(ids) == 0 {
				cursor = ""
				continue
			}
			cursor = ids[len(ids)-1]
			for _, id := range ids {
				if ctx.Err() != nil {
					return
				}
				a, err := w.Store.Application(ctx, id)
				if err != nil {
					continue
				}
				observeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				o, err := w.Cluster.Observe(observeCtx, cluster.Target{Project: a.Project, Environment: a.Environment, ApplicationID: a.ID, Revision: a.Revision, Spec: a.Spec})
				cancel()
				if err != nil {
					o = cluster.Observation{Revision: a.Revision, Status: "unknown", Services: []cluster.ServiceStatus{}, ObservedAt: time.Now().UTC()}
				}
				_, err = w.Store.Pool.Exec(ctx, "UPDATE applications SET observed=$2 WHERE id=$1 AND revision=$3", id, store.JSON(o), a.Revision)
				if err != nil {
					slog.Debug(fmt.Sprintf("runtime observation for %s deferred", id))
				}
			}
		}
	}
}
