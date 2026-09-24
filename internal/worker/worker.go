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
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type Runtime interface {
	ResolveScoped(context.Context, spec.Application, string, string) (spec.Application, error)
	Deploy(context.Context, cluster.Target, func(cluster.Event)) (cluster.Observation, error)
	Observe(context.Context, cluster.Target) (cluster.Observation, error)
	RenewBackendCertificates(context.Context, cluster.Target, func(cluster.Event), ...string) error
	DeletePreview(context.Context, cluster.Target) error
}

type Worker struct {
	Store       *store.Store
	Cluster     Runtime
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
	// Namespace deletion can wait for Kubernetes finalizers. Give expiry its own
	// single bounded lane so a stuck preview does not delay normal deployments.
	wg.Add(1)
	go func() {
		defer wg.Done()
		timer := time.NewTicker(5 * time.Second)
		defer timer.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			w.cleanupPreview(ctx)
			w.cleanupRetained(ctx)
			w.cleanupServiceVolumes(ctx)
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
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
	previousRelease, err := w.Store.LastHealthyRelease(ctx, a.ID, d.Revision)
	if err != nil {
		cancel(errDatabase)
		return
	}
	var previous *spec.Application
	if previousRelease != nil {
		previous = previousRelease.ResolvedSpec
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
	if d.RecoveryState != "" {
		w.recoverRelease(parent, ctx, c, target, emit)
		return
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
	message := err.Error()
	emit(cluster.Event{Type: "failed", Message: message})
	skip := spec.RecoveryBlocked(*resolved, previous)
	if err := c.BeginRecovery(ctx, previousRelease, message, skip); err != nil {
		cancel(errDatabase)
		return
	}
	if skip != "" {
		emit(cluster.Event{Type: "recovery_skipped", Message: skip})
		w.finish(parent, c, "failed", message+"; "+skip, observation)
		return
	}
	w.recoverRelease(parent, ctx, c, target, emit)
}

func (w *Worker) recoverRelease(parent, ctx context.Context, c *store.Claim, target cluster.Target, emit func(cluster.Event)) {
	d := c.Deployment
	if d.RecoveryState == "running" {
		if d.RecoverySpec == nil {
			return
		}
		emit(cluster.Event{Type: "recovery_started", Message: fmt.Sprintf("Restoring workload configuration from revision %d. External data and secret values are not rolled back", d.RecoveryRevision)})
		failedSpec := target.Spec
		target.Previous = &failedSpec
		target.Spec = *d.RecoverySpec
		timeout := w.Timeout / 2
		if timeout == 0 {
			timeout = 5 * time.Minute
		}
		bounded, stop := context.WithTimeout(ctx, timeout)
		result, err := w.Cluster.Deploy(bounded, target, emit)
		stop()
		if ctx.Err() != nil {
			w.handleFailure(parent, ctx, c, context.Cause(ctx), &result)
			return
		}
		state, message := "succeeded", ""
		if err != nil {
			state = "failed"
			message = err.Error()
		}
		if e := c.FinishRecovery(ctx, state, message, result); e != nil {
			return
		}
		d = c.Deployment
		if state == "succeeded" {
			emit(cluster.Event{Type: "recovered", Message: fmt.Sprintf("Revision %d workload configuration restored; the attempted release remains failed", d.RecoveryRevision)})
		} else {
			emit(cluster.Event{Type: "recovery_failed", Message: message})
		}
	}
	message := d.Error
	if d.RecoveryState == "succeeded" {
		message += "; previous healthy workload configuration restored"
	} else {
		message += "; recovery " + d.RecoveryState + ": " + d.RecoveryError
	}
	w.finish(parent, c, "failed", message, d.Result)
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
	if d.RecoveryState == "running" && status != "succeeded" {
		// Cancellation, revoked authority or a timeout terminates recovery too.
		// Process/database interruptions never reach finish and remain resumable.
		if err := c.FinishRecovery(finishCtx, "failed", "Recovery stopped: "+message, result); err != nil {
			slog.Warn("recovery finalization deferred until database recovery", "operation", d.ID)
			return
		}
	}
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
	var renewalRound uint64
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
				renewalRound++
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
				effective, e := w.Store.ObservedSpec(ctx, a)
				if e != nil {
					continue
				}
				a.Spec = effective
				w.renewCertificates(ctx, a, renewalRound)
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

// Maintenance shares the deployment claim and rechecks it before each write.
func (w *Worker) renewCertificates(parent context.Context, a store.Application, round uint64) {
	if !spec.HasAutomaticCertificates(a.Spec) {
		return
	}
	names := []string{}
	for _, name := range spec.Names(a.Spec) {
		for _, mount := range a.Spec.Services[name].CertificateMounts {
			if mount.Source == "ingress" {
				names = append(names, name)
				break
			}
		}
	}
	if len(names) == 0 {
		return
	}
	selected := names[round%uint64(len(names))]
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	claim, err := w.Store.ClaimRuntime(ctx, a.ID, a.Revision)
	if err != nil || claim == nil {
		return
	}
	defer claim.Release()
	target := cluster.Target{Project: a.Project, Environment: a.Environment, ApplicationID: a.ID, Revision: a.Revision, Spec: a.Spec, BeforeStep: claim.Check}
	err = w.Cluster.RenewBackendCertificates(ctx, target, func(event cluster.Event) {
		_ = w.Store.Event(ctx, claim.OperationID, event.Type, event.Message, event.Service)
	}, selected)
	if err != nil && ctx.Err() == nil {
		slog.Debug("automatic certificate renewal deferred", "application", a.ID)
	}
}
