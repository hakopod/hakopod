//go:build hakopod_native_acceptance && linux

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/jackc/pgx/v5"
)

var ErrNativeCancellationNeverStarted = errors.New("cancelled recovery never started provider cleanup")

type NativeCancellationWorkload struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type NativeNeonCancellationReceipt struct {
	SchemaVersion             int                          `json:"schema_version"`
	OperationID               string                       `json:"operation_id"`
	Project                   string                       `json:"project"`
	Environment               string                       `json:"environment"`
	SourcePlatformID          string                       `json:"source_platform_id"`
	SourceRevision            int64                        `json:"source_revision"`
	TargetPlatformID          string                       `json:"target_platform_id"`
	TargetRevision            int64                        `json:"target_revision"`
	ArtifactID                string                       `json:"artifact_id"`
	ManifestSHA256            string                       `json:"manifest_sha256"`
	TenantID                  string                       `json:"tenant_id"`
	TimelineID                string                       `json:"timeline_id"`
	TenantGeneration          int64                        `json:"tenant_generation"`
	TimelineGeneration        int64                        `json:"timeline_generation"`
	JournalEntries            int                          `json:"journal_entries"`
	JournalPhaseCounts        map[string]int               `json:"journal_phase_counts"`
	CleanupPending            bool                         `json:"cleanup_pending"`
	OperationAuthorityRefused bool                         `json:"operation_authority_refused"`
	NamespaceUID              string                       `json:"namespace_uid"`
	Workloads                 []NativeCancellationWorkload `json:"workloads"`
	StagingPrefix             string                       `json:"-"`
}

func validateNativeCancellationJournal(resources []NeonRecoveryResource) (map[string]int, error) {
	if len(resources) == 0 {
		return nil, ErrNativeCancellationNeverStarted
	}
	if len(resources) > MaxManagedPlatformResources {
		return nil, fmt.Errorf("native cancellation journal exceeds its bound")
	}
	counts := map[string]int{"complete": 0, "empty_complete": 0, "untouched_complete": 0}
	for _, resource := range resources {
		if _, ok := counts[resource.Phase]; !ok {
			return nil, fmt.Errorf("native cancellation cleanup journal is not terminal")
		}
		counts[resource.Phase]++
	}
	return counts, nil
}

// nativeCancellationMutationAuthority shares the current credential check
// used by recovery workers, then proves without heartbeating or extending a
// lease that the exact operation has no live mutation authority.
func (s *Store) nativeCancellationMutationAuthority(ctx context.Context, op platformbackup.Operation) error {
	credentialErr := s.ReauthorizePlatformRecovery(ctx, op)
	if credentialErr != nil {
		return credentialErr
	}
	var active bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_platform_recovery_operations
		WHERE id=$1 AND kind=$2 AND project=$3 AND environment=$4 AND source_platform_id=$5 AND target_platform_id=$6
		AND expected_source_revision=$7 AND expected_target_revision=$8 AND artifact_id=$9
		AND status='running' AND lease<>'' AND lease_until>=clock_timestamp() AND cleanup_required=false)`,
		op.ID, op.Kind, op.Project, op.Environment, op.SourcePlatformID, op.TargetPlatformID,
		op.ExpectedSourceRevision, op.ExpectedTargetRevision, op.ArtifactID).Scan(&active)
	if err != nil {
		return err
	}
	if active {
		return nil
	}
	return ErrConflict
}

// NativeNeonCancellationReceipt reads a completed cancellation only. It does
// not acquire a lease or restore authority, and it refuses queued cancellations
// which never created a provider cleanup journal.
func (s *Store) NativeNeonCancellationReceipt(ctx context.Context, operationID, project, environment string) (NativeNeonCancellationReceipt, error) {
	var receipt NativeNeonCancellationReceipt
	if operationID == "" || project == "" || environment == "" {
		return receipt, ErrInput
	}
	var op platformbackup.Operation
	var binding NeonRecoveryBinding
	err := s.Pool.QueryRow(ctx, `SELECT `+platformRecoveryQualifiedColumns+`
		FROM managed_platform_recovery_operations o
		JOIN managed_platforms source ON source.id=o.source_platform_id AND source.revision=o.expected_source_revision AND source.project=o.project AND source.environment=o.environment AND source.kind='neon' AND source.deleted_at IS NULL
		JOIN managed_platforms target ON target.id=o.target_platform_id AND target.revision=o.expected_target_revision AND target.project=o.project AND target.environment=o.environment AND target.kind='neon' AND target.deleted_at IS NULL
		JOIN managed_platform_recovery_artifacts artifact ON artifact.id=o.artifact_id AND artifact.source_platform_id=o.source_platform_id AND artifact.source_revision=o.expected_source_revision AND artifact.published_at IS NOT NULL AND artifact.deleted_at IS NULL
		WHERE o.id=$1 AND o.project=$2 AND o.environment=$3 AND o.kind='restore' AND o.status='cancelled' AND o.cancel_requested=true AND o.cleanup_required=false AND o.lease='' AND o.lease_until IS NULL`, operationID, project, environment).Scan(
		&op.ID, &op.Kind, &op.Project, &op.Environment, &op.SourcePlatformID, &op.TargetPlatformID, &op.ArtifactID, &op.ResultArtifactID, &op.DestinationID, &op.DestinationRevision, &op.ExpectedSourceRevision, &op.ExpectedTargetRevision, &op.Status, &op.Phase, &op.Message, &op.AuthorityFingerprint, &op.Lease, &op.CancelRequested, &op.CleanupRequired)
	if errors.Is(err, pgx.ErrNoRows) {
		return receipt, ErrConflict
	}
	if err != nil {
		return receipt, err
	}
	err = s.Pool.QueryRow(ctx, `SELECT operation_id,target_platform_id,target_revision,artifact_id,manifest_sha256,tenant_id,timeline_id,tenant_generation,timeline_generation,staging_prefix
		FROM managed_platform_neon_recovery_bindings WHERE operation_id=$1 AND target_platform_id=$2 AND target_revision=$3 AND artifact_id=$4
		AND manifest_sha256=(SELECT manifest_sha256 FROM managed_platform_recovery_artifacts WHERE id=$4 AND source_platform_id=$5 AND source_revision=$6 AND published_at IS NOT NULL AND deleted_at IS NULL)`, op.ID, op.TargetPlatformID, op.ExpectedTargetRevision, op.ArtifactID, op.SourcePlatformID, op.ExpectedSourceRevision).Scan(
		&binding.OperationID, &binding.TargetPlatformID, &binding.TargetRevision, &binding.ArtifactID, &binding.ManifestSHA256, &binding.TenantID, &binding.TimelineID, &binding.TenantGeneration, &binding.TimelineGeneration, &binding.StagingPrefix)
	if errors.Is(err, pgx.ErrNoRows) {
		return receipt, ErrNativeCancellationNeverStarted
	}
	if err != nil {
		return receipt, err
	}
	var targetStatus, targetPhase, targetRecoveryOperation string
	err = s.Pool.QueryRow(ctx, `SELECT status,COALESCE(observation->>'phase',''),COALESCE(observation->>'recovery_operation_id','') FROM managed_platforms WHERE id=$1 AND revision=$2 AND deleted_at IS NULL`, op.TargetPlatformID, op.ExpectedTargetRevision).Scan(&targetStatus, &targetPhase, &targetRecoveryOperation)
	if err != nil || targetStatus != "failed" || targetPhase != "recovery-isolated" || targetRecoveryOperation != op.ID {
		if err != nil {
			return receipt, err
		}
		return receipt, ErrConflict
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+neonRecoveryResourceColumns+` FROM platform_component_recovery_overrides
		WHERE recovery_operation_id=$1 AND platform_id=$2 AND platform_revision=$3 AND artifact_id=$4 AND manifest_sha256=$5
		ORDER BY component,resource_kind LIMIT $6`, op.ID, binding.TargetPlatformID, binding.TargetRevision, binding.ArtifactID, binding.ManifestSHA256, MaxManagedPlatformResources+1)
	if err != nil {
		return receipt, err
	}
	resources := make([]NeonRecoveryResource, 0, MaxManagedPlatformResources)
	for rows.Next() {
		resource, scanErr := scanNeonRecoveryResource(rows)
		if scanErr != nil {
			rows.Close()
			return receipt, scanErr
		}
		resources = append(resources, resource)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return receipt, err
	}
	rows.Close()
	counts, err := validateNativeCancellationJournal(resources)
	if err != nil {
		return receipt, err
	}
	var namespaceUID string
	err = s.Pool.QueryRow(ctx, `SELECT resource_id FROM platform_component_resources WHERE platform_id=$1 AND platform_revision=$2 AND component=$3 AND resource_kind='runtime_component'
		AND owner_operation_id=(SELECT id FROM managed_platform_operations WHERE platform_id=$1 AND revision=$2 AND status='succeeded' ORDER BY finished_at DESC,id DESC LIMIT 1)
		AND released_at IS NULL`, op.TargetPlatformID, op.ExpectedTargetRevision, "namespace.managed-platform-"+op.TargetPlatformID).Scan(&namespaceUID)
	if err != nil {
		return receipt, err
	}
	workloadRows, err := s.Pool.Query(ctx, `SELECT workload_kind,deployment_name,deployment_uid,target_replicas
		FROM managed_platform_recovery_deployments WHERE operation_id=$1 AND platform_id=$2
		ORDER BY workload_kind,deployment_name LIMIT $3`, op.ID, op.TargetPlatformID, MaxManagedPlatformResources+1)
	if err != nil {
		return receipt, err
	}
	workloads := make([]NativeCancellationWorkload, 0, MaxManagedPlatformResources)
	for workloadRows.Next() {
		var workload NativeCancellationWorkload
		var targetReplicas int32
		if err = workloadRows.Scan(&workload.Kind, &workload.Name, &workload.UID, &targetReplicas); err != nil {
			workloadRows.Close()
			return receipt, err
		}
		if workload.UID == "" || workload.Name == "" || targetReplicas != 0 || workload.Kind != "deployment" && workload.Kind != "statefulset" {
			workloadRows.Close()
			return receipt, ErrConflict
		}
		workloads = append(workloads, workload)
	}
	if err = workloadRows.Err(); err != nil {
		workloadRows.Close()
		return receipt, err
	}
	workloadRows.Close()
	if len(workloads) == 0 || len(workloads) > MaxManagedPlatformResources {
		return receipt, ErrConflict
	}
	authorityErr := s.nativeCancellationMutationAuthority(ctx, op)
	if authorityErr == nil {
		return receipt, fmt.Errorf("%w: cancelled recovery still has mutation authority", ErrConflict)
	}
	if !errors.Is(authorityErr, ErrConflict) {
		return receipt, authorityErr
	}
	if !strings.HasSuffix(binding.StagingPrefix, "/") || len(binding.StagingPrefix) > 512 {
		return receipt, ErrConflict
	}
	return NativeNeonCancellationReceipt{SchemaVersion: 1, OperationID: op.ID, Project: op.Project, Environment: op.Environment,
		SourcePlatformID: op.SourcePlatformID, SourceRevision: op.ExpectedSourceRevision, TargetPlatformID: op.TargetPlatformID,
		TargetRevision: op.ExpectedTargetRevision, ArtifactID: op.ArtifactID, ManifestSHA256: binding.ManifestSHA256,
		TenantID: binding.TenantID, TimelineID: binding.TimelineID, TenantGeneration: binding.TenantGeneration,
		TimelineGeneration: binding.TimelineGeneration, JournalEntries: len(resources), JournalPhaseCounts: counts,
		CleanupPending: false, OperationAuthorityRefused: true, NamespaceUID: namespaceUID, Workloads: workloads,
		StagingPrefix: binding.StagingPrefix}, nil
}
