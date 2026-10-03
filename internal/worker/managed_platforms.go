package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	if errors.Is(err, pgx.ErrNoRows) {
		operation, err = w.Store.ClaimManagedPlatformMaintenance(claimCtx)
	}
	cancelClaim()
	if err != nil {
		return
	}
	// Native database bootstrap can outlast one lease. Renew the exact
	// operation while it runs, and stop its work if that authority is lost.
	attemptCtx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	err, leaseLost := reconcileManagedPlatformWithLease(attemptCtx, 5*time.Second,
		func(ctx context.Context) error { return w.Store.HeartbeatManagedPlatformOperation(ctx, operation) },
		func(ctx context.Context) error {
			return w.ManagedPlatforms.ReconcileManagedPlatform(ctx, w.Store, operation)
		})
	if err == nil || leaseLost {
		return
	}
	if attemptCtx.Err() != nil && parent.Err() != nil {
		return
	}
	errorCategory, errorType := managedPlatformErrorObservation(err)
	finishCtx, finishCancel := context.WithTimeout(parent, 3*time.Second)
	defer finishCancel()
	if recordErr := w.Store.RecordManagedPlatformStep(finishCtx, operation, "queued", "retry", "Managed platform reconciliation will retry.", map[string]any{"status": "pending", "revision": operation.Revision, "error_category": errorCategory, "error_type": errorType}); recordErr != nil {
		slog.Warn("managed platform retry could not be recorded", "operation", operation.ID, "error_category", errorCategory, "error_type", errorType)
	}
}

// Reconciliation and renewal share cancellation, but a completed operation
// stops its renewer without turning a successful result into cancellation.
func reconcileManagedPlatformWithLease(parent context.Context, interval time.Duration, heartbeat, reconcile func(context.Context) error) (error, bool) {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	renewCtx, stopRenewing := context.WithCancel(ctx)
	defer stopRenewing()
	renewed := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				renewed <- nil
				return
			case <-ticker.C:
				checkCtx, stopCheck := context.WithTimeout(renewCtx, 3*time.Second)
				err := heartbeat(checkCtx)
				stopCheck()
				if err != nil {
					if renewCtx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
						renewed <- nil
						return
					}
					cancel(err)
					renewed <- err
					return
				}
			}
		}
	}()
	err := reconcile(ctx)
	stopRenewing()
	if renewalErr := <-renewed; renewalErr != nil {
		return renewalErr, true
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause, false
	}
	return err, false
}

var managedPlatformSafeCategories = map[string]struct{}{
	"capacity_admission": {}, "neon_snapshot_mismatch": {}, "neon_snapshot_missing": {},
	"neon_proxy_activate": {}, "neon_namespace": {}, "neon_claims": {}, "neon_provider_state": {},
	"neon_runtime_repair": {}, "neon_tls_prepare": {}, "neon_lifecycle_prepare": {}, "neon_lifecycle_deprovision": {},
	"neon_node_inventory": {}, "neon_recovery_binding": {}, "neon_controller_secret": {}, "neon_render": {},
	"neon_secret_validate": {}, "neon_secret_apply": {}, "neon_object_apply": {}, "neon_bootstrap_observe": {},
	"neon_lifecycle_provision": {}, "neon_compute_replay": {}, "neon_serving_observe": {}, "neon_snapshot_prune": {}, "neon_recovery_observe": {},
	"runtime_unavailable": {}, "snapshot_invalid": {}, "supabase_assets_invalid": {},
	"supabase_apply_configmap": {}, "supabase_apply_deployment": {},
	"supabase_apply_networkpolicy": {}, "supabase_apply_pvc": {},
	"supabase_apply_secret": {}, "supabase_apply_service": {},
	"supabase_apply_statefulset": {}, "supabase_claims": {},
	"supabase_database_tls_validation": {}, "supabase_database_url_validation": {},
	"supabase_gateway_validation": {}, "supabase_snapshot_mismatch": {},
	"supabase_snapshot_missing": {}, "supabase_namespace": {},
	"supabase_observe": {}, "supabase_qualification": {}, "neon_qualification": {}, "supabase_render": {}, "unsupported_kind": {},
	"supabase_rotate_database_credentials": {},
	"platform_identity":                    {}, "platform_tls_observe": {},
}

func managedPlatformErrorObservation(err error) (string, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return "context_deadline", fmt.Sprintf("%T", context.DeadlineExceeded)
	}
	if errors.Is(err, context.Canceled) {
		return "context_cancelled", fmt.Sprintf("%T", context.Canceled)
	}
	for _, item := range []struct {
		target   error
		category string
	}{{store.ErrConflict, "store_conflict"}, {store.ErrInput, "store_input"}, {store.ErrForbidden, "store_forbidden"}, {store.ErrUnauthorized, "store_unauthorized"}} {
		if errors.Is(err, item.target) {
			return item.category, fmt.Sprintf("%T", item.target)
		}
	}
	for _, item := range []struct {
		match    func(error) bool
		category string
	}{{apierrors.IsNotFound, "kubernetes_not_found"}, {apierrors.IsAlreadyExists, "kubernetes_already_exists"}, {apierrors.IsConflict, "kubernetes_conflict"}, {apierrors.IsForbidden, "kubernetes_forbidden"}, {apierrors.IsUnauthorized, "kubernetes_unauthorized"}, {apierrors.IsInvalid, "kubernetes_invalid"}, {apierrors.IsTimeout, "kubernetes_timeout"}, {apierrors.IsServerTimeout, "kubernetes_timeout"}, {apierrors.IsTooManyRequests, "kubernetes_too_many_requests"}, {apierrors.IsServiceUnavailable, "kubernetes_unavailable"}} {
		if item.match(err) {
			return item.category, "*errors.StatusError"
		}
	}
	var connectError *pgconn.ConnectError
	if errors.As(err, &connectError) {
		return "postgres_connect", fmt.Sprintf("%T", connectError)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		if postgresError.Code == "40001" {
			return "postgres_serialization", fmt.Sprintf("%T", postgresError)
		}
		return "postgres_error", fmt.Sprintf("%T", postgresError)
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return "network_timeout", fmt.Sprintf("%T", networkError)
	}
	type safeCategorized interface{ SafeCategory() string }
	var categorized safeCategorized
	if errors.As(err, &categorized) {
		if _, ok := managedPlatformSafeCategories[categorized.SafeCategory()]; ok {
			return categorized.SafeCategory(), fmt.Sprintf("%T", categorized)
		}
	}
	return "other", fmt.Sprintf("%T", err)
}
