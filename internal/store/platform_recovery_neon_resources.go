package store

import (
	"bytes"
	"context"
	"errors"
	"regexp"

	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/jackc/pgx/v5"
)

var neonRecoveryTransitionToken = regexp.MustCompile(`^[0-9a-f]{64}$`)

type NeonRecoveryResource struct {
	OperationID, TargetPlatformID, ArtifactID, ManifestSHA256             string
	Component, Kind, PriorResourceID, PriorOwnerOperationID               string
	ReplacementExternalKey, ReplacementResourceID, Phase, TransitionToken string
	TargetRevision, PriorGeneration, ReplacementGeneration                int64
}

const neonRecoveryResourceColumns = `recovery_operation_id,platform_id,platform_revision,artifact_id,manifest_sha256,component,resource_kind,prior_resource_id,prior_generation,prior_owner_operation_id,replacement_external_key,COALESCE(replacement_resource_id,''),COALESCE(replacement_generation,0),phase,transition_token`
const platformRecoveryQualifiedColumns = `o.id,o.kind,o.project,o.environment,o.source_platform_id,COALESCE(o.target_platform_id,''),COALESCE(o.artifact_id,''),COALESCE(o.result_artifact_id,''),COALESCE(o.destination_id,''),COALESCE(o.destination_revision,0),o.expected_source_revision,COALESCE(o.expected_target_revision,0),o.status,o.phase,o.message,o.authority_fingerprint,o.lease,o.cancel_requested,o.cleanup_required`

func scanNeonRecoveryResource(row pgx.Row) (NeonRecoveryResource, error) {
	var resource NeonRecoveryResource
	err := row.Scan(&resource.OperationID, &resource.TargetPlatformID, &resource.TargetRevision, &resource.ArtifactID, &resource.ManifestSHA256, &resource.Component, &resource.Kind, &resource.PriorResourceID, &resource.PriorGeneration, &resource.PriorOwnerOperationID, &resource.ReplacementExternalKey, &resource.ReplacementResourceID, &resource.ReplacementGeneration, &resource.Phase, &resource.TransitionToken)
	return resource, err
}

func validNeonRecoveryResourceInput(component, kind, externalKey, token string) bool {
	if !managedPlatformComponent.MatchString(component) || len(externalKey) < 1 || len(externalKey) > 255 || !neonRecoveryTransitionToken.MatchString(token) {
		return false
	}
	return kind == "neon_tenant" || kind == "neon_timeline" || kind == "runtime_component"
}

// neonRecoveryFenceTx binds every mutation to the reviewed restore, its live
// lease, current authority, exact target revision, artifact and immutable
// manifest binding. Cancellation stops new provider effects immediately.
func (s *Store) neonRecoveryFenceTx(ctx context.Context, tx pgx.Tx, supplied platformbackup.Operation, allowCancelled bool) (NeonRecoveryBinding, error) {
	if supplied.Kind != "restore" || supplied.ID == "" || supplied.TargetPlatformID == "" || supplied.ExpectedTargetRevision < 1 || supplied.ArtifactID == "" || supplied.Lease == "" {
		return NeonRecoveryBinding{}, ErrInput
	}
	var stored platformbackup.Operation
	var binding NeonRecoveryBinding
	err := tx.QueryRow(ctx, `SELECT `+platformRecoveryQualifiedColumns+`,b.operation_id,b.target_platform_id,b.target_revision,b.artifact_id,b.manifest_sha256,b.tenant_id,b.timeline_id,b.tenant_generation,b.timeline_generation,b.staging_prefix
		FROM managed_platform_recovery_operations o
		JOIN managed_platforms p ON p.id=o.target_platform_id AND p.revision=o.expected_target_revision AND p.kind='neon' AND p.deleted_at IS NULL
		JOIN managed_platform_neon_recovery_bindings b ON b.operation_id=o.id AND b.target_platform_id=o.target_platform_id AND b.target_revision=o.expected_target_revision AND b.artifact_id=o.artifact_id
		WHERE o.id=$1 AND o.kind='restore' AND o.target_platform_id=$2 AND o.expected_target_revision=$3 AND o.artifact_id=$4
		AND o.status='running' AND o.lease=$5 AND o.lease_until>=clock_timestamp() AND (o.cancel_requested=false OR $6)
		FOR UPDATE OF o,p`, supplied.ID, supplied.TargetPlatformID, supplied.ExpectedTargetRevision, supplied.ArtifactID, supplied.Lease, allowCancelled).Scan(
		&stored.ID, &stored.Kind, &stored.Project, &stored.Environment, &stored.SourcePlatformID, &stored.TargetPlatformID, &stored.ArtifactID, &stored.ResultArtifactID, &stored.DestinationID, &stored.DestinationRevision, &stored.ExpectedSourceRevision, &stored.ExpectedTargetRevision, &stored.Status, &stored.Phase, &stored.Message, &stored.AuthorityFingerprint, &stored.Lease, &stored.CancelRequested, &stored.CleanupRequired,
		&binding.OperationID, &binding.TargetPlatformID, &binding.TargetRevision, &binding.ArtifactID, &binding.ManifestSHA256, &binding.TenantID, &binding.TimelineID, &binding.TenantGeneration, &binding.TimelineGeneration, &binding.StagingPrefix)
	if errors.Is(err, pgx.ErrNoRows) {
		return NeonRecoveryBinding{}, ErrConflict
	}
	if err != nil {
		return NeonRecoveryBinding{}, err
	}
	if stored.Kind != supplied.Kind || stored.SourcePlatformID != supplied.SourcePlatformID || stored.TargetPlatformID != supplied.TargetPlatformID || stored.ArtifactID != supplied.ArtifactID || stored.ExpectedSourceRevision != supplied.ExpectedSourceRevision || stored.ExpectedTargetRevision != supplied.ExpectedTargetRevision || stored.Lease != supplied.Lease || !bytes.Equal(stored.AuthorityFingerprint, supplied.AuthorityFingerprint) {
		return NeonRecoveryBinding{}, ErrConflict
	}
	if !allowCancelled {
		if err = s.reauthorizePlatformRecoveryTx(ctx, tx, stored); err != nil {
			return NeonRecoveryBinding{}, err
		}
	}
	return binding, nil
}

func (s *Store) PlanNeonRecoveryResource(ctx context.Context, op platformbackup.Operation, binding NeonRecoveryBinding, currentClaim PlatformResourceClaim, newExternalKey, token string) (NeonRecoveryResource, error) {
	if currentClaim.PlatformID != op.TargetPlatformID || currentClaim.PlatformRevision != op.ExpectedTargetRevision || currentClaim.OwnerOperationID == "" || currentClaim.ResourceID == "" || currentClaim.ImmutableGeneration < 1 || !validNeonRecoveryResourceInput(currentClaim.Component, currentClaim.Kind, newExternalKey, token) {
		return NeonRecoveryResource{}, ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	defer tx.Rollback(ctx)
	storedBinding, err := s.neonRecoveryFenceTx(ctx, tx, op, false)
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	if binding != storedBinding {
		return NeonRecoveryResource{}, ErrConflict
	}
	var exact bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_component_resources WHERE platform_id=$1 AND platform_revision=$2 AND component=$3 AND resource_kind=$4 AND resource_id=$5 AND immutable_generation=$6 AND owner_operation_id=$7 AND released_at IS NULL)`, currentClaim.PlatformID, currentClaim.PlatformRevision, currentClaim.Component, currentClaim.Kind, currentClaim.ResourceID, currentClaim.ImmutableGeneration, currentClaim.OwnerOperationID).Scan(&exact); err != nil {
		return NeonRecoveryResource{}, err
	}
	if !exact {
		return NeonRecoveryResource{}, ErrConflict
	}
	resource, err := scanNeonRecoveryResource(tx.QueryRow(ctx, `INSERT INTO platform_component_recovery_overrides(platform_id,platform_revision,component,resource_kind,recovery_operation_id,artifact_id,manifest_sha256,prior_resource_id,prior_generation,prior_owner_operation_id,replacement_external_key,transition_token,phase)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'planned') ON CONFLICT(platform_id,platform_revision,component,resource_kind) DO NOTHING RETURNING `+neonRecoveryResourceColumns,
		currentClaim.PlatformID, currentClaim.PlatformRevision, currentClaim.Component, currentClaim.Kind, op.ID, binding.ArtifactID, binding.ManifestSHA256, currentClaim.ResourceID, currentClaim.ImmutableGeneration, currentClaim.OwnerOperationID, newExternalKey, token))
	if errors.Is(err, pgx.ErrNoRows) {
		resource, err = scanNeonRecoveryResource(tx.QueryRow(ctx, "SELECT "+neonRecoveryResourceColumns+" FROM platform_component_recovery_overrides WHERE platform_id=$1 AND platform_revision=$2 AND component=$3 AND resource_kind=$4 FOR UPDATE", currentClaim.PlatformID, currentClaim.PlatformRevision, currentClaim.Component, currentClaim.Kind))
	}
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	if resource.OperationID != op.ID || resource.ArtifactID != binding.ArtifactID || resource.ManifestSHA256 != binding.ManifestSHA256 || resource.PriorResourceID != currentClaim.ResourceID || resource.PriorGeneration != currentClaim.ImmutableGeneration || resource.PriorOwnerOperationID != currentClaim.OwnerOperationID || resource.ReplacementExternalKey != newExternalKey || resource.TransitionToken != token {
		return NeonRecoveryResource{}, ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return NeonRecoveryResource{}, err
	}
	return resource, nil
}

func (s *Store) transitionNeonRecoveryResource(ctx context.Context, op platformbackup.Operation, component, token, fromPhase, toPhase, extraPredicate, assignments string, args ...any) (NeonRecoveryResource, error) {
	if !managedPlatformComponent.MatchString(component) || !neonRecoveryTransitionToken.MatchString(token) {
		return NeonRecoveryResource{}, ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	defer tx.Rollback(ctx)
	cleanup := platformbackup.RecoveryCleanupFromContext(ctx) && (toPhase == "prior_released" || toPhase == "replacement_released" || toPhase == "complete")
	binding, err := s.neonRecoveryFenceTx(ctx, tx, op, cleanup)
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	queryArgs := []any{op.ID, component, token, binding.TargetPlatformID, binding.TargetRevision, binding.ArtifactID, binding.ManifestSHA256, fromPhase, toPhase}
	queryArgs = append(queryArgs, args...)
	query := `UPDATE platform_component_recovery_overrides SET phase=$9,updated_at=now()` + assignments + ` WHERE recovery_operation_id=$1 AND component=$2 AND transition_token=$3 AND platform_id=$4 AND platform_revision=$5 AND artifact_id=$6 AND manifest_sha256=$7 AND phase=$8` + extraPredicate + ` RETURNING ` + neonRecoveryResourceColumns
	resource, err := scanNeonRecoveryResource(tx.QueryRow(ctx, query, queryArgs...))
	if errors.Is(err, pgx.ErrNoRows) {
		resource, err = scanNeonRecoveryResource(tx.QueryRow(ctx, "SELECT "+neonRecoveryResourceColumns+" FROM platform_component_recovery_overrides WHERE recovery_operation_id=$1 AND component=$2 AND transition_token=$3 AND platform_id=$4 AND platform_revision=$5 AND artifact_id=$6 AND manifest_sha256=$7"+extraPredicate+" FOR UPDATE", queryArgs...))
		if err == nil && resource.Phase != toPhase {
			return NeonRecoveryResource{}, ErrConflict
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return NeonRecoveryResource{}, ErrConflict
	}
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return NeonRecoveryResource{}, err
	}
	return resource, nil
}

func (s *Store) MarkNeonRecoveryPriorReleased(ctx context.Context, op platformbackup.Operation, component, priorID string, priorGen int64, token string) (NeonRecoveryResource, error) {
	if priorID == "" || priorGen < 1 {
		return NeonRecoveryResource{}, ErrInput
	}
	resource, err := s.transitionNeonRecoveryResource(ctx, op, component, token, "planned", "prior_released", ` AND prior_resource_id=$10 AND prior_generation=$11`, `,prior_released_at=COALESCE(prior_released_at,now())`, priorID, priorGen)
	if err == nil && (resource.PriorResourceID != priorID || resource.PriorGeneration != priorGen) {
		err = ErrConflict
	}
	return resource, err
}

func (s *Store) ReserveNeonRecoveryReplacement(ctx context.Context, op platformbackup.Operation, component, kind, externalKey, token string) (NeonRecoveryResource, error) {
	if !validNeonRecoveryResourceInput(component, kind, externalKey, token) {
		return NeonRecoveryResource{}, ErrInput
	}
	resource, err := s.transitionNeonRecoveryResource(ctx, op, component, token, "prior_released", "reserved", ` AND resource_kind=$10 AND replacement_external_key=$11`, `,reserved_at=COALESCE(reserved_at,now())`, kind, externalKey)
	if err == nil && (resource.Kind != kind || resource.ReplacementExternalKey != externalKey) {
		err = ErrConflict
	}
	return resource, err
}

func (s *Store) ConfirmNeonRecoveryReplacement(ctx context.Context, op platformbackup.Operation, component, providerID string, generation int64, token string) (NeonRecoveryResource, error) {
	if providerID == "" || len(providerID) > 255 || generation < 1 {
		return NeonRecoveryResource{}, ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	defer tx.Rollback(ctx)
	binding, err := s.neonRecoveryFenceTx(ctx, tx, op, false)
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	var kind string
	if err = tx.QueryRow(ctx, `SELECT resource_kind FROM platform_component_recovery_overrides WHERE recovery_operation_id=$1 AND component=$2 AND transition_token=$3 AND platform_id=$4 AND platform_revision=$5 AND artifact_id=$6 AND manifest_sha256=$7 FOR UPDATE`, op.ID, component, token, binding.TargetPlatformID, binding.TargetRevision, binding.ArtifactID, binding.ManifestSHA256).Scan(&kind); errors.Is(err, pgx.ErrNoRows) {
		return NeonRecoveryResource{}, ErrConflict
	}
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,692))", kind+"\x1f"+providerID); err != nil {
		return NeonRecoveryResource{}, err
	}
	var collision bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_component_resources WHERE resource_kind=$1 AND resource_id=$2 AND released_at IS NULL) OR EXISTS(SELECT 1 FROM platform_component_recovery_overrides WHERE resource_kind=$1 AND replacement_resource_id=$2 AND replacement_released_at IS NULL AND adopted_at IS NULL AND NOT (recovery_operation_id=$3 AND component=$4))`, kind, providerID, op.ID, component).Scan(&collision); err != nil {
		return NeonRecoveryResource{}, err
	}
	if collision {
		return NeonRecoveryResource{}, ErrConflict
	}
	resource, err := scanNeonRecoveryResource(tx.QueryRow(ctx, `UPDATE platform_component_recovery_overrides SET phase='confirmed',replacement_resource_id=$8,replacement_generation=$9,confirmed_at=COALESCE(confirmed_at,now()),updated_at=now() WHERE recovery_operation_id=$1 AND component=$2 AND transition_token=$3 AND platform_id=$4 AND platform_revision=$5 AND artifact_id=$6 AND manifest_sha256=$7 AND phase='reserved' AND replacement_resource_id IS NULL AND replacement_generation IS NULL RETURNING `+neonRecoveryResourceColumns, op.ID, component, token, binding.TargetPlatformID, binding.TargetRevision, binding.ArtifactID, binding.ManifestSHA256, providerID, generation))
	if errors.Is(err, pgx.ErrNoRows) {
		resource, err = scanNeonRecoveryResource(tx.QueryRow(ctx, "SELECT "+neonRecoveryResourceColumns+" FROM platform_component_recovery_overrides WHERE recovery_operation_id=$1 AND component=$2 AND transition_token=$3 AND platform_id=$4 AND platform_revision=$5 AND artifact_id=$6 AND manifest_sha256=$7 AND replacement_resource_id=$8 AND replacement_generation=$9 AND phase='confirmed' FOR UPDATE", op.ID, component, token, binding.TargetPlatformID, binding.TargetRevision, binding.ArtifactID, binding.ManifestSHA256, providerID, generation))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return NeonRecoveryResource{}, ErrConflict
	}
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return NeonRecoveryResource{}, err
	}
	if err == nil && (resource.ReplacementResourceID != providerID || resource.ReplacementGeneration != generation) {
		err = ErrConflict
	}
	return resource, err
}

func (s *Store) ConfirmNeonRecoveryCleanupReservation(ctx context.Context, op platformbackup.Operation, expected NeonRecoveryResource, providerID string, generation int64) (NeonRecoveryResource, error) {
	if !platformbackup.RecoveryCleanupFromContext(ctx) || expected.OperationID != op.ID || expected.TargetPlatformID != op.TargetPlatformID || expected.TargetRevision != op.ExpectedTargetRevision || expected.ArtifactID != op.ArtifactID || expected.Phase != "reserved" || !validNeonRecoveryResourceInput(expected.Component, expected.Kind, expected.ReplacementExternalKey, expected.TransitionToken) || providerID == "" || len(providerID) > 255 || generation < 1 {
		return NeonRecoveryResource{}, ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	defer tx.Rollback(ctx)
	binding, err := s.neonRecoveryFenceTx(ctx, tx, op, true)
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	if expected.ManifestSHA256 != binding.ManifestSHA256 {
		return NeonRecoveryResource{}, ErrConflict
	}
	stored, err := scanNeonRecoveryResource(tx.QueryRow(ctx, `SELECT `+neonRecoveryResourceColumns+` FROM platform_component_recovery_overrides WHERE recovery_operation_id=$1 AND platform_id=$2 AND platform_revision=$3 AND artifact_id=$4 AND manifest_sha256=$5 AND component=$6 AND resource_kind=$7 AND prior_resource_id=$8 AND prior_generation=$9 AND prior_owner_operation_id=$10 AND replacement_external_key=$11 AND transition_token=$12 AND phase='reserved' AND replacement_resource_id IS NULL AND replacement_generation IS NULL FOR UPDATE`, expected.OperationID, expected.TargetPlatformID, expected.TargetRevision, expected.ArtifactID, expected.ManifestSHA256, expected.Component, expected.Kind, expected.PriorResourceID, expected.PriorGeneration, expected.PriorOwnerOperationID, expected.ReplacementExternalKey, expected.TransitionToken))
	if errors.Is(err, pgx.ErrNoRows) {
		return NeonRecoveryResource{}, ErrConflict
	}
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	if stored != expected {
		return NeonRecoveryResource{}, ErrConflict
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,692))", expected.Kind+"\x1f"+providerID); err != nil {
		return NeonRecoveryResource{}, err
	}
	var collision bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_component_resources WHERE resource_kind=$1 AND resource_id=$2 AND released_at IS NULL) OR EXISTS(SELECT 1 FROM platform_component_recovery_overrides WHERE resource_kind=$1 AND replacement_resource_id=$2 AND replacement_released_at IS NULL AND adopted_at IS NULL AND NOT (recovery_operation_id=$3 AND component=$4))`, expected.Kind, providerID, op.ID, expected.Component).Scan(&collision); err != nil {
		return NeonRecoveryResource{}, err
	}
	if collision {
		return NeonRecoveryResource{}, ErrConflict
	}
	resource, err := scanNeonRecoveryResource(tx.QueryRow(ctx, `UPDATE platform_component_recovery_overrides SET phase='confirmed',replacement_resource_id=$13,replacement_generation=$14,confirmed_at=COALESCE(confirmed_at,now()),updated_at=now() WHERE recovery_operation_id=$1 AND platform_id=$2 AND platform_revision=$3 AND artifact_id=$4 AND manifest_sha256=$5 AND component=$6 AND resource_kind=$7 AND prior_resource_id=$8 AND prior_generation=$9 AND prior_owner_operation_id=$10 AND replacement_external_key=$11 AND transition_token=$12 AND phase='reserved' AND replacement_resource_id IS NULL AND replacement_generation IS NULL RETURNING `+neonRecoveryResourceColumns, expected.OperationID, expected.TargetPlatformID, expected.TargetRevision, expected.ArtifactID, expected.ManifestSHA256, expected.Component, expected.Kind, expected.PriorResourceID, expected.PriorGeneration, expected.PriorOwnerOperationID, expected.ReplacementExternalKey, expected.TransitionToken, providerID, generation))
	if errors.Is(err, pgx.ErrNoRows) {
		return NeonRecoveryResource{}, ErrConflict
	}
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	return resource, tx.Commit(ctx)
}

func (s *Store) CompleteNeonRecoveryEmptyReservation(ctx context.Context, op platformbackup.Operation, expected NeonRecoveryResource) (NeonRecoveryResource, error) {
	if !platformbackup.RecoveryCleanupFromContext(ctx) || expected.OperationID != op.ID || expected.TargetPlatformID != op.TargetPlatformID || expected.TargetRevision != op.ExpectedTargetRevision || expected.ArtifactID != op.ArtifactID || expected.Phase != "prior_released" && expected.Phase != "reserved" && expected.Phase != "empty_complete" || !validNeonRecoveryResourceInput(expected.Component, expected.Kind, expected.ReplacementExternalKey, expected.TransitionToken) {
		return NeonRecoveryResource{}, ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	defer tx.Rollback(ctx)
	binding, err := s.neonRecoveryFenceTx(ctx, tx, op, true)
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	if expected.ManifestSHA256 != binding.ManifestSHA256 {
		return NeonRecoveryResource{}, ErrConflict
	}
	resource, err := scanNeonRecoveryResource(tx.QueryRow(ctx, `UPDATE platform_component_recovery_overrides SET phase='empty_complete',completed_at=COALESCE(completed_at,now()),updated_at=now() WHERE recovery_operation_id=$1 AND platform_id=$2 AND platform_revision=$3 AND artifact_id=$4 AND manifest_sha256=$5 AND component=$6 AND resource_kind=$7 AND prior_resource_id=$8 AND prior_generation=$9 AND prior_owner_operation_id=$10 AND replacement_external_key=$11 AND transition_token=$12 AND phase=$13 AND replacement_resource_id IS NULL AND replacement_generation IS NULL RETURNING `+neonRecoveryResourceColumns, expected.OperationID, expected.TargetPlatformID, expected.TargetRevision, expected.ArtifactID, expected.ManifestSHA256, expected.Component, expected.Kind, expected.PriorResourceID, expected.PriorGeneration, expected.PriorOwnerOperationID, expected.ReplacementExternalKey, expected.TransitionToken, expected.Phase))
	if errors.Is(err, pgx.ErrNoRows) {
		resource, err = scanNeonRecoveryResource(tx.QueryRow(ctx, `SELECT `+neonRecoveryResourceColumns+` FROM platform_component_recovery_overrides WHERE recovery_operation_id=$1 AND platform_id=$2 AND platform_revision=$3 AND artifact_id=$4 AND manifest_sha256=$5 AND component=$6 AND resource_kind=$7 AND prior_resource_id=$8 AND prior_generation=$9 AND prior_owner_operation_id=$10 AND replacement_external_key=$11 AND transition_token=$12 AND phase='empty_complete' AND replacement_resource_id IS NULL AND replacement_generation IS NULL FOR UPDATE`, expected.OperationID, expected.TargetPlatformID, expected.TargetRevision, expected.ArtifactID, expected.ManifestSHA256, expected.Component, expected.Kind, expected.PriorResourceID, expected.PriorGeneration, expected.PriorOwnerOperationID, expected.ReplacementExternalKey, expected.TransitionToken))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return NeonRecoveryResource{}, ErrConflict
	}
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	return resource, tx.Commit(ctx)
}

func (s *Store) CompleteNeonRecoveryUntouchedResource(ctx context.Context, op platformbackup.Operation, expected NeonRecoveryResource) (NeonRecoveryResource, error) {
	if !platformbackup.RecoveryCleanupFromContext(ctx) || expected.OperationID != op.ID || expected.TargetPlatformID != op.TargetPlatformID || expected.TargetRevision != op.ExpectedTargetRevision || expected.ArtifactID != op.ArtifactID || expected.Phase != "planned" && expected.Phase != "untouched_complete" || !validNeonRecoveryResourceInput(expected.Component, expected.Kind, expected.ReplacementExternalKey, expected.TransitionToken) {
		return NeonRecoveryResource{}, ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	defer tx.Rollback(ctx)
	binding, err := s.neonRecoveryFenceTx(ctx, tx, op, true)
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	if expected.ManifestSHA256 != binding.ManifestSHA256 {
		return NeonRecoveryResource{}, ErrConflict
	}
	resource, err := scanNeonRecoveryResource(tx.QueryRow(ctx, `UPDATE platform_component_recovery_overrides SET phase='untouched_complete',completed_at=COALESCE(completed_at,now()),updated_at=now() WHERE recovery_operation_id=$1 AND platform_id=$2 AND platform_revision=$3 AND artifact_id=$4 AND manifest_sha256=$5 AND component=$6 AND resource_kind=$7 AND prior_resource_id=$8 AND prior_generation=$9 AND prior_owner_operation_id=$10 AND replacement_external_key=$11 AND transition_token=$12 AND phase=$13 AND replacement_resource_id IS NULL AND replacement_generation IS NULL RETURNING `+neonRecoveryResourceColumns, expected.OperationID, expected.TargetPlatformID, expected.TargetRevision, expected.ArtifactID, expected.ManifestSHA256, expected.Component, expected.Kind, expected.PriorResourceID, expected.PriorGeneration, expected.PriorOwnerOperationID, expected.ReplacementExternalKey, expected.TransitionToken, expected.Phase))
	if errors.Is(err, pgx.ErrNoRows) {
		resource, err = scanNeonRecoveryResource(tx.QueryRow(ctx, `SELECT `+neonRecoveryResourceColumns+` FROM platform_component_recovery_overrides WHERE recovery_operation_id=$1 AND platform_id=$2 AND platform_revision=$3 AND artifact_id=$4 AND manifest_sha256=$5 AND component=$6 AND resource_kind=$7 AND prior_resource_id=$8 AND prior_generation=$9 AND prior_owner_operation_id=$10 AND replacement_external_key=$11 AND transition_token=$12 AND phase='untouched_complete' AND replacement_resource_id IS NULL AND replacement_generation IS NULL FOR UPDATE`, expected.OperationID, expected.TargetPlatformID, expected.TargetRevision, expected.ArtifactID, expected.ManifestSHA256, expected.Component, expected.Kind, expected.PriorResourceID, expected.PriorGeneration, expected.PriorOwnerOperationID, expected.ReplacementExternalKey, expected.TransitionToken))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return NeonRecoveryResource{}, ErrConflict
	}
	if err != nil {
		return NeonRecoveryResource{}, err
	}
	return resource, tx.Commit(ctx)
}

func (s *Store) MarkNeonRecoveryReplacementReleased(ctx context.Context, op platformbackup.Operation, component, resourceID string, generation int64, token string) (NeonRecoveryResource, error) {
	if resourceID == "" || generation < 1 {
		return NeonRecoveryResource{}, ErrInput
	}
	resource, err := s.transitionNeonRecoveryResource(ctx, op, component, token, "confirmed", "replacement_released", ` AND replacement_resource_id=$10 AND replacement_generation=$11`, `,replacement_released_at=COALESCE(replacement_released_at,now())`, resourceID, generation)
	if err == nil && (resource.ReplacementResourceID != resourceID || resource.ReplacementGeneration != generation) {
		err = ErrConflict
	}
	return resource, err
}

func (s *Store) CompleteNeonRecoveryResource(ctx context.Context, op platformbackup.Operation, component, token string) (NeonRecoveryResource, error) {
	return s.transitionNeonRecoveryResource(ctx, op, component, token, "replacement_released", "complete", ``, `,completed_at=COALESCE(completed_at,now())`)
}

func (s *Store) NeonRecoveryResources(ctx context.Context, op platformbackup.Operation) ([]NeonRecoveryResource, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	binding, err := s.neonRecoveryFenceTx(ctx, tx, op, platformbackup.RecoveryCleanupFromContext(ctx))
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+neonRecoveryResourceColumns+` FROM platform_component_recovery_overrides WHERE recovery_operation_id=$1 AND platform_id=$2 AND platform_revision=$3 AND artifact_id=$4 AND manifest_sha256=$5 ORDER BY component,resource_kind LIMIT $6`, op.ID, binding.TargetPlatformID, binding.TargetRevision, binding.ArtifactID, binding.ManifestSHA256, MaxManagedPlatformResources+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resources := make([]NeonRecoveryResource, 0, MaxManagedPlatformResources)
	for rows.Next() {
		var r NeonRecoveryResource
		if err = rows.Scan(&r.OperationID, &r.TargetPlatformID, &r.TargetRevision, &r.ArtifactID, &r.ManifestSHA256, &r.Component, &r.Kind, &r.PriorResourceID, &r.PriorGeneration, &r.PriorOwnerOperationID, &r.ReplacementExternalKey, &r.ReplacementResourceID, &r.ReplacementGeneration, &r.Phase, &r.TransitionToken); err != nil {
			return nil, err
		}
		resources = append(resources, r)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(resources) > MaxManagedPlatformResources {
		return nil, ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return resources, nil
}

func (s *Store) EffectiveNeonRecoveryClaims(ctx context.Context, op platformbackup.Operation) (map[string]PlatformResourceClaim, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	binding, err := s.neonRecoveryFenceTx(ctx, tx, op, platformbackup.RecoveryCleanupFromContext(ctx))
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT b.platform_id,b.platform_revision,b.component,b.resource_kind,
		COALESCE(r.replacement_resource_id,b.resource_id),COALESCE(r.replacement_generation,b.immutable_generation),b.owner_operation_id,b.released_at
		FROM platform_component_resources b
		LEFT JOIN platform_component_recovery_overrides r ON r.platform_id=b.platform_id AND r.platform_revision=b.platform_revision AND r.component=b.component AND r.resource_kind=b.resource_kind AND r.recovery_operation_id=$3 AND r.phase='confirmed' AND r.replacement_released_at IS NULL
		WHERE b.platform_id=$1 AND b.platform_revision=$2 AND b.released_at IS NULL
		AND NOT EXISTS(SELECT 1 FROM platform_component_recovery_overrides pending WHERE pending.platform_id=b.platform_id AND pending.platform_revision=b.platform_revision AND pending.component=b.component AND pending.resource_kind=b.resource_kind AND pending.recovery_operation_id=$3 AND pending.phase IN ('prior_released','reserved','replacement_released','complete','empty_complete'))
		ORDER BY b.component,b.resource_kind LIMIT $4`, binding.TargetPlatformID, binding.TargetRevision, op.ID, MaxManagedPlatformResources+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]PlatformResourceClaim)
	for rows.Next() {
		var claim PlatformResourceClaim
		if err = rows.Scan(&claim.PlatformID, &claim.PlatformRevision, &claim.Component, &claim.Kind, &claim.ResourceID, &claim.ImmutableGeneration, &claim.OwnerOperationID, &claim.ReleasedAt); err != nil {
			return nil, err
		}
		if _, exists := out[claim.Component]; exists {
			return nil, ErrConflict
		}
		out[claim.Component] = claim
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > MaxManagedPlatformResources {
		return nil, ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) VerifyNeonRecoveryResource(ctx context.Context, op platformbackup.Operation, claim PlatformResourceClaim) error {
	if claim.PlatformID != op.TargetPlatformID || claim.PlatformRevision != op.ExpectedTargetRevision || claim.ResourceID == "" || claim.ImmutableGeneration < 1 || claim.OwnerOperationID == "" || !managedPlatformComponent.MatchString(claim.Component) {
		return ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	binding, err := s.neonRecoveryFenceTx(ctx, tx, op, platformbackup.RecoveryCleanupFromContext(ctx))
	if err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_component_recovery_overrides r JOIN platform_component_resources b ON b.platform_id=r.platform_id AND b.platform_revision=r.platform_revision AND b.component=r.component AND b.resource_kind=r.resource_kind WHERE r.recovery_operation_id=$1 AND r.platform_id=$2 AND r.platform_revision=$3 AND r.artifact_id=$4 AND r.manifest_sha256=$5 AND r.component=$6 AND r.resource_kind=$7 AND r.replacement_resource_id=$8 AND r.replacement_generation=$9 AND r.prior_owner_operation_id=$10 AND r.phase IN ('confirmed','adopted') AND r.replacement_released_at IS NULL AND b.owner_operation_id=r.prior_owner_operation_id AND b.released_at IS NULL)`, op.ID, binding.TargetPlatformID, binding.TargetRevision, binding.ArtifactID, binding.ManifestSHA256, claim.Component, claim.Kind, claim.ResourceID, claim.ImmutableGeneration, claim.OwnerOperationID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Store) RebindNeonRecoveryProxyEndpoint(ctx context.Context, op platformbackup.Operation, suppliedBinding NeonRecoveryBinding, endpoint NeonProxyEndpointRecord, tenantID, timelineID, computeID string) error {
	if endpoint.EndpointID == "" || !endpoint.Enabled || endpoint.PlatformID != op.TargetPlatformID || endpoint.PlatformRevision != op.ExpectedTargetRevision || endpoint.Generation != op.ExpectedTargetRevision || tenantID == "" || timelineID == "" || computeID == "" || endpoint.ComputeID != computeID {
		return ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	binding, err := s.neonRecoveryFenceTx(ctx, tx, op, false)
	if err != nil {
		return err
	}
	if suppliedBinding != binding || tenantID != binding.TenantID || timelineID != binding.TimelineID {
		return ErrConflict
	}
	var stored NeonProxyEndpointRecord
	err = tx.QueryRow(ctx, `SELECT endpoint_id,platform_id,platform_revision,owner_operation_id,generation,enabled,address,server_name,project_id,branch_id,compute_id,encrypted_roles,updated_at FROM managed_platform_neon_proxy_endpoints WHERE endpoint_id=$1 AND platform_id=$2 AND platform_revision=$3 AND generation=$3 AND enabled FOR UPDATE`, endpoint.EndpointID, binding.TargetPlatformID, binding.TargetRevision).Scan(&stored.EndpointID, &stored.PlatformID, &stored.PlatformRevision, &stored.OwnerOperationID, &stored.Generation, &stored.Enabled, &stored.Address, &stored.ServerName, &stored.ProjectID, &stored.BranchID, &stored.ComputeID, &stored.EncryptedRoles, &stored.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if stored.OwnerOperationID != endpoint.OwnerOperationID || stored.Address != endpoint.Address || stored.ServerName != endpoint.ServerName || stored.ProjectID != endpoint.ProjectID || stored.ComputeID != endpoint.ComputeID || !bytes.Equal(stored.EncryptedRoles, endpoint.EncryptedRoles) || stored.BranchID != endpoint.BranchID && stored.BranchID != timelineID {
		return ErrConflict
	}
	priorBranch := endpoint.BranchID
	if priorBranch == timelineID {
		priorBranch = stored.BranchID
	}
	_, err = tx.Exec(ctx, `INSERT INTO managed_platform_neon_proxy_recovery_rebindings(recovery_operation_id,endpoint_id,platform_id,platform_revision,artifact_id,manifest_sha256,tenant_id,prior_branch_id,replacement_branch_id,compute_id,endpoint_generation) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(recovery_operation_id) DO NOTHING`, op.ID, endpoint.EndpointID, binding.TargetPlatformID, binding.TargetRevision, binding.ArtifactID, binding.ManifestSHA256, tenantID, priorBranch, timelineID, computeID, endpoint.Generation)
	if err != nil {
		return err
	}
	var journalEndpoint, journalPlatform, journalArtifact, journalManifest, journalTenant, journalPrior, journalReplacement, journalCompute string
	var journalRevision, journalGeneration int64
	err = tx.QueryRow(ctx, `SELECT endpoint_id,platform_id,platform_revision,artifact_id,manifest_sha256,tenant_id,prior_branch_id,replacement_branch_id,compute_id,endpoint_generation FROM managed_platform_neon_proxy_recovery_rebindings WHERE recovery_operation_id=$1 FOR SHARE`, op.ID).Scan(&journalEndpoint, &journalPlatform, &journalRevision, &journalArtifact, &journalManifest, &journalTenant, &journalPrior, &journalReplacement, &journalCompute, &journalGeneration)
	if err != nil {
		return err
	}
	if journalEndpoint != endpoint.EndpointID || journalPlatform != binding.TargetPlatformID || journalRevision != binding.TargetRevision || journalArtifact != binding.ArtifactID || journalManifest != binding.ManifestSHA256 || journalTenant != tenantID || journalReplacement != timelineID || journalCompute != computeID || journalGeneration != endpoint.Generation || endpoint.BranchID != journalPrior && endpoint.BranchID != journalReplacement {
		return ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE managed_platform_neon_proxy_endpoints SET branch_id=$2,updated_at=now() WHERE endpoint_id=$1 AND platform_id=$3 AND platform_revision=$4 AND generation=$4 AND enabled AND owner_operation_id=$5 AND address=$6 AND server_name=$7 AND project_id=$8 AND compute_id=$9 AND encrypted_roles=$10 AND branch_id IN ($2,$11)`, endpoint.EndpointID, timelineID, binding.TargetPlatformID, binding.TargetRevision, endpoint.OwnerOperationID, endpoint.Address, endpoint.ServerName, endpoint.ProjectID, computeID, endpoint.EncryptedRoles, journalPrior)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}
