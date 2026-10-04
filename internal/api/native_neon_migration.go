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

// This route exists only in the admitted development acceptance binary.
func (s *Server) migrateNativeNeon(w http.ResponseWriter, r *http.Request) {
	if nativeacceptance.KubernetesConfig() == nil || s.Store == nil || s.Cluster == nil {
		problem(w, 503, "native_probe_unavailable", "native migration requires an admitted development run")
		return
	}
	var in struct {
		ExpectedRevision  int64  `json:"expected_revision"`
		NamespaceUID      string `json:"namespace_uid"`
		SourceNodeID      int64  `json:"source_node_id"`
		DestinationNodeID int64  `json:"destination_node_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision < 1 || len(in.NamespaceUID) < 1 || len(in.NamespaceUID) > 128 || in.SourceNodeID < 1 || in.SourceNodeID > 8 || in.DestinationNodeID < 1 || in.DestinationNodeID > 8 || in.SourceNodeID == in.DestinationNodeID {
		failure(w, store.ErrInput)
		return
	}
	release, acquired := s.acquireNativeProbe()
	if !acquired {
		problem(w, 503, "busy", "a native provider probe is already running")
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
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
	observed, err := s.Cluster.MigrateNeonNative(ctx, s.Store, op, request, s.authEncryptionKey(), in.NamespaceUID, in.SourceNodeID, in.DestinationNodeID)
	if err != nil {
		problem(w, 503, "native_migration_failed", "the owned tenant migration could not establish the required facts")
		return
	}
	if err = nativeacceptance.Recheck(ctx, item.Project, item.Environment, item.Spec.Kind); err != nil {
		problem(w, 503, "native_probe_unavailable", "the admitted development run changed or expired")
		return
	}
	write(w, http.StatusOK, observed)
}
