package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

// ManagedPlatformRuntime opens the operation's authenticated encrypted
// snapshot and dispatches to the matching native reconciler. It must never
// derive runtime secrets from request data or log the snapshot.
type ManagedPlatformRuntime interface {
	ReconcileManagedPlatform(context.Context, *store.Store, store.ManagedPlatformOperation) error
}

func (w *Worker) runManagedPlatform(parent context.Context) {
	claimCtx, cancelClaim := context.WithTimeout(parent, 3*time.Second)
	operation, err := w.Store.ClaimManagedPlatformOperation(claimCtx)
	cancelClaim()
	if err != nil {
		return
	}
	attemptCtx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	if err = w.ManagedPlatforms.ReconcileManagedPlatform(attemptCtx, w.Store, operation); err == nil {
		return
	}
	if attemptCtx.Err() != nil && parent.Err() != nil {
		return
	}
	finishCtx, finishCancel := context.WithTimeout(parent, 3*time.Second)
	defer finishCancel()
	if recordErr := w.Store.RecordManagedPlatformStep(finishCtx, operation, "queued", "retry", "Managed platform reconciliation will retry.", map[string]any{"status": "pending", "revision": operation.Revision}); recordErr != nil {
		slog.Warn("managed platform retry could not be recorded", "operation", operation.ID)
	}
}
