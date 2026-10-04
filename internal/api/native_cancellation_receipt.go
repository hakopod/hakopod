//go:build hakopod_native_acceptance && linux

package api

import (
	"context"
	"net/http"
	"reflect"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/nativeacceptance"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/hakopod/hakopod/internal/store"
)

func nativeCancellationAdmission(p store.Principal, op platformbackup.Operation, source, target store.ManagedPlatform) error {
	if !p.AllowsManagedPlatform(op.Project, op.Environment, true) {
		return store.ErrForbidden
	}
	if op.Kind != "restore" || op.Status != "cancelled" || !op.CancelRequested || op.CleanupRequired || op.TargetPlatformID == "" || op.SourcePlatformID == op.TargetPlatformID || op.Lease != "" {
		return store.ErrConflict
	}
	if nativeRecoveryPlatformAdmission(p, op, source, false) != nil || target.ID != op.TargetPlatformID || target.Revision != op.ExpectedTargetRevision || target.Project != op.Project || target.Environment != op.Environment || target.Spec.Kind != "neon" || target.DeletedAt != nil || target.Status != "failed" || target.Observation["phase"] != "recovery-isolated" || target.Observation["recovery_operation_id"] != op.ID {
		return store.ErrConflict
	}
	return nil
}

// The receipt measures a completed cancellation against its immutable restore
// binding. Provider access remains read-only and shares the one-probe limit.
func (s *Server) nativeNeonCancellationReceipt(w http.ResponseWriter, r *http.Request) {
	if nativeacceptance.KubernetesConfig() == nil || s.Store == nil || s.Cluster == nil {
		problem(w, 503, "native_probe_unavailable", "native cancellation receipts require an admitted development run")
		return
	}
	release, acquired := s.acquireNativeProbe()
	if !acquired {
		problem(w, 503, "busy", "a native provider probe is already running")
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	p := who(r)
	op, err := s.Store.PlatformRecoveryOperation(ctx, p, r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	if !p.AllowsManagedPlatform(op.Project, op.Environment, true) {
		failure(w, store.ErrForbidden)
		return
	}
	source, sourceErr := s.Store.ManagedPlatform(ctx, p, op.SourcePlatformID, true)
	target, targetErr := s.Store.ManagedPlatform(ctx, p, op.TargetPlatformID, true)
	if sourceErr != nil || targetErr != nil || nativeCancellationAdmission(p, op, source, target) != nil {
		failure(w, store.ErrConflict)
		return
	}
	if err = nativeacceptance.Recheck(ctx, op.Project, op.Environment, "neon"); err != nil {
		problem(w, 503, "native_probe_unavailable", "the admitted development run changed or expired")
		return
	}
	receipt, err := s.Store.NativeNeonCancellationReceipt(ctx, op.ID, op.Project, op.Environment)
	if err != nil {
		problem(w, 503, "native_probe_failed", "the cancellation cleanup facts are unavailable")
		return
	}
	accepted, err := s.Store.ManagedPlatformRecoverySnapshot(ctx, target.ID, target.Revision)
	if err != nil || nativeProbeSnapshotAdmission(target, accepted) != nil {
		failure(w, store.ErrConflict)
		return
	}
	snapshot, err := cluster.OpenManagedPlatformSnapshot(s.authEncryptionKey(), accepted)
	if err != nil || snapshot.Neon == nil {
		problem(w, 503, "native_probe_unavailable", "the accepted provider snapshot is unavailable")
		return
	}
	request := *snapshot.Neon
	request.Operation = accepted
	observed, err := s.Cluster.ObserveNativeNeonCancellation(ctx, receipt, target, request, s.authEncryptionKey())
	if err != nil {
		problem(w, 503, "native_probe_failed", "the cancellation cleanup facts could not be established")
		return
	}
	opAfter, opErr := s.Store.PlatformRecoveryOperation(ctx, p, op.ID)
	sourceAfter, sourceErr := s.Store.ManagedPlatform(ctx, p, source.ID, true)
	targetAfter, targetErr := s.Store.ManagedPlatform(ctx, p, target.ID, true)
	receiptAfter, receiptErr := s.Store.NativeNeonCancellationReceipt(ctx, op.ID, op.Project, op.Environment)
	acceptedAfter, acceptedErr := s.Store.ManagedPlatformRecoverySnapshot(ctx, target.ID, target.Revision)
	if opErr != nil || sourceErr != nil || targetErr != nil || receiptErr != nil || acceptedErr != nil || nativeCancellationAdmission(p, opAfter, sourceAfter, targetAfter) != nil || !reflect.DeepEqual(opAfter, op) || !reflect.DeepEqual(receiptAfter, receipt) || !reflect.DeepEqual(acceptedAfter, accepted) || !reflect.DeepEqual(targetAfter.Spec, target.Spec) || !reflect.DeepEqual(sourceAfter.Spec, source.Spec) {
		failure(w, store.ErrConflict)
		return
	}
	if err = nativeacceptance.Recheck(ctx, op.Project, op.Environment, "neon"); err != nil {
		problem(w, 503, "native_probe_unavailable", "the admitted development run changed or expired")
		return
	}
	write(w, http.StatusOK, observed)
}
