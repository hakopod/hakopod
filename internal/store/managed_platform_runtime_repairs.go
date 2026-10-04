package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// PlatformRuntimeRepair retains the original intent and ownership evidence.
// A newly reviewed lifecycle operation may resolve it after the original
// operation has stopped, without reusing the original caller's authority.
type PlatformRuntimeRepair struct {
	Mutation PlatformRuntimeMutation
	Claim    PlatformResourceClaim
}

func terminalPlatformRuntimeRepairTx(ctx context.Context, tx pgx.Tx, op ManagedPlatformOperation, component string) (PlatformRuntimeRepair, error) {
	var result PlatformRuntimeRepair
	var revision int64
	var owner string
	err := tx.QueryRow(ctx, `SELECT m.platform_revision,m.operation_id,m.component,m.resource_id,m.old_generation,m.new_generation,m.transition_token,m.spec_sha256
		FROM managed_platform_runtime_mutations m JOIN managed_platform_operations o ON o.id=m.operation_id
		WHERE m.platform_id=$1 AND m.platform_revision<$2 AND m.component=$3 AND m.completed_at IS NULL
		AND o.status IN ('failed','cancelled') ORDER BY m.platform_revision LIMIT 1 FOR UPDATE OF m,o`, op.PlatformID, op.Revision, component).
		Scan(&revision, &owner, &result.Mutation.Component, &result.Mutation.ResourceID, &result.Mutation.OldGeneration, &result.Mutation.NewGeneration, &result.Mutation.Token, &result.Mutation.SpecSHA256)
	if err != nil {
		return result, err
	}
	claim, _, err := platformRuntimeClaimTx(ctx, tx, op.PlatformID, revision, component)
	if err != nil {
		return result, err
	}
	if claim.OwnerOperationID != owner || claim.ResourceID != result.Mutation.ResourceID || claim.ImmutableGeneration != result.Mutation.OldGeneration {
		return result, ErrConflict
	}
	result.Claim = claim
	return result, nil
}

func (s *Store) PlatformRuntimeMutationForRepair(ctx context.Context, op ManagedPlatformOperation, component string) (PlatformRuntimeRepair, error) {
	var result PlatformRuntimeRepair
	if !runtimeWorkloadComponent(component) {
		return result, ErrInput
	}
	if op.Maintenance || op.Revision < 2 {
		return result, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if _, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return result, err
	}
	result, err = terminalPlatformRuntimeRepairTx(ctx, tx, op, component)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

// The cluster caller must first verify the exact applied intent, or fence the
// unchanged old generation with a resourceVersion-conditional metadata write.
// Recording only an observation of the old generation would leave a delayed
// original Kubernetes write able to commit after this transaction.
func (s *Store) ResolvePlatformRuntimeMutationRepair(ctx context.Context, op ManagedPlatformOperation, repair PlatformRuntimeRepair, applied bool) error {
	if op.Maintenance || repair.Claim.PlatformID != op.PlatformID || repair.Claim.PlatformRevision < 1 || repair.Claim.PlatformRevision >= op.Revision || !runtimeWorkloadComponent(repair.Mutation.Component) {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return err
	}
	stored, err := terminalPlatformRuntimeRepairTx(ctx, tx, op, repair.Mutation.Component)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if stored.Mutation != repair.Mutation || stored.Claim.PlatformID != repair.Claim.PlatformID || stored.Claim.PlatformRevision != repair.Claim.PlatformRevision || stored.Claim.OwnerOperationID != repair.Claim.OwnerOperationID || stored.Claim.ResourceID != repair.Claim.ResourceID || stored.Claim.ImmutableGeneration != repair.Claim.ImmutableGeneration {
		return ErrConflict
	}
	generation := repair.Mutation.OldGeneration
	if applied {
		generation = repair.Mutation.NewGeneration
	}
	if err = advancePlatformRuntimeGenerationTx(ctx, tx, stored.Claim, generation); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE managed_platform_runtime_mutations SET completed_at=now()
		WHERE platform_id=$1 AND platform_revision=$2 AND component=$3 AND operation_id=$4 AND completed_at IS NULL`, op.PlatformID, stored.Claim.PlatformRevision, stored.Claim.Component, stored.Claim.OwnerOperationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'managed_platform.runtime_repair',$3,$4)`, op.IdentityID, op.KeyID, op.PlatformID, JSON(map[string]any{"operation_id": op.ID, "original_operation_id": stored.Claim.OwnerOperationID, "component": stored.Claim.Component, "adopted_result": applied, "generation": generation})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Recovery still requires every runtime intent to be resolved. A newly
// reviewed lifecycle operation may instead repair intents whose owner is
// terminal; active operations and unfinished maintenance remain exclusive.
func rejectPlatformRuntimeLifecycleOverlapTx(ctx context.Context, tx pgx.Tx, platformIDs []string) error {
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_platform_maintenance WHERE platform_id=ANY($1) AND status='running' AND lease_until>=clock_timestamp())
		OR EXISTS(SELECT 1 FROM managed_platform_runtime_mutations m JOIN managed_platform_operations o ON o.id=m.operation_id
		WHERE m.platform_id=ANY($1) AND m.completed_at IS NULL AND o.status NOT IN ('failed','cancelled'))`, platformIDs).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return ErrConflict
	}
	return nil
}
