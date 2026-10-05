package store

import (
	"context"
	"errors"
	"time"
)

var errDatabaseMaintenanceActive = errors.New("managed database maintenance is active")

const (
	databaseMaintenanceWaitLimit = 25 * time.Second
	databaseMaintenancePoll      = 200 * time.Millisecond
)

// waitForDatabaseMaintenance retries only admission attempts fenced by a
// short-lived database maintenance claim. Each attempt owns and closes its own
// transaction, so maintenance release is never blocked while this waits.
func waitForDatabaseMaintenance[T any](ctx context.Context, attempt func(context.Context) (T, error)) (T, error) {
	return waitForDatabaseMaintenanceBounded(ctx, databaseMaintenanceWaitLimit, databaseMaintenancePoll, attempt)
}

func waitForDatabaseMaintenanceBounded[T any](ctx context.Context, limit, poll time.Duration, attempt func(context.Context) (T, error)) (T, error) {
	wait, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var zero T
	var lastMaintenance error
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if wait.Err() != nil {
			if lastMaintenance != nil {
				return zero, lastMaintenance
			}
			return zero, wait.Err()
		}
		result, err := attempt(wait)
		// A successful attempt may have committed just as its context expired.
		// Preserve that durable receipt instead of reporting a stale wait error.
		if err == nil {
			return result, nil
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if wait.Err() != nil {
			if lastMaintenance != nil {
				return zero, lastMaintenance
			}
			if err != nil {
				return zero, err
			}
			return zero, wait.Err()
		}
		if !errors.Is(err, errDatabaseMaintenanceActive) {
			return result, err
		}
		lastMaintenance = err
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-wait.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return zero, ctx.Err()
			}
			return zero, lastMaintenance
		case <-timer.C:
		}
	}
}
