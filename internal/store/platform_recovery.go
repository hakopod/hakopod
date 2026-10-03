package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/jackc/pgx/v5"
)

type NeonRecoveryBinding struct {
	OperationID, TargetPlatformID, ArtifactID, ManifestSHA256 string
	TargetRevision, TenantGeneration, TimelineGeneration      int64
	TenantID, TimelineID, StagingPrefix                       string
}

func (s *Store) BindNeonRecoveryTarget(ctx context.Context, op platformbackup.Operation, manifest platformbackup.Manifest, stagingPrefix string) (NeonRecoveryBinding, error) {
	var binding NeonRecoveryBinding
	if op.Kind != "restore" || manifest.Format != platformbackup.NeonFormat || manifest.Neon == nil || manifest.ManifestSHA256 == "" || manifest.ManifestSHA256 != manifest.Digest() || op.ArtifactID == "" || op.TargetPlatformID == "" || stagingPrefix == "" || len(stagingPrefix) > 512 {
		return binding, ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return binding, err
	}
	defer tx.Rollback(ctx)
	stored, err := scanPlatformRecovery(tx.QueryRow(ctx, `SELECT `+platformRecoveryQualifiedColumns+` FROM managed_platform_recovery_operations o
		JOIN managed_platforms source ON source.id=o.source_platform_id AND source.revision=o.expected_source_revision AND source.project=o.project AND source.environment=o.environment AND source.kind='neon' AND source.deleted_at IS NULL
		JOIN managed_platforms target ON target.id=o.target_platform_id AND target.revision=o.expected_target_revision AND target.project=o.project AND target.environment=o.environment AND target.kind='neon' AND target.deleted_at IS NULL
		JOIN managed_platform_recovery_artifacts artifact ON artifact.id=o.artifact_id AND artifact.source_platform_id=o.source_platform_id AND artifact.source_revision=o.expected_source_revision AND artifact.manifest_sha256=$8 AND artifact.published_at IS NOT NULL AND artifact.deleted_at IS NULL
		WHERE o.id=$1 AND o.kind='restore' AND o.source_platform_id=$2 AND o.target_platform_id=$3 AND o.expected_source_revision=$4 AND o.expected_target_revision=$5 AND o.artifact_id=$6 AND o.status='running' AND o.lease=$7 AND o.lease_until>=clock_timestamp() AND o.cancel_requested=false
		FOR UPDATE OF o FOR SHARE OF source,target,artifact`, op.ID, op.SourcePlatformID, op.TargetPlatformID, op.ExpectedSourceRevision, op.ExpectedTargetRevision, op.ArtifactID, op.Lease, manifest.ManifestSHA256))
	if errors.Is(err, pgx.ErrNoRows) {
		return binding, ErrConflict
	}
	if err != nil {
		return binding, err
	}
	if stored.Kind != op.Kind || stored.Project != op.Project || stored.Environment != op.Environment || stored.SourcePlatformID != op.SourcePlatformID || stored.TargetPlatformID != op.TargetPlatformID || stored.ArtifactID != op.ArtifactID || stored.DestinationID != op.DestinationID || stored.DestinationRevision != op.DestinationRevision || stored.ExpectedSourceRevision != op.ExpectedSourceRevision || stored.ExpectedTargetRevision != op.ExpectedTargetRevision || stored.Lease != op.Lease || !bytes.Equal(stored.AuthorityFingerprint, op.AuthorityFingerprint) {
		return binding, ErrConflict
	}
	if err = s.reauthorizePlatformRecoveryTx(ctx, tx, stored); err != nil {
		return binding, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO managed_platform_neon_recovery_bindings(operation_id,target_platform_id,target_revision,artifact_id,manifest_sha256,tenant_id,timeline_id,tenant_generation,timeline_generation,staging_prefix)
	VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	ON CONFLICT DO NOTHING
	RETURNING operation_id,target_platform_id,target_revision,artifact_id,manifest_sha256,tenant_id,timeline_id,tenant_generation,timeline_generation,staging_prefix`, op.ID, op.TargetPlatformID, op.ExpectedTargetRevision, op.ArtifactID, manifest.ManifestSHA256, manifest.Neon.TenantID, manifest.Neon.TimelineID, manifest.Neon.TenantGeneration, manifest.Neon.TimelineGeneration, stagingPrefix).Scan(&binding.OperationID, &binding.TargetPlatformID, &binding.TargetRevision, &binding.ArtifactID, &binding.ManifestSHA256, &binding.TenantID, &binding.TimelineID, &binding.TenantGeneration, &binding.TimelineGeneration, &binding.StagingPrefix)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT operation_id,target_platform_id,target_revision,artifact_id,manifest_sha256,tenant_id,timeline_id,tenant_generation,timeline_generation,staging_prefix FROM managed_platform_neon_recovery_bindings WHERE operation_id=$1`, op.ID).Scan(&binding.OperationID, &binding.TargetPlatformID, &binding.TargetRevision, &binding.ArtifactID, &binding.ManifestSHA256, &binding.TenantID, &binding.TimelineID, &binding.TenantGeneration, &binding.TimelineGeneration, &binding.StagingPrefix)
		if errors.Is(err, pgx.ErrNoRows) {
			var existingOperation string
			collisionErr := tx.QueryRow(ctx, `SELECT operation_id FROM managed_platform_neon_recovery_bindings WHERE target_platform_id=$1 AND target_revision=$2`, op.TargetPlatformID, op.ExpectedTargetRevision).Scan(&existingOperation)
			if collisionErr == nil {
				return NeonRecoveryBinding{}, fmt.Errorf("%w: this Neon target revision already has an immutable recovery binding; create and review a fresh target revision", ErrConflict)
			}
			if !errors.Is(collisionErr, pgx.ErrNoRows) {
				return NeonRecoveryBinding{}, collisionErr
			}
		}
	}
	if err != nil {
		return binding, err
	}
	if binding.TargetPlatformID != op.TargetPlatformID || binding.TargetRevision != op.ExpectedTargetRevision || binding.ArtifactID != op.ArtifactID || binding.ManifestSHA256 != manifest.ManifestSHA256 || binding.TenantID != manifest.Neon.TenantID || binding.TimelineID != manifest.Neon.TimelineID || binding.TenantGeneration != manifest.Neon.TenantGeneration || binding.TimelineGeneration != manifest.Neon.TimelineGeneration || binding.StagingPrefix != stagingPrefix {
		return NeonRecoveryBinding{}, ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO managed_platform_neon_recovery_lineage(platform_id,platform_revision,recovery_operation_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, binding.TargetPlatformID, binding.TargetRevision, binding.OperationID); err != nil {
		return NeonRecoveryBinding{}, err
	}
	var lineageOperationID string
	if err = tx.QueryRow(ctx, `SELECT recovery_operation_id FROM managed_platform_neon_recovery_lineage WHERE platform_id=$1 AND platform_revision=$2`, binding.TargetPlatformID, binding.TargetRevision).Scan(&lineageOperationID); err != nil {
		return NeonRecoveryBinding{}, err
	}
	if lineageOperationID != binding.OperationID {
		return NeonRecoveryBinding{}, ErrConflict
	}
	return binding, tx.Commit(ctx)
}

func (s *Store) NeonRecoveryBindingForTarget(ctx context.Context, platformID string, revision int64) (NeonRecoveryBinding, error) {
	var binding NeonRecoveryBinding
	err := s.Pool.QueryRow(ctx, `SELECT b.operation_id,b.target_platform_id,b.target_revision,b.artifact_id,b.manifest_sha256,b.tenant_id,b.timeline_id,b.tenant_generation,b.timeline_generation,b.staging_prefix
		FROM managed_platform_neon_recovery_lineage l JOIN managed_platform_neon_recovery_bindings b ON b.operation_id=l.recovery_operation_id AND b.target_platform_id=l.platform_id
		JOIN managed_platform_recovery_operations o ON o.id=b.operation_id AND o.status='succeeded'
		JOIN managed_platforms p ON p.id=l.platform_id AND p.revision=$2 AND p.deleted_at IS NULL
		WHERE l.platform_id=$1 AND l.platform_revision=$2`, platformID, revision).Scan(&binding.OperationID, &binding.TargetPlatformID, &binding.TargetRevision, &binding.ArtifactID, &binding.ManifestSHA256, &binding.TenantID, &binding.TimelineID, &binding.TenantGeneration, &binding.TimelineGeneration, &binding.StagingPrefix)
	return binding, err
}

func (s *Store) NeonRecoveryBindingForRecovery(ctx context.Context, op platformbackup.Operation) (NeonRecoveryBinding, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return NeonRecoveryBinding{}, err
	}
	defer tx.Rollback(ctx)
	binding, err := s.neonRecoveryFenceTx(ctx, tx, op, platformbackup.RecoveryCleanupFromContext(ctx))
	if err != nil {
		if !errors.Is(err, ErrConflict) {
			return NeonRecoveryBinding{}, err
		}
		var existingOperationID string
		bindingErr := tx.QueryRow(ctx, `SELECT operation_id FROM managed_platform_neon_recovery_bindings WHERE target_platform_id=$1 AND target_revision=$2`, op.TargetPlatformID, op.ExpectedTargetRevision).Scan(&existingOperationID)
		if bindingErr == nil || !errors.Is(bindingErr, pgx.ErrNoRows) {
			if bindingErr != nil {
				return NeonRecoveryBinding{}, bindingErr
			}
			return NeonRecoveryBinding{}, ErrConflict
		}
		if platformbackup.RecoveryCleanupFromContext(ctx) {
			if err = s.neonRecoveryEmptyCleanupFenceTx(ctx, tx, op); err != nil {
				return NeonRecoveryBinding{}, err
			}
		} else {
			stored, scanErr := scanPlatformRecovery(tx.QueryRow(ctx, `SELECT `+platformRecoveryQualifiedColumns+` FROM managed_platform_recovery_operations o JOIN managed_platforms p ON p.id=o.target_platform_id AND p.revision=o.expected_target_revision AND p.kind='neon' AND p.deleted_at IS NULL WHERE o.id=$1 AND o.kind='restore' AND o.source_platform_id=$2 AND o.target_platform_id=$3 AND o.expected_source_revision=$4 AND o.expected_target_revision=$5 AND o.artifact_id=$6 AND o.status='running' AND o.lease=$7 AND o.lease_until>=clock_timestamp() AND o.cancel_requested=false FOR UPDATE OF o,p`, op.ID, op.SourcePlatformID, op.TargetPlatformID, op.ExpectedSourceRevision, op.ExpectedTargetRevision, op.ArtifactID, op.Lease))
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return NeonRecoveryBinding{}, ErrConflict
			}
			if scanErr != nil {
				return NeonRecoveryBinding{}, scanErr
			}
			if !bytes.Equal(stored.AuthorityFingerprint, op.AuthorityFingerprint) {
				return NeonRecoveryBinding{}, ErrConflict
			}
			if err = s.reauthorizePlatformRecoveryTx(ctx, tx, stored); err != nil {
				return NeonRecoveryBinding{}, err
			}
		}
		return NeonRecoveryBinding{}, pgx.ErrNoRows
	}
	if err = tx.Commit(ctx); err != nil {
		return NeonRecoveryBinding{}, err
	}
	return binding, nil
}

func (s *Store) ManagedPlatformRecoverySnapshot(ctx context.Context, platformID string, revision int64) (ManagedPlatformOperation, error) {
	return scanManagedPlatformOperation(s.Pool.QueryRow(ctx, `SELECT `+managedPlatformOperationColumns+` FROM managed_platform_operations WHERE platform_id=$1 AND revision=$2 AND status='succeeded' ORDER BY finished_at DESC,id DESC LIMIT 1`, platformID, revision))
}

func (s *Store) SaveNeonRecoveryCapture(ctx context.Context, op platformbackup.Operation, identity platformbackup.NeonIdentity) error {
	if op.Kind != "backup" {
		return ErrInput
	}
	r, err := s.Pool.Exec(ctx, `INSERT INTO managed_platform_neon_recovery_captures(operation_id,manifest_neon) SELECT $1,$3 WHERE EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE id=$1 AND kind='backup' AND status='running' AND lease=$2 AND lease_until>=clock_timestamp()) ON CONFLICT(operation_id) DO NOTHING`, op.ID, op.Lease, JSON(identity))
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) NeonRecoveryCapture(ctx context.Context, op platformbackup.Operation) (platformbackup.NeonIdentity, error) {
	var identity platformbackup.NeonIdentity
	err := s.Pool.QueryRow(ctx, `SELECT manifest_neon FROM managed_platform_neon_recovery_captures WHERE operation_id=$1 AND EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE id=$1 AND lease=$2 AND status='running' AND lease_until>=clock_timestamp())`, op.ID, op.Lease).Scan(&identity)
	return identity, err
}

const platformRecoveryColumns = `id,kind,project,environment,source_platform_id,COALESCE(target_platform_id,''),COALESCE(artifact_id,''),COALESCE(result_artifact_id,''),COALESCE(destination_id,''),COALESCE(destination_revision,0),expected_source_revision,COALESCE(expected_target_revision,0),status,phase,message,authority_fingerprint,lease,cancel_requested,cleanup_required`

func scanPlatformRecovery(row scanner) (platformbackup.Operation, error) {
	var op platformbackup.Operation
	err := row.Scan(&op.ID, &op.Kind, &op.Project, &op.Environment, &op.SourcePlatformID, &op.TargetPlatformID, &op.ArtifactID, &op.ResultArtifactID, &op.DestinationID, &op.DestinationRevision, &op.ExpectedSourceRevision, &op.ExpectedTargetRevision, &op.Status, &op.Phase, &op.Message, &op.AuthorityFingerprint, &op.Lease, &op.CancelRequested, &op.CleanupRequired)
	return op, err
}

func platformRecoveryHash(intent platformbackup.Intent) [32]byte { return sha256.Sum256(JSON(intent)) }

func (s *Store) ManagedPlatformRecoveryContract(ctx context.Context, id string, revision int64) (ManagedPlatform, managedplatform.Plan, error) {
	item, err := scanManagedPlatform(s.Pool.QueryRow(ctx, "SELECT "+managedPlatformColumns+" FROM managed_platforms WHERE id=$1 AND revision=$2 AND kind IN ('supabase','neon') AND deleted_at IS NULL", id, revision))
	if err != nil {
		return item, managedplatform.Plan{}, err
	}
	var plan managedplatform.Plan
	err = s.Pool.QueryRow(ctx, `SELECT resolved_plan FROM managed_platform_operations WHERE platform_id=$1 AND revision=$2 AND status='succeeded' ORDER BY finished_at DESC LIMIT 1`, id, revision).Scan(&plan)
	return item, plan, err
}
func (s *Store) ManagedPlatformRecoveryClaims(ctx context.Context, id string, revision int64) (map[string]PlatformResourceClaim, error) {
	rows, err := s.Pool.Query(ctx, `SELECT b.platform_id,b.platform_revision,b.component,b.resource_kind,
		COALESCE(r.replacement_resource_id,b.resource_id),COALESCE(r.runtime_generation,r.replacement_generation,b.immutable_generation),b.owner_operation_id,b.released_at
		FROM platform_component_resources b
		LEFT JOIN platform_component_recovery_overrides r ON r.platform_id=b.platform_id AND r.platform_revision=b.platform_revision AND r.component=b.component AND r.resource_kind=b.resource_kind AND r.phase='confirmed' AND r.replacement_released_at IS NULL
		WHERE b.platform_id=$1 AND b.platform_revision=$2 AND b.released_at IS NULL
		AND NOT EXISTS(SELECT 1 FROM platform_component_recovery_overrides pending WHERE pending.platform_id=b.platform_id AND pending.platform_revision=b.platform_revision AND pending.component=b.component AND pending.resource_kind=b.resource_kind AND pending.phase IN ('prior_released','reserved','replacement_released','complete','empty_complete'))
		ORDER BY b.component,b.resource_kind LIMIT $3`, id, revision, MaxManagedPlatformResources+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PlatformResourceClaim{}
	for rows.Next() {
		var c PlatformResourceClaim
		if err = rows.Scan(&c.PlatformID, &c.PlatformRevision, &c.Component, &c.Kind, &c.ResourceID, &c.ImmutableGeneration, &c.OwnerOperationID, &c.ReleasedAt); err != nil {
			return nil, err
		}
		out[c.Component] = c
	}
	if len(out) > MaxManagedPlatformResources {
		return nil, ErrConflict
	}
	return out, rows.Err()
}
func (s *Store) ManagedPlatformRecoveryCurrentClaim(ctx context.Context, id, component string) (PlatformResourceClaim, error) {
	var c PlatformResourceClaim
	err := s.Pool.QueryRow(ctx, `SELECT r.platform_id,r.platform_revision,r.component,r.resource_kind,r.resource_id,r.immutable_generation,r.owner_operation_id,r.released_at FROM platform_component_resources r JOIN managed_platforms p ON p.id=r.platform_id AND p.revision=r.platform_revision WHERE r.platform_id=$1 AND r.component=$2 AND r.released_at IS NULL AND p.deleted_at IS NULL`, id, component).Scan(&c.PlatformID, &c.PlatformRevision, &c.Component, &c.Kind, &c.ResourceID, &c.ImmutableGeneration, &c.OwnerOperationID, &c.ReleasedAt)
	return c, err
}

func (s *Store) SavePlatformRecoveryReview(ctx context.Context, p Principal, intent platformbackup.Intent) (platformbackup.Review, error) {
	var review platformbackup.Review
	if err := intent.Validate(); err != nil {
		return review, ErrInput
	}
	if !p.AllowsManagedPlatform(intent.Project, intent.Environment, true) {
		return review, ErrForbidden
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return review, err
	}
	defer tx.Rollback(ctx)
	authority, fingerprint, err := s.managedPlatformAuthorityTx(ctx, tx, p.KeyID)
	if err != nil {
		return review, err
	}
	if authority.Principal.ID != p.ID || !authority.Principal.AllowsManagedPlatform(intent.Project, intent.Environment, true) {
		return review, ErrForbidden
	}
	var sourceRevision int64
	var sourceKind, sourceStatus string
	if err = tx.QueryRow(ctx, "SELECT revision,kind,status FROM managed_platforms WHERE id=$1 AND project=$2 AND environment=$3 AND deleted_at IS NULL FOR SHARE", intent.SourcePlatformID, intent.Project, intent.Environment).Scan(&sourceRevision, &sourceKind, &sourceStatus); err != nil {
		return review, err
	}
	if (sourceKind != "supabase" && sourceKind != "neon") || sourceRevision != intent.ExpectedSourceRevision || sourceStatus != "ready" {
		return review, ErrConflict
	}
	if intent.Kind == "backup" {
		var id string
		var revision int64
		if err = tx.QueryRow(ctx, "SELECT id,revision FROM backup_destinations WHERE id=$1 AND config->>'project'=$2 AND config->>'environment'=$3 FOR SHARE", intent.DestinationID, intent.Project, intent.Environment).Scan(&id, &revision); err != nil {
			return review, err
		}
		if revision != intent.DestinationRevision {
			return review, ErrConflict
		}
	} else {
		var targetRevision int64
		var targetKind, targetStatus string
		if err = tx.QueryRow(ctx, "SELECT revision,kind,status FROM managed_platforms WHERE id=$1 AND project=$2 AND environment=$3 AND deleted_at IS NULL FOR SHARE", intent.TargetPlatformID, intent.Project, intent.Environment).Scan(&targetRevision, &targetKind, &targetStatus); err != nil {
			return review, err
		}
		if targetKind != sourceKind || targetRevision != intent.ExpectedTargetRevision || targetStatus != "ready" {
			return review, ErrConflict
		}
		var published bool
		var artifactFormat string
		if err = tx.QueryRow(ctx, "SELECT published_at IS NOT NULL AND deleted_at IS NULL,manifest->>'format' FROM managed_platform_recovery_artifacts WHERE id=$1 AND source_platform_id=$2 FOR SHARE", intent.ArtifactID, intent.SourcePlatformID).Scan(&published, &artifactFormat); err != nil || !published || artifactFormat != recoveryFormat(sourceKind) {
			if err != nil {
				return review, err
			}
			return review, ErrConflict
		}
	}
	requestHash := platformRecoveryHash(intent)
	review = platformbackup.Review{ID: NewID(), Intent: intent, RequestHash: hex.EncodeToString(requestHash[:]), AuthorityFingerprint: hex.EncodeToString(fingerprint), ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
	_, err = tx.Exec(ctx, `INSERT INTO managed_platform_recovery_reviews(id,identity_id,key_id,project,environment,kind,source_platform_id,target_platform_id,artifact_id,destination_id,destination_revision,expected_source_revision,expected_target_revision,request_hash,authority_fingerprint,reviewed_intent,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),NULLIF($9,''),NULLIF($10,''),NULLIF($11,0),$12,NULLIF($13,0),$14,$15,$16,$17)`, review.ID, p.ID, p.KeyID, intent.Project, intent.Environment, intent.Kind, intent.SourcePlatformID, intent.TargetPlatformID, intent.ArtifactID, intent.DestinationID, intent.DestinationRevision, intent.ExpectedSourceRevision, intent.ExpectedTargetRevision, requestHash[:], fingerprint, JSON(intent), review.ExpiresAt)
	if err != nil {
		return review, err
	}
	return review, tx.Commit(ctx)
}

func (s *Store) AcceptPlatformRecovery(ctx context.Context, p Principal, intent platformbackup.Intent, review platformbackup.Review, idem string) (platformbackup.Operation, error) {
	var op platformbackup.Operation
	if err := intent.Validate(); err != nil || len(idem) < 8 || len(idem) > 128 {
		return op, ErrInput
	}
	requestHash := platformRecoveryHash(intent)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return op, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044269)"); err != nil {
		return op, err
	}
	if err = lockManagedPlatformMutations(ctx, tx, intent.SourcePlatformID, intent.TargetPlatformID); err != nil {
		return op, err
	}
	authority, fingerprint, err := s.managedPlatformAuthorityTx(ctx, tx, p.KeyID)
	if err != nil {
		return op, err
	}
	if authority.Principal.ID != p.ID || !authority.Principal.AllowsManagedPlatform(intent.Project, intent.Environment, true) {
		return op, ErrForbidden
	}
	if s.RequireManagedPlatformAdmission && s.AdmitManagedPlatform == nil {
		return op, ErrForbidden
	}
	if s.AdmitManagedPlatform != nil {
		if err = s.AdmitManagedPlatform(ctx, tx, p, intent.Project, intent.Environment, idem); err != nil {
			return op, err
		}
	}
	if s.RequireManagedPlatformAdmission || s.ManagedPlatformCapacityBudget != nil {
		var environment string
		if err = tx.QueryRow(ctx, "SELECT name FROM environments WHERE project=$1 AND name=$2 FOR UPDATE", intent.Project, intent.Environment).Scan(&environment); err != nil {
			return op, err
		}
		if err = s.checkManagedPlatformCapacityReservationTx(ctx, tx, intent.Project, intent.Environment); err != nil {
			return op, err
		}
	}
	if existing, e := scanPlatformRecovery(tx.QueryRow(ctx, "SELECT "+platformRecoveryColumns+" FROM managed_platform_recovery_operations WHERE identity_id=$1 AND idempotency_key=$2", p.ID, idem)); e == nil {
		var stored, storedAuthority []byte
		if e = tx.QueryRow(ctx, "SELECT request_hash,authority_fingerprint FROM managed_platform_recovery_operations WHERE id=$1", existing.ID).Scan(&stored, &storedAuthority); e != nil {
			return op, e
		}
		if !bytes.Equal(stored, requestHash[:]) || !bytes.Equal(storedAuthority, fingerprint) {
			return op, ErrConflict
		}
		if e = tx.Commit(ctx); e != nil {
			return op, e
		}
		existing.AuthorityFingerprint = nil
		existing.Lease = ""
		return existing, nil
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return op, e
	}
	platformIDs := []string{intent.SourcePlatformID}
	if intent.TargetPlatformID != "" {
		platformIDs = append(platformIDs, intent.TargetPlatformID)
	}
	if err = rejectPlatformRuntimeOverlapTx(ctx, tx, platformIDs); err != nil {
		return op, err
	}
	if err = rejectUnfinishedPlatformMaintenanceRecoveryTx(ctx, tx, platformIDs); err != nil {
		return op, err
	}
	var overlapping bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE status IN ('queued','running') AND (source_platform_id=ANY($1) OR target_platform_id=ANY($1)))`, platformIDs).Scan(&overlapping); err != nil {
		return op, err
	}
	if overlapping {
		return op, ErrConflict
	}
	var consumed *time.Time
	var storedHash, storedFingerprint []byte
	var expires time.Time
	var storedIntent platformbackup.Intent
	err = tx.QueryRow(ctx, `SELECT request_hash,authority_fingerprint,reviewed_intent,expires_at,consumed_at FROM managed_platform_recovery_reviews WHERE id=$1 AND identity_id=$2 AND key_id=$3 FOR UPDATE`, review.ID, p.ID, p.KeyID).Scan(&storedHash, &storedFingerprint, &storedIntent, &expires, &consumed)
	if err != nil {
		return op, err
	}
	if consumed != nil || time.Now().After(expires) || !bytes.Equal(storedHash, requestHash[:]) || !bytes.Equal(storedFingerprint, fingerprint) || !bytes.Equal(JSON(storedIntent), JSON(intent)) {
		return op, ErrConflict
	}
	var sourceRevision int64
	var sourceKind, sourceStatus string
	if err = tx.QueryRow(ctx, `SELECT revision,kind,status FROM managed_platforms WHERE id=$1 AND project=$2 AND environment=$3 AND kind IN ('supabase','neon') AND deleted_at IS NULL FOR UPDATE`, intent.SourcePlatformID, intent.Project, intent.Environment).Scan(&sourceRevision, &sourceKind, &sourceStatus); err != nil {
		return op, err
	}
	if sourceRevision != intent.ExpectedSourceRevision || sourceStatus != "ready" {
		return op, ErrConflict
	}
	if intent.Kind == "backup" {
		var revision int64
		if err = tx.QueryRow(ctx, `SELECT revision FROM backup_destinations WHERE id=$1 AND config->>'project'=$2 AND config->>'environment'=$3 FOR SHARE`, intent.DestinationID, intent.Project, intent.Environment).Scan(&revision); err != nil {
			return op, err
		}
		if revision != intent.DestinationRevision {
			return op, ErrConflict
		}
	} else {
		var targetRevision int64
		var targetKind, targetStatus string
		if err = tx.QueryRow(ctx, `SELECT revision,kind,status FROM managed_platforms WHERE id=$1 AND project=$2 AND environment=$3 AND kind IN ('supabase','neon') AND deleted_at IS NULL FOR UPDATE`, intent.TargetPlatformID, intent.Project, intent.Environment).Scan(&targetRevision, &targetKind, &targetStatus); err != nil {
			return op, err
		}
		if targetKind != sourceKind || targetRevision != intent.ExpectedTargetRevision || targetStatus != "ready" {
			return op, ErrConflict
		}
		var valid bool
		var artifactFormat string
		if err = tx.QueryRow(ctx, `SELECT published_at IS NOT NULL AND deleted_at IS NULL,manifest->>'format' FROM managed_platform_recovery_artifacts WHERE id=$1 AND source_platform_id=$2 FOR SHARE`, intent.ArtifactID, intent.SourcePlatformID).Scan(&valid, &artifactFormat); err != nil {
			return op, err
		}
		if !valid || artifactFormat != recoveryFormat(sourceKind) {
			return op, ErrConflict
		}
		if sourceKind == "neon" {
			var bound bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_platform_neon_recovery_lineage WHERE platform_id=$1 AND platform_revision=$2)`, intent.TargetPlatformID, intent.ExpectedTargetRevision).Scan(&bound); err != nil {
				return op, err
			}
			if bound {
				return op, fmt.Errorf("%w: this Neon target revision already has an immutable recovery binding; create and review a fresh target revision", ErrConflict)
			}
		}
	}
	op = platformbackup.Operation{ID: NewID(), Kind: intent.Kind, Project: intent.Project, Environment: intent.Environment, SourcePlatformID: intent.SourcePlatformID, TargetPlatformID: intent.TargetPlatformID, ArtifactID: intent.ArtifactID, DestinationID: intent.DestinationID, DestinationRevision: intent.DestinationRevision, ExpectedSourceRevision: intent.ExpectedSourceRevision, ExpectedTargetRevision: intent.ExpectedTargetRevision, AuthorityFingerprint: fingerprint, Status: "queued", Phase: "accepted"}
	_, err = tx.Exec(ctx, `INSERT INTO managed_platform_recovery_operations(id,kind,identity_id,key_id,project,environment,review_id,idempotency_key,request_hash,authority_fingerprint,source_platform_id,target_platform_id,artifact_id,destination_id,destination_revision,expected_source_revision,expected_target_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,''),NULLIF($13,''),NULLIF($14,''),NULLIF($15,0),$16,NULLIF($17,0))`, op.ID, op.Kind, p.ID, p.KeyID, op.Project, op.Environment, review.ID, idem, requestHash[:], fingerprint, op.SourcePlatformID, op.TargetPlatformID, op.ArtifactID, op.DestinationID, op.DestinationRevision, op.ExpectedSourceRevision, op.ExpectedTargetRevision)
	if err != nil {
		return op, err
	}
	if _, err = tx.Exec(ctx, "UPDATE managed_platform_recovery_reviews SET consumed_at=now() WHERE id=$1 AND consumed_at IS NULL", review.ID); err != nil {
		return op, err
	}
	if err = tx.Commit(ctx); err != nil {
		return op, err
	}
	op.AuthorityFingerprint = nil
	return op, nil
}

func recoveryFormat(kind string) string {
	if kind == "neon" {
		return platformbackup.NeonFormat
	}
	return platformbackup.Format
}

func (s *Store) ClaimPlatformRecovery(ctx context.Context, lease string) (platformbackup.Operation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return platformbackup.Operation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044269)"); err != nil {
		return platformbackup.Operation{}, err
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT r.id FROM managed_platform_recovery_operations r WHERE ((r.status='queued' AND r.next_attempt_at<=now()) OR (r.status='running' AND r.lease_until<now()))
		AND NOT EXISTS(SELECT 1 FROM managed_platform_operations o WHERE (o.platform_id=r.source_platform_id OR o.platform_id=r.target_platform_id) AND o.status IN ('queued','running'))
		AND NOT EXISTS(SELECT 1 FROM managed_platform_maintenance m WHERE (m.platform_id=r.source_platform_id OR m.platform_id=r.target_platform_id) AND m.status='running' AND m.lease_until>=clock_timestamp())
		ORDER BY r.cleanup_required DESC,r.next_attempt_at,r.created_at,r.id FOR UPDATE OF r SKIP LOCKED LIMIT 1`).Scan(&id)
	if err != nil {
		return platformbackup.Operation{}, err
	}
	op, err := scanPlatformRecovery(tx.QueryRow(ctx, "UPDATE managed_platform_recovery_operations SET status='running',lease=$2,lease_until=clock_timestamp()+interval '30 seconds',attempt=attempt+1,started_at=COALESCE(started_at,now()) WHERE id=$1 RETURNING "+platformRecoveryColumns, id, lease))
	if err != nil {
		return op, err
	}
	if !op.CleanupRequired {
		if err = s.reauthorizePlatformRecoveryTx(ctx, tx, op); err != nil {
			_, _ = tx.Exec(ctx, "UPDATE managed_platform_recovery_operations SET status='cancelled',phase='authority-revoked',message='Operation authority is no longer valid.',lease='',lease_until=NULL,finished_at=now() WHERE id=$1", id)
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return op, commitErr
			}
			return platformbackup.Operation{}, pgx.ErrNoRows
		}
	}
	return op, tx.Commit(ctx)
}

func (s *Store) reauthorizePlatformRecoveryTx(ctx context.Context, tx pgx.Tx, op platformbackup.Operation) error {
	var keyID, identityID, project, environment string
	var fingerprint []byte
	if err := tx.QueryRow(ctx, `SELECT o.key_id,o.identity_id,p.project,p.environment,o.authority_fingerprint FROM managed_platform_recovery_operations o JOIN managed_platforms p ON p.id=o.source_platform_id WHERE o.id=$1 FOR SHARE OF o,p`, op.ID).Scan(&keyID, &identityID, &project, &environment, &fingerprint); err != nil {
		return err
	}
	authority, current, err := s.managedPlatformAuthorityTx(ctx, tx, keyID)
	if err != nil {
		return err
	}
	if authority.Principal.ID != identityID || !authority.Principal.AllowsManagedPlatform(project, environment, true) || !bytes.Equal(current, fingerprint) || !bytes.Equal(fingerprint, op.AuthorityFingerprint) {
		return ErrForbidden
	}
	return nil
}

func (s *Store) ReauthorizePlatformRecovery(ctx context.Context, op platformbackup.Operation) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.reauthorizePlatformRecoveryTx(ctx, tx, op); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) HeartbeatPlatformRecovery(ctx context.Context, op platformbackup.Operation) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err = s.reauthorizePlatformRecoveryTx(ctx, tx, op); err != nil {
		return false, err
	}
	var cancelled bool
	err = tx.QueryRow(ctx, `UPDATE managed_platform_recovery_operations SET lease_until=clock_timestamp()+interval '30 seconds' WHERE id=$1 AND status='running' AND lease=$2 AND lease_until>=clock_timestamp() RETURNING cancel_requested`, op.ID, op.Lease).Scan(&cancelled)
	if err != nil {
		return false, err
	}
	return cancelled, tx.Commit(ctx)
}
func (s *Store) FencePlatformRecoveryCleanup(ctx context.Context, op platformbackup.Operation) error {
	if op.Kind == "restore" {
		if !platformbackup.RecoveryCleanupFromContext(ctx) {
			return ErrConflict
		}
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err = s.neonRecoveryFenceTx(ctx, tx, op, true); err != nil {
			if !errors.Is(err, ErrConflict) {
				return err
			}
			if err = s.neonRecoveryEmptyCleanupFenceTx(ctx, tx, op); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE managed_platform_recovery_operations SET lease_until=clock_timestamp()+interval '30 seconds' WHERE id=$1 AND status='running' AND lease=$2 AND lease_until>=clock_timestamp()`, op.ID, op.Lease); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.backupRecoveryCleanupFenceTx(ctx, tx, op, false); err != nil {
		return err
	}
	r, err := tx.Exec(ctx, `UPDATE managed_platform_recovery_operations SET lease_until=clock_timestamp()+interval '30 seconds' WHERE id=$1 AND status='running' AND lease=$2 AND lease_until>=clock_timestamp()`, op.ID, op.Lease)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Store) backupRecoveryCleanupFenceTx(ctx context.Context, tx pgx.Tx, supplied platformbackup.Operation, requireComplete bool) error {
	if !platformbackup.RecoveryCleanupFromContext(ctx) || supplied.Kind != "backup" {
		return ErrConflict
	}
	stored, err := scanPlatformRecovery(tx.QueryRow(ctx, `SELECT `+platformRecoveryColumns+` FROM managed_platform_recovery_operations WHERE id=$1 AND kind='backup' AND source_platform_id=$2 AND expected_source_revision=$3 AND destination_id=$4 AND destination_revision=$5 AND status='running' AND lease=$6 AND lease_until>=clock_timestamp() AND (NOT $7 OR cleanup_required=false) FOR UPDATE`, supplied.ID, supplied.SourcePlatformID, supplied.ExpectedSourceRevision, supplied.DestinationID, supplied.DestinationRevision, supplied.Lease, requireComplete))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if stored.Kind != supplied.Kind || stored.Project != supplied.Project || stored.Environment != supplied.Environment || stored.SourcePlatformID != supplied.SourcePlatformID || stored.TargetPlatformID != supplied.TargetPlatformID || stored.ArtifactID != supplied.ArtifactID || stored.DestinationID != supplied.DestinationID || stored.DestinationRevision != supplied.DestinationRevision || stored.ExpectedSourceRevision != supplied.ExpectedSourceRevision || stored.ExpectedTargetRevision != supplied.ExpectedTargetRevision || stored.Lease != supplied.Lease || !bytes.Equal(stored.AuthorityFingerprint, supplied.AuthorityFingerprint) {
		return ErrConflict
	}
	return nil
}

func (s *Store) neonRecoveryEmptyCleanupFenceTx(ctx context.Context, tx pgx.Tx, supplied platformbackup.Operation) error {
	if !platformbackup.RecoveryCleanupFromContext(ctx) || supplied.Kind != "restore" {
		return ErrConflict
	}
	stored, err := scanPlatformRecovery(tx.QueryRow(ctx, `SELECT `+platformRecoveryQualifiedColumns+` FROM managed_platform_recovery_operations o JOIN managed_platforms p ON p.id=o.target_platform_id AND p.revision=o.expected_target_revision AND p.kind='neon' AND p.deleted_at IS NULL WHERE o.id=$1 AND o.kind='restore' AND o.source_platform_id=$2 AND o.target_platform_id=$3 AND o.expected_source_revision=$4 AND o.expected_target_revision=$5 AND o.artifact_id=$6 AND o.status='running' AND o.lease=$7 AND o.lease_until>=clock_timestamp() AND NOT EXISTS(SELECT 1 FROM managed_platform_neon_recovery_bindings b WHERE b.operation_id=o.id) AND NOT EXISTS(SELECT 1 FROM platform_component_recovery_overrides r WHERE r.recovery_operation_id=o.id) AND NOT EXISTS(SELECT 1 FROM managed_platform_recovery_deployments d WHERE d.operation_id=o.id) AND NOT EXISTS(SELECT 1 FROM managed_platform_recovery_database_state d WHERE d.operation_id=o.id) FOR UPDATE OF o,p`, supplied.ID, supplied.SourcePlatformID, supplied.TargetPlatformID, supplied.ExpectedSourceRevision, supplied.ExpectedTargetRevision, supplied.ArtifactID, supplied.Lease))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(stored.AuthorityFingerprint, supplied.AuthorityFingerprint) {
		return ErrConflict
	}
	return nil
}

func (s *Store) SetPlatformRecoveryCleanup(ctx context.Context, op platformbackup.Operation, required bool) error {
	if op.Kind == "restore" {
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if required && !platformbackup.RecoveryCleanupFromContext(ctx) {
			stored, scanErr := scanPlatformRecovery(tx.QueryRow(ctx, `SELECT `+platformRecoveryQualifiedColumns+` FROM managed_platform_recovery_operations o JOIN managed_platforms p ON p.id=o.target_platform_id AND p.revision=o.expected_target_revision AND p.kind='neon' AND p.deleted_at IS NULL WHERE o.id=$1 AND o.kind='restore' AND o.source_platform_id=$2 AND o.target_platform_id=$3 AND o.expected_source_revision=$4 AND o.expected_target_revision=$5 AND o.artifact_id=$6 AND o.status='running' AND o.lease=$7 AND o.lease_until>=clock_timestamp() FOR UPDATE OF o,p`, op.ID, op.SourcePlatformID, op.TargetPlatformID, op.ExpectedSourceRevision, op.ExpectedTargetRevision, op.ArtifactID, op.Lease))
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return ErrConflict
			}
			if scanErr != nil {
				return scanErr
			}
			if !bytes.Equal(stored.AuthorityFingerprint, op.AuthorityFingerprint) {
				return ErrConflict
			}
			if err = s.reauthorizePlatformRecoveryTx(ctx, tx, stored); err != nil {
				return err
			}
		} else if _, err = s.neonRecoveryFenceTx(ctx, tx, op, platformbackup.RecoveryCleanupFromContext(ctx)); err != nil {
			if required || !errors.Is(err, ErrConflict) {
				return err
			}
			if err = s.neonRecoveryEmptyCleanupFenceTx(ctx, tx, op); err != nil {
				return err
			}
		}
		cleanup := platformbackup.RecoveryCleanupFromContext(ctx)
		r, err := tx.Exec(ctx, `UPDATE managed_platform_recovery_operations SET cleanup_required=$3 WHERE id=$1 AND kind='restore' AND status='running' AND lease=$2 AND lease_until>=clock_timestamp() AND ($3 OR ($4 AND NOT EXISTS(SELECT 1 FROM platform_component_recovery_overrides r WHERE r.recovery_operation_id=$1 AND r.phase NOT IN ('complete','empty_complete','untouched_complete'))) OR (NOT $4 AND cancel_requested=false AND NOT EXISTS(SELECT 1 FROM platform_component_recovery_overrides r WHERE r.recovery_operation_id=$1 AND r.phase<>'confirmed')))`, op.ID, op.Lease, required, cleanup)
		if err != nil {
			return err
		}
		if r.RowsAffected() != 1 {
			return ErrConflict
		}
		return tx.Commit(ctx)
	}
	r, err := s.Pool.Exec(ctx, `UPDATE managed_platform_recovery_operations SET cleanup_required=$3 WHERE id=$1 AND kind='backup' AND status='running' AND lease=$2 AND lease_until>=clock_timestamp()`, op.ID, op.Lease, required)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) PreparePlatformRecoveryDeployment(ctx context.Context, op platformbackup.Operation, platformID, name, uid string, generation int64, replicas int32, target int32, claimedGeneration int64, token string) (int64, int32, error) {
	return s.PreparePlatformRecoveryWorkload(ctx, op, platformID, "deployment", name, uid, generation, replicas, target, claimedGeneration, token)
}
func (s *Store) PreparePlatformRecoveryWorkload(ctx context.Context, op platformbackup.Operation, platformID, kind, name, uid string, generation int64, replicas int32, target int32, claimedGeneration int64, token string) (int64, int32, error) {
	if kind != "deployment" && kind != "statefulset" {
		return 0, 0, ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO managed_platform_recovery_deployments(operation_id,platform_id,workload_kind,deployment_name,deployment_uid,baseline_generation,current_generation,prior_replicas,target_replicas,transition_token) SELECT $1,$2,$11,$3,$4,$5::bigint,$5::bigint,$6::integer,$7::integer,$10 WHERE $5::bigint=$9::bigint AND EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE id=$1 AND lease=$8 AND status='running' AND lease_until>=clock_timestamp()) ON CONFLICT(operation_id,deployment_name) DO NOTHING`, op.ID, platformID, name, uid, generation, replicas, target, op.Lease, claimedGeneration, token, kind); err != nil {
		return 0, 0, err
	}
	var storedPlatform, storedUID string
	var current int64
	var prior int32
	if err = tx.QueryRow(ctx, `UPDATE managed_platform_recovery_deployments SET target_replicas=$3,transition_token=$5,updated_at=now() WHERE operation_id=$1 AND deployment_name=$2 AND workload_kind=$6 AND EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE id=$1 AND lease=$4 AND status='running' AND lease_until>=clock_timestamp()) RETURNING platform_id,deployment_uid,current_generation,prior_replicas`, op.ID, name, target, op.Lease, token, kind).Scan(&storedPlatform, &storedUID, &current, &prior); err != nil {
		return 0, 0, err
	}
	if storedPlatform != platformID || storedUID != uid || current != generation {
		return 0, 0, ErrConflict
	}
	return current, prior, tx.Commit(ctx)
}
func (s *Store) ReconcilePlatformRecoveryDeployment(ctx context.Context, op platformbackup.Operation, name, uid, token string, generation int64, replicas int32) error {
	return s.completePlatformRecoveryWorkloadGeneration(ctx, op, name, uid, generation-1, generation, token, &replicas, nil)
}
func (s *Store) CompletePlatformRecoveryDeployment(ctx context.Context, op platformbackup.Operation, name, uid string, oldGeneration, newGeneration int64) error {
	return s.completePlatformRecoveryWorkloadGeneration(ctx, op, name, uid, oldGeneration, newGeneration, "", nil, nil)
}
func (s *Store) PlatformRecoveryDeploymentGeneration(ctx context.Context, op platformbackup.Operation, name string, baseline int64) (int64, error) {
	var generation int64
	err := s.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT current_generation FROM managed_platform_recovery_deployments WHERE operation_id=$1 AND deployment_name=$2),$3)`, op.ID, name, baseline).Scan(&generation)
	return generation, err
}
func (s *Store) PlatformRecoveryPriorReplicas(ctx context.Context, op platformbackup.Operation, platformID, name, uid string) (int32, error) {
	var prior int32
	err := s.Pool.QueryRow(ctx, `SELECT prior_replicas FROM managed_platform_recovery_deployments WHERE operation_id=$1 AND platform_id=$2 AND deployment_name=$3 AND deployment_uid=$4 AND EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE id=$1 AND lease=$5 AND status='running' AND lease_until>=clock_timestamp())`, op.ID, platformID, name, uid, op.Lease).Scan(&prior)
	return prior, err
}
func (s *Store) SavePlatformRecoveryDatabaseState(ctx context.Context, op platformbackup.Operation, platformID string, readOnly bool) (bool, error) {
	var prior bool
	err := s.Pool.QueryRow(ctx, `INSERT INTO managed_platform_recovery_database_state(operation_id,platform_id,prior_read_only) SELECT $1,$2,$3 WHERE EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE id=$1 AND lease=$4 AND status='running' AND lease_until>=clock_timestamp()) ON CONFLICT(operation_id) DO UPDATE SET platform_id=managed_platform_recovery_database_state.platform_id RETURNING prior_read_only`, op.ID, platformID, readOnly, op.Lease).Scan(&prior)
	return prior, err
}
func (s *Store) PlatformRecoveryDatabaseState(ctx context.Context, op platformbackup.Operation, platformID string) (bool, error) {
	var prior bool
	err := s.Pool.QueryRow(ctx, `SELECT prior_read_only FROM managed_platform_recovery_database_state WHERE operation_id=$1 AND platform_id=$2 AND EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE id=$1 AND lease=$3 AND status='running' AND lease_until>=clock_timestamp())`, op.ID, platformID, op.Lease).Scan(&prior)
	return prior, err
}
func (s *Store) StepPlatformRecovery(ctx context.Context, op platformbackup.Operation, phase, message string) error {
	if len(phase) > 64 || len(message) > 512 {
		return ErrInput
	}
	r, err := s.Pool.Exec(ctx, `UPDATE managed_platform_recovery_operations SET phase=$3,message=$4 WHERE id=$1 AND status='running' AND lease=$2 AND lease_until>=clock_timestamp()`, op.ID, op.Lease, phase, message)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) FinishPlatformRecovery(ctx context.Context, op platformbackup.Operation, status, message string) error {
	if status != "succeeded" && status != "failed" && status != "cancelled" || len(message) > 512 {
		return ErrInput
	}
	cleanupAuthority := platformbackup.RecoveryCleanupFromContext(ctx) && (op.Kind == "backup" || op.Kind == "restore")
	if cleanupAuthority && status == "succeeded" {
		return ErrInput
	}
	backupCleanup := op.Kind == "backup" && cleanupAuthority
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	restoreCleanup := op.Kind == "restore" && platformbackup.RecoveryCleanupFromContext(ctx)
	if backupCleanup {
		if err = s.backupRecoveryCleanupFenceTx(ctx, tx, op, true); err != nil {
			return err
		}
	} else if restoreCleanup {
		if _, err = s.neonRecoveryFenceTx(ctx, tx, op, true); err != nil {
			if !errors.Is(err, ErrConflict) {
				return err
			}
			if err = s.neonRecoveryEmptyCleanupFenceTx(ctx, tx, op); err != nil {
				return err
			}
		}
	} else {
		if err = s.reauthorizePlatformRecoveryTx(ctx, tx, op); err != nil {
			return err
		}
	}
	r, err := tx.Exec(ctx, `UPDATE managed_platform_recovery_operations SET status=$3,phase=$3,message=$4,lease='',lease_until=NULL,finished_at=now() WHERE id=$1 AND status='running' AND lease=$2 AND lease_until>=clock_timestamp() AND cleanup_required=false`, op.ID, op.Lease, status, message)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	if op.Kind == "restore" {
		// Supabase restore leaves the target's clients and gateway stopped.
		// Only a newly reviewed revision may activate them again.
		const heldMessage = "Certificate maintenance is paused for this restore target. Review a new platform revision before activating its services."
		if _, err = tx.Exec(ctx, `UPDATE managed_platform_maintenance m SET phase='restore-isolated',message=$3,updated_at=now()
			FROM managed_platforms p WHERE p.id=$1 AND p.revision=$2 AND p.kind='supabase' AND p.deleted_at IS NULL
			AND m.platform_id=p.id AND m.revision=p.revision`, op.TargetPlatformID, op.ExpectedTargetRevision, heldMessage); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE managed_platforms SET observation=observation || jsonb_build_object('maintenance',jsonb_build_object('status','pending','phase','restore-isolated','message',$3::text,'checked_at',clock_timestamp())),updated_at=now()
			WHERE id=$1 AND revision=$2 AND kind='supabase' AND desired_spec->>'tls_mode'='managed' AND deleted_at IS NULL`, op.TargetPlatformID, op.ExpectedTargetRevision, heldMessage); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) PlatformRecoveryOperation(ctx context.Context, p Principal, id string) (platformbackup.Operation, error) {
	op, err := scanPlatformRecovery(s.Pool.QueryRow(ctx, `SELECT `+platformRecoveryColumns+` FROM managed_platform_recovery_operations WHERE id=$1`, id))
	if err != nil {
		return op, err
	}
	source, err := s.ManagedPlatform(ctx, p, op.SourcePlatformID, false)
	if err != nil {
		return platformbackup.Operation{}, err
	}
	op.Project, op.Environment = source.Project, source.Environment
	op.AuthorityFingerprint = nil
	op.Lease = ""
	return op, nil
}
func (s *Store) PlatformRecoveryOperations(ctx context.Context, p Principal, platformID string) ([]platformbackup.Operation, error) {
	if _, err := s.ManagedPlatform(ctx, p, platformID, false); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+platformRecoveryColumns+` FROM managed_platform_recovery_operations WHERE source_platform_id=$1 OR target_platform_id=$1 ORDER BY created_at DESC,id DESC LIMIT 101`, platformID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []platformbackup.Operation{}
	for rows.Next() {
		op, e := scanPlatformRecovery(rows)
		if e != nil {
			return nil, e
		}
		op.AuthorityFingerprint = nil
		op.Lease = ""
		out = append(out, op)
	}
	if len(out) > 100 {
		return nil, ErrConflict
	}
	return out, rows.Err()
}
func (s *Store) CancelPlatformRecovery(ctx context.Context, p Principal, id string) error {
	op, err := s.PlatformRecoveryOperation(ctx, p, id)
	if err != nil {
		return err
	}
	if !p.AllowsManagedPlatform(op.Project, op.Environment, true) {
		return ErrForbidden
	}
	r, err := s.Pool.Exec(ctx, `UPDATE managed_platform_recovery_operations SET cancel_requested=true WHERE id=$1 AND status IN ('queued','running')`, id)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func scanPlatformRecoveryArtifact(row scanner) (platformbackup.StoredArtifact, error) {
	var a platformbackup.StoredArtifact
	err := row.Scan(&a.ID, &a.DestinationID, &a.ObjectKey, &a.EncryptedBytes, &a.EncryptedSHA256, &a.Manifest)
	return a, err
}
func (s *Store) ReservePlatformRecoveryUpload(ctx context.Context, op platformbackup.Operation, key string) error {
	if op.Kind != "backup" || len(key) < 1 || len(key) > 512 {
		return ErrInput
	}
	r, err := s.Pool.Exec(ctx, `INSERT INTO managed_platform_recovery_uploads(operation_id,destination_id,object_key) SELECT id,destination_id,$3 FROM managed_platform_recovery_operations WHERE id=$1 AND kind='backup' AND status='running' AND lease=$2 AND lease_until>=clock_timestamp() ON CONFLICT(operation_id) DO UPDATE SET object_key=EXCLUDED.object_key WHERE managed_platform_recovery_uploads.destination_id=EXCLUDED.destination_id AND managed_platform_recovery_uploads.artifact_id IS NULL`, op.ID, op.Lease, key)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) StagePlatformRecoveryArtifact(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest, key string, size int64, digest string) (platformbackup.StoredArtifact, error) {
	if err := m.Validate(); err != nil || op.Kind != "backup" || m.PlatformID != op.SourcePlatformID || m.PlatformRevision != op.ExpectedSourceRevision || m.DestinationID != op.DestinationID {
		return platformbackup.StoredArtifact{}, ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return platformbackup.StoredArtifact{}, err
	}
	defer tx.Rollback(ctx)
	id := NewID()
	artifact, err := scanPlatformRecoveryArtifact(tx.QueryRow(ctx, `INSERT INTO managed_platform_recovery_artifacts(id,source_platform_id,source_revision,destination_id,object_key,encrypted_bytes,encrypted_sha256,manifest,manifest_sha256) SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9 FROM managed_platform_recovery_uploads u WHERE u.operation_id=$10 AND u.destination_id=$4 AND u.object_key=$5 AND u.artifact_id IS NULL AND EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE id=$10 AND kind='backup' AND source_platform_id=$2 AND expected_source_revision=$3 AND destination_id=$4 AND status='running' AND lease=$11 AND lease_until>=clock_timestamp()) RETURNING id,destination_id,object_key,encrypted_bytes,encrypted_sha256,manifest`, id, op.SourcePlatformID, op.ExpectedSourceRevision, m.DestinationID, key, size, digest, JSON(m), m.Digest(), op.ID, op.Lease))
	if err != nil {
		return artifact, err
	}
	r, err := tx.Exec(ctx, `UPDATE managed_platform_recovery_uploads SET artifact_id=$2 WHERE operation_id=$1 AND artifact_id IS NULL`, op.ID, id)
	if err != nil {
		return artifact, err
	}
	if r.RowsAffected() != 1 {
		return artifact, ErrConflict
	}
	return artifact, tx.Commit(ctx)
}
func (s *Store) StagedPlatformRecoveryArtifact(ctx context.Context, id string) (platformbackup.StoredArtifact, error) {
	return scanPlatformRecoveryArtifact(s.Pool.QueryRow(ctx, `SELECT id,destination_id,object_key,encrypted_bytes,encrypted_sha256,manifest FROM managed_platform_recovery_artifacts WHERE id=$1 AND published_at IS NULL AND deleted_at IS NULL`, id))
}
func (s *Store) PlatformRecoveryArtifact(ctx context.Context, id string) (platformbackup.StoredArtifact, error) {
	return scanPlatformRecoveryArtifact(s.Pool.QueryRow(ctx, `SELECT id,destination_id,object_key,encrypted_bytes,encrypted_sha256,manifest FROM managed_platform_recovery_artifacts WHERE id=$1 AND published_at IS NOT NULL AND deleted_at IS NULL`, id))
}
func (s *Store) PublishPlatformRecoveryArtifact(ctx context.Context, op platformbackup.Operation, a platformbackup.StoredArtifact) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	r, err := tx.Exec(ctx, `UPDATE managed_platform_recovery_artifacts SET published_at=now() WHERE id=$1 AND published_at IS NULL AND deleted_at IS NULL AND EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE id=$2 AND status='running' AND lease=$3 AND lease_until>=clock_timestamp())`, a.ID, op.ID, op.Lease)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	r, err = tx.Exec(ctx, `UPDATE managed_platform_recovery_operations SET result_artifact_id=$3 WHERE id=$1 AND status='running' AND lease=$2 AND result_artifact_id IS NULL`, op.ID, op.Lease, a.ID)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `DELETE FROM managed_platform_recovery_uploads WHERE operation_id=$1 AND artifact_id=$2`, op.ID, a.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) DiscardPlatformRecoveryArtifact(ctx context.Context, id string) error {
	r, err := s.Pool.Exec(ctx, `UPDATE managed_platform_recovery_artifacts SET deleted_at=now() WHERE id=$1 AND published_at IS NULL AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
