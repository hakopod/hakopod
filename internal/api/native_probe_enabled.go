//go:build hakopod_native_acceptance && linux

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/nativeacceptance"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) registerNativeProbeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/managed-platforms/{id}/native-probe", s.probeNativeNeon)
	mux.HandleFunc("GET /api/v1/managed-platform-recovery-operations/{id}/native-receipt", s.nativeNeonRecoveryReceipt)
	mux.HandleFunc("GET /api/v1/managed-platform-recovery-operations/{id}/native-cancellation-receipt", s.nativeNeonCancellationReceipt)
}

func nativeProbeAdmission(p store.Principal, item store.ManagedPlatform, expectedRevision int64) error {
	if expectedRevision < 1 {
		return store.ErrInput
	}
	if !p.AllowsManagedPlatform(item.Project, item.Environment, true) {
		return store.ErrForbidden
	}
	if item.DeletedAt != nil || item.Spec.Kind != "neon" || item.Revision != expectedRevision || item.Status != "ready" {
		return store.ErrConflict
	}
	return nil
}

func nativeProbeSnapshotAdmission(item store.ManagedPlatform, op store.ManagedPlatformOperation) error {
	if op.PlatformID != item.ID || op.Revision != item.Revision || op.Status != "succeeded" || op.Kind != "create" && op.Kind != "update" {
		return store.ErrConflict
	}
	return nil
}

func (s *Server) acquireNativeProbe() (func(), bool) {
	select {
	case s.nativeProbes <- struct{}{}:
		return func() { <-s.nativeProbes }, true
	default:
		return nil, false
	}
}

// Probe credentials stay inside the reviewed server snapshot. The separate
// development binary returns measured facts for its exact admitted platform.
func (s *Server) probeNativeNeon(w http.ResponseWriter, r *http.Request) {
	if nativeacceptance.KubernetesConfig() == nil || s.Store == nil || s.Cluster == nil {
		problem(w, 503, "native_probe_unavailable", "native provider probes require an admitted development run")
		return
	}
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision < 1 {
		failure(w, store.ErrInput)
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
	item, err := s.Store.ManagedPlatform(ctx, p, r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	if err = nativeProbeAdmission(p, item, in.ExpectedRevision); err != nil {
		failure(w, err)
		return
	}
	if err = nativeacceptance.Recheck(ctx, item.Project, item.Environment, item.Spec.Kind); err != nil {
		problem(w, 503, "native_probe_unavailable", "the admitted development run changed or expired")
		return
	}
	op, err := s.Store.ManagedPlatformRecoverySnapshot(ctx, item.ID, item.Revision)
	if err != nil || nativeProbeSnapshotAdmission(item, op) != nil {
		failure(w, store.ErrConflict)
		return
	}
	snapshot, err := cluster.OpenManagedPlatformSnapshot(s.authEncryptionKey(), op)
	if err != nil || snapshot.Neon == nil {
		problem(w, 503, "native_probe_unavailable", "the accepted provider snapshot is unavailable")
		return
	}
	request := *snapshot.Neon
	request.Operation = op
	observed, err := s.Cluster.ProbeNeonNative(ctx, s.Store, op, request, s.authEncryptionKey())
	if err != nil {
		problem(w, 503, "native_probe_failed", "the provider probe could not establish the required facts")
		return
	}
	current, err := s.Store.ManagedPlatform(ctx, p, item.ID, true)
	if err != nil || current.ID != item.ID || current.Project != item.Project || current.Environment != item.Environment || nativeProbeAdmission(p, current, item.Revision) != nil {
		failure(w, store.ErrConflict)
		return
	}
	if err = nativeacceptance.Recheck(ctx, current.Project, current.Environment, current.Spec.Kind); err != nil {
		problem(w, 503, "native_probe_unavailable", "the admitted development run changed or expired")
		return
	}
	write(w, http.StatusOK, observed)
}
