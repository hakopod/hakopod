//go:build hakopod_native_acceptance && linux

package api

import (
	"context"
	"net/http"
	"reflect"
	"time"

	"github.com/hakopod/hakopod/internal/nativeacceptance"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/hakopod/hakopod/internal/store"
)

type nativeRecoveryReceipt struct {
	OperationID        string                      `json:"operation_id"`
	Status             string                      `json:"status"`
	ArtifactID         string                      `json:"artifact_id"`
	ManifestSHA256     string                      `json:"manifest_sha256"`
	SourcePlatformID   string                      `json:"source_platform_id"`
	SourceRevision     int64                       `json:"source_revision"`
	SourceNamespaceUID string                      `json:"source_namespace_uid"`
	TargetPlatformID   string                      `json:"target_platform_id,omitempty"`
	TargetRevision     int64                       `json:"target_revision,omitempty"`
	Format             string                      `json:"format"`
	Parts              []string                    `json:"parts"`
	Neon               platformbackup.NeonIdentity `json:"neon"`
}

// The native receipt exposes selected fields from the published immutable
// artifact. It is not proof of restored SQL data or completed cleanup.
func (s *Server) nativeNeonRecoveryReceipt(w http.ResponseWriter, r *http.Request) {
	if nativeacceptance.KubernetesConfig() == nil || s.Store == nil {
		problem(w, 503, "native_probe_unavailable", "native recovery receipts require an admitted development run")
		return
	}
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
	if op.Status != "succeeded" {
		failure(w, store.ErrConflict)
		return
	}
	if err = nativeacceptance.Recheck(ctx, op.Project, op.Environment, "neon"); err != nil {
		problem(w, 503, "native_probe_unavailable", "the admitted development run changed or expired")
		return
	}
	source, err := s.Store.ManagedPlatform(ctx, p, op.SourcePlatformID, true)
	if err != nil || nativeRecoveryPlatformAdmission(p, op, source, false) != nil {
		failure(w, store.ErrConflict)
		return
	}
	if op.TargetPlatformID != "" {
		target, targetErr := s.Store.ManagedPlatform(ctx, p, op.TargetPlatformID, true)
		if targetErr != nil || nativeRecoveryPlatformAdmission(p, op, target, true) != nil {
			failure(w, store.ErrConflict)
			return
		}
	}
	artifactID := op.ArtifactID
	if op.Kind == "backup" {
		artifactID = op.ResultArtifactID
	}
	artifact, err := s.Store.PlatformRecoveryArtifact(ctx, artifactID)
	if err != nil {
		failure(w, err)
		return
	}
	receipt, err := nativeRecoveryArtifactReceipt(op, artifact)
	if err != nil {
		failure(w, err)
		return
	}
	current, err := s.Store.ManagedPlatform(ctx, p, source.ID, true)
	if err != nil || nativeRecoveryPlatformAdmission(p, op, current, false) != nil {
		failure(w, store.ErrConflict)
		return
	}
	if op.TargetPlatformID != "" {
		target, targetErr := s.Store.ManagedPlatform(ctx, p, op.TargetPlatformID, true)
		if targetErr != nil || nativeRecoveryPlatformAdmission(p, op, target, true) != nil {
			failure(w, store.ErrConflict)
			return
		}
	}
	if err = nativeacceptance.Recheck(ctx, op.Project, op.Environment, "neon"); err != nil {
		problem(w, 503, "native_probe_unavailable", "the admitted development run changed or expired")
		return
	}
	opAfter, operationErr := s.Store.PlatformRecoveryOperation(ctx, p, op.ID)
	artifactAfter, artifactErr := s.Store.PlatformRecoveryArtifact(ctx, artifactID)
	if operationErr != nil || artifactErr != nil || !reflect.DeepEqual(opAfter, op) || !reflect.DeepEqual(artifactAfter, artifact) {
		failure(w, store.ErrConflict)
		return
	}
	write(w, http.StatusOK, receipt)
}

func nativeRecoveryPlatformAdmission(p store.Principal, op platformbackup.Operation, item store.ManagedPlatform, target bool) error {
	id, revision := op.SourcePlatformID, op.ExpectedSourceRevision
	if target {
		if op.Kind != "restore" || op.TargetPlatformID == "" || op.TargetPlatformID == op.SourcePlatformID {
			return store.ErrConflict
		}
		id, revision = op.TargetPlatformID, op.ExpectedTargetRevision
	}
	if item.ID != id || item.Project != op.Project || item.Environment != op.Environment {
		return store.ErrConflict
	}
	return nativeProbeAdmission(p, item, revision)
}

func nativeRecoveryArtifactReceipt(op platformbackup.Operation, artifact platformbackup.StoredArtifact) (nativeRecoveryReceipt, error) {
	var receipt nativeRecoveryReceipt
	m := artifact.Manifest
	expectedArtifact := op.ArtifactID
	if op.Kind == "backup" {
		expectedArtifact = op.ResultArtifactID
	}
	if op.Status != "succeeded" || op.Kind != "backup" && op.Kind != "restore" || expectedArtifact == "" || artifact.ID != expectedArtifact || m.Validate() != nil || m.Neon == nil || m.Format != platformbackup.NeonFormat || m.PlatformID != op.SourcePlatformID || m.PlatformRevision != op.ExpectedSourceRevision || m.ManifestSHA256 != m.Digest() {
		return receipt, store.ErrConflict
	}
	parts := make([]string, len(m.Parts))
	for i, part := range m.Parts {
		parts[i] = part.Name
	}
	return nativeRecoveryReceipt{OperationID: op.ID, Status: op.Status, ArtifactID: artifact.ID, ManifestSHA256: m.ManifestSHA256, SourcePlatformID: m.PlatformID, SourceRevision: m.PlatformRevision, SourceNamespaceUID: m.SourceNamespaceUID, TargetPlatformID: op.TargetPlatformID, TargetRevision: op.ExpectedTargetRevision, Format: m.Format, Parts: parts, Neon: *m.Neon}, nil
}
