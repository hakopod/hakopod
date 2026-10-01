package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/jackc/pgx/v5"
)

const platformRecoveryColumns = `id,kind,project,environment,source_platform_id,COALESCE(target_platform_id,''),COALESCE(artifact_id,''),COALESCE(result_artifact_id,''),COALESCE(destination_id,''),COALESCE(destination_revision,0),expected_source_revision,COALESCE(expected_target_revision,0),status,phase,message,authority_fingerprint,lease,cancel_requested,cleanup_required`

func scanPlatformRecovery(row scanner) (platformbackup.Operation, error) {
	var op platformbackup.Operation
	err := row.Scan(&op.ID, &op.Kind, &op.Project, &op.Environment, &op.SourcePlatformID, &op.TargetPlatformID, &op.ArtifactID, &op.ResultArtifactID, &op.DestinationID, &op.DestinationRevision, &op.ExpectedSourceRevision, &op.ExpectedTargetRevision, &op.Status, &op.Phase, &op.Message, &op.AuthorityFingerprint, &op.Lease, &op.CancelRequested, &op.CleanupRequired)
	return op, err
}

func platformRecoveryHash(intent platformbackup.Intent) [32]byte { return sha256.Sum256(JSON(intent)) }

func (s *Store) ManagedPlatformRecoveryContract(ctx context.Context, id string, revision int64) (ManagedPlatform, managedplatform.Plan, error) {
	item, err := scanManagedPlatform(s.Pool.QueryRow(ctx, "SELECT "+managedPlatformColumns+" FROM managed_platforms WHERE id=$1 AND revision=$2 AND kind='supabase' AND deleted_at IS NULL", id, revision))
	if err != nil {
		return item, managedplatform.Plan{}, err
	}
	var plan managedplatform.Plan
	err = s.Pool.QueryRow(ctx, `SELECT resolved_plan FROM managed_platform_operations WHERE platform_id=$1 AND revision=$2 AND status='succeeded' ORDER BY finished_at DESC LIMIT 1`, id, revision).Scan(&plan)
	return item, plan, err
}
func (s *Store) ManagedPlatformRecoveryClaims(ctx context.Context, id string, revision int64) (map[string]PlatformResourceClaim, error) {
	rows, err := s.Pool.Query(ctx, `SELECT platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id,released_at FROM platform_component_resources WHERE platform_id=$1 AND platform_revision=$2 AND released_at IS NULL ORDER BY component,resource_kind LIMIT $3`, id, revision, MaxManagedPlatformResources+1)
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
	if sourceKind != "supabase" || sourceRevision != intent.ExpectedSourceRevision || sourceStatus != "ready" {
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
		if targetKind != "supabase" || targetRevision != intent.ExpectedTargetRevision || targetStatus != "ready" {
			return review, ErrConflict
		}
		var published bool
		if err = tx.QueryRow(ctx, "SELECT published_at IS NOT NULL AND deleted_at IS NULL FROM managed_platform_recovery_artifacts WHERE id=$1 AND source_platform_id=$2 FOR SHARE", intent.ArtifactID, intent.SourcePlatformID).Scan(&published); err != nil || !published {
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
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return op, err
	}
	defer tx.Rollback(ctx)
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
	var sourceStatus string
	if err = tx.QueryRow(ctx, `SELECT revision,status FROM managed_platforms WHERE id=$1 AND project=$2 AND environment=$3 AND kind='supabase' AND deleted_at IS NULL FOR UPDATE`, intent.SourcePlatformID, intent.Project, intent.Environment).Scan(&sourceRevision, &sourceStatus); err != nil {
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
		var targetStatus string
		if err = tx.QueryRow(ctx, `SELECT revision,status FROM managed_platforms WHERE id=$1 AND project=$2 AND environment=$3 AND kind='supabase' AND deleted_at IS NULL FOR UPDATE`, intent.TargetPlatformID, intent.Project, intent.Environment).Scan(&targetRevision, &targetStatus); err != nil {
			return op, err
		}
		if targetRevision != intent.ExpectedTargetRevision || targetStatus != "ready" {
			return op, ErrConflict
		}
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT published_at IS NOT NULL AND deleted_at IS NULL FROM managed_platform_recovery_artifacts WHERE id=$1 AND source_platform_id=$2 FOR SHARE`, intent.ArtifactID, intent.SourcePlatformID).Scan(&valid); err != nil {
			return op, err
		}
		if !valid {
			return op, ErrConflict
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

func (s *Store) ClaimPlatformRecovery(ctx context.Context, lease string) (platformbackup.Operation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return platformbackup.Operation{}, err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `SELECT id FROM managed_platform_recovery_operations WHERE (status='queued' AND next_attempt_at<=now()) OR (status='running' AND lease_until<now()) ORDER BY cleanup_required DESC,next_attempt_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id)
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
	r, err := s.Pool.Exec(ctx, `UPDATE managed_platform_recovery_operations SET lease_until=clock_timestamp()+interval '30 seconds' WHERE id=$1 AND kind='backup' AND source_platform_id=$3 AND expected_source_revision=$4 AND status='running' AND lease=$2 AND lease_until>=clock_timestamp()`, op.ID, op.Lease, op.SourcePlatformID, op.ExpectedSourceRevision)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) SetPlatformRecoveryCleanup(ctx context.Context, op platformbackup.Operation, required bool) error {
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
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO managed_platform_recovery_deployments(operation_id,platform_id,deployment_name,deployment_uid,baseline_generation,current_generation,prior_replicas,target_replicas,transition_token) SELECT $1,$2,$3,$4,$5::bigint,$5::bigint,$6::integer,$7::integer,$10 WHERE $5::bigint=$9::bigint AND EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE id=$1 AND lease=$8 AND status='running' AND lease_until>=clock_timestamp()) ON CONFLICT(operation_id,deployment_name) DO NOTHING`, op.ID, platformID, name, uid, generation, replicas, target, op.Lease, claimedGeneration, token); err != nil {
		return 0, 0, err
	}
	var storedPlatform, storedUID string
	var current int64
	var prior int32
	if err = tx.QueryRow(ctx, `UPDATE managed_platform_recovery_deployments SET target_replicas=$3,transition_token=$5,updated_at=now() WHERE operation_id=$1 AND deployment_name=$2 AND EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE id=$1 AND lease=$4 AND status='running' AND lease_until>=clock_timestamp()) RETURNING platform_id,deployment_uid,current_generation,prior_replicas`, op.ID, name, target, op.Lease, token).Scan(&storedPlatform, &storedUID, &current, &prior); err != nil {
		return 0, 0, err
	}
	if storedPlatform != platformID || storedUID != uid || current != generation {
		return 0, 0, ErrConflict
	}
	return current, prior, tx.Commit(ctx)
}
func (s *Store) ReconcilePlatformRecoveryDeployment(ctx context.Context, op platformbackup.Operation, name, uid, token string, generation int64, replicas int32) error {
	r, err := s.Pool.Exec(ctx, `UPDATE managed_platform_recovery_deployments SET current_generation=$5,updated_at=now() WHERE operation_id=$1 AND deployment_name=$2 AND deployment_uid=$3 AND transition_token=$4 AND target_replicas=$6 AND current_generation+1=$5 AND EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE id=$1 AND lease=$7 AND status='running' AND lease_until>=clock_timestamp())`, op.ID, name, uid, token, generation, replicas, op.Lease)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) CompletePlatformRecoveryDeployment(ctx context.Context, op platformbackup.Operation, name, uid string, oldGeneration, newGeneration int64) error {
	r, err := s.Pool.Exec(ctx, `UPDATE managed_platform_recovery_deployments SET current_generation=$5,updated_at=now() WHERE operation_id=$1 AND deployment_name=$2 AND deployment_uid=$3 AND current_generation=$4 AND EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE id=$1 AND lease=$6 AND status='running' AND lease_until>=clock_timestamp())`, op.ID, name, uid, oldGeneration, newGeneration, op.Lease)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
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
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if !op.CleanupRequired {
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
