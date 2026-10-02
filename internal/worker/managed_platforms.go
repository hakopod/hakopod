package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/hakopod/hakopod/internal/store"
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
	errorCategory, errorType := managedPlatformErrorObservation(err)
	finishCtx, finishCancel := context.WithTimeout(parent, 3*time.Second)
	defer finishCancel()
	if recordErr := w.Store.RecordManagedPlatformStep(finishCtx, operation, "queued", "retry", "Managed platform reconciliation will retry.", map[string]any{"status": "pending", "revision": operation.Revision, "error_category": errorCategory, "error_type": errorType}); recordErr != nil {
		slog.Warn("managed platform retry could not be recorded", "operation", operation.ID, "error_category", errorCategory, "error_type", errorType)
	}
}

var managedPlatformSafeCategories = map[string]struct{}{
	"capacity_admission": {}, "neon_snapshot_mismatch": {}, "neon_snapshot_missing": {},
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
