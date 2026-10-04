package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/jackc/pgx/v5"
)

type NeonControllerContext struct {
	Operation              ManagedPlatformOperation
	Binding                NeonRecoveryBinding
	Claims                 []PlatformResourceClaim
	PendingCompute         bool
	State                  managedplatform.NeonControllerState
	ObserveTenantPlacement func(PlatformResourceClaim, string, int64) error
}

// WithNeonControllerState serializes notifications for a revision and holds a
// shared platform lock through their bounded apply. A new revision or deletion
// cannot race an already-authorized compute configuration request.
func (s *Store) WithNeonControllerState(ctx context.Context, platformID string, revision int64, apply func(NeonControllerContext) (managedplatform.NeonControllerState, bool, error)) (bool, error) {
	if !managedPlatformID.MatchString(platformID) || revision < 1 || apply == nil {
		return false, ErrInput
	}
	return s.withNeonControllerState(ctx, platformID, revision, nil, apply)
}

// WithNeonRecoveryControllerState serializes initial routing with the restore
// fence and the controller state update. A previously accepted target operation
// cannot retain authority after the restore lease or its reviewed binding has
// changed.
func (s *Store) WithNeonRecoveryControllerState(ctx context.Context, recovery platformbackup.Operation, apply func(NeonControllerContext) (managedplatform.NeonControllerState, bool, error)) (bool, error) {
	if recovery.Kind != "restore" || !managedPlatformID.MatchString(recovery.TargetPlatformID) || recovery.ExpectedTargetRevision < 1 || apply == nil {
		return false, ErrInput
	}
	return s.withNeonControllerState(ctx, recovery.TargetPlatformID, recovery.ExpectedTargetRevision, &recovery, apply)
}

func (s *Store) withNeonControllerState(ctx context.Context, platformID string, revision int64, recovery *platformbackup.Operation, apply func(NeonControllerContext) (managedplatform.NeonControllerState, bool, error)) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var recoveryBinding NeonRecoveryBinding
	if recovery != nil {
		recoveryBinding, err = s.neonRecoveryFenceTx(ctx, tx, *recovery, false)
		if err != nil {
			return false, err
		}
		if recoveryBinding.OperationID != recovery.ID || recoveryBinding.TargetPlatformID != platformID || recoveryBinding.TargetRevision != revision || recoveryBinding.ArtifactID != recovery.ArtifactID {
			return false, ErrConflict
		}
	}
	op, err := scanManagedPlatformOperation(tx.QueryRow(ctx, `SELECT `+managedPlatformOperationQualifiedColumns+` FROM managed_platform_operations o JOIN managed_platforms p ON p.id=o.platform_id AND p.revision=o.revision WHERE o.platform_id=$1 AND o.revision=$2 AND o.kind IN ('create','update') AND o.status IN ('queued','running','succeeded') AND p.deleted_at IS NULL AND p.kind='neon' FOR SHARE OF p`, platformID, revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrConflict
	}
	if err != nil {
		return false, err
	}
	// An established revision retains runtime authority when its creator's
	// API key expires, matching managed-platform maintenance. In-flight user
	// operations still require their original live principal and fingerprint.
	if op.Status != "succeeded" {
		var project, environment string
		if err = tx.QueryRow(ctx, `SELECT project,environment FROM managed_platforms WHERE id=$1 AND revision=$2`, platformID, revision).Scan(&project, &environment); err != nil {
			return false, err
		}
		authority, fingerprint, err := s.managedPlatformAuthorityTx(ctx, tx, op.KeyID)
		if err != nil {
			return false, err
		}
		if authority.Principal.ID != op.IdentityID || !authority.Principal.AllowsManagedPlatform(project, environment, true) || !bytes.Equal(fingerprint, op.AuthorityFingerprint) {
			return false, ErrForbidden
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO managed_platform_neon_controller_state(platform_id,platform_revision,state) VALUES($1,$2,'{}') ON CONFLICT DO NOTHING`, platformID, revision); err != nil {
		return false, err
	}
	input := NeonControllerContext{Operation: op}
	if err = tx.QueryRow(ctx, `SELECT state FROM managed_platform_neon_controller_state WHERE platform_id=$1 AND platform_revision=$2 FOR UPDATE`, platformID, revision).Scan(&input.State); err != nil {
		return false, err
	}
	err = tx.QueryRow(ctx, `SELECT b.operation_id,b.target_platform_id,b.target_revision,b.artifact_id,b.manifest_sha256,b.tenant_id,b.timeline_id,b.tenant_generation,b.timeline_generation,b.staging_prefix
	 FROM managed_platform_neon_recovery_lineage l JOIN managed_platform_neon_recovery_bindings b ON b.operation_id=l.recovery_operation_id AND b.target_platform_id=l.platform_id
	 JOIN managed_platform_recovery_operations r ON r.id=b.operation_id AND r.status IN ('running','succeeded')
	 WHERE l.platform_id=$1 AND l.platform_revision IN ($2::bigint,$2::bigint-1) ORDER BY l.platform_revision DESC LIMIT 1`, platformID, revision).Scan(&input.Binding.OperationID, &input.Binding.TargetPlatformID, &input.Binding.TargetRevision, &input.Binding.ArtifactID, &input.Binding.ManifestSHA256, &input.Binding.TenantID, &input.Binding.TimelineID, &input.Binding.TenantGeneration, &input.Binding.TimelineGeneration, &input.Binding.StagingPrefix)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if recovery != nil && input.Binding != recoveryBinding {
		return false, ErrConflict
	}
	rows, err := tx.Query(ctx, `SELECT platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id FROM effective_platform_component_resources WHERE platform_id=$1 AND platform_revision<=$2 ORDER BY component,resource_kind LIMIT $3`, platformID, revision, MaxManagedPlatformResources+1)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var claim PlatformResourceClaim
		if err = rows.Scan(&claim.PlatformID, &claim.PlatformRevision, &claim.Component, &claim.Kind, &claim.ResourceID, &claim.ImmutableGeneration, &claim.OwnerOperationID); err != nil {
			rows.Close()
			return false, err
		}
		input.Claims = append(input.Claims, claim)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if len(input.Claims) > MaxManagedPlatformResources {
		return false, ErrConflict
	}
	if recovery != nil {
		matches := 0
		for _, claim := range input.Claims {
			if claim.Component != "timeline" {
				continue
			}
			matches++
			var exact bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_component_recovery_overrides WHERE recovery_operation_id=$1 AND platform_id=$2 AND platform_revision=$3 AND component='timeline' AND resource_kind=$4 AND phase='confirmed' AND replacement_resource_id=$5 AND replacement_generation=$6 AND replacement_released_at IS NULL AND adopted_at IS NULL)`, recovery.ID, platformID, revision, claim.Kind, claim.ResourceID, claim.ImmutableGeneration).Scan(&exact); err != nil {
				return false, err
			}
			if !exact {
				return false, ErrConflict
			}
		}
		if matches != 1 {
			return false, ErrConflict
		}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_resource_intents WHERE platform_id=$1 AND platform_revision=$2 AND component LIKE 'compute-compute-%' AND confirmed_at IS NULL AND released_at IS NULL)`, platformID, revision).Scan(&input.PendingCompute); err != nil {
		return false, err
	}
	input.ObserveTenantPlacement = func(prior PlatformResourceClaim, observedID string, generation int64) error {
		return observeNeonTenantPlacementTx(ctx, tx, op, prior, observedID, generation)
	}
	next, applied, err := apply(input)
	if err != nil {
		return false, err
	}
	encoded, err := json.Marshal(next)
	if err != nil || len(encoded) > 16384 {
		return false, ErrInput
	}
	if _, err = tx.Exec(ctx, `UPDATE managed_platform_neon_controller_state SET state=$3,updated_at=now() WHERE platform_id=$1 AND platform_revision=$2`, platformID, revision, encoded); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return applied, nil
}

func (s *Store) NeonControllerState(ctx context.Context, platformID string, revision int64) (managedplatform.NeonControllerState, error) {
	var state managedplatform.NeonControllerState
	if !managedPlatformID.MatchString(platformID) || revision < 1 {
		return state, ErrInput
	}
	err := s.Pool.QueryRow(ctx, `SELECT n.state FROM managed_platform_neon_controller_state n JOIN managed_platforms p ON p.id=n.platform_id AND p.revision=$2::bigint AND p.deleted_at IS NULL WHERE n.platform_id=$1 AND n.platform_revision IN ($2::bigint,$2::bigint-1) ORDER BY n.platform_revision DESC LIMIT 1`, platformID, revision).Scan(&state)
	return state, err
}
