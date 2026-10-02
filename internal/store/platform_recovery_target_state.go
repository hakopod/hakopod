package store

import (
	"context"
	"fmt"

	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/jackc/pgx/v5"
)

// MarkPlatformRecoveryTargetIsolated records the externally visible target
// state only after cleanup has fenced the exact restore binding. A failed or
// cancelled restore target must not remain advertised as ready while its
// serving and storage workloads are intentionally stopped.
func (s *Store) MarkPlatformRecoveryTargetIsolated(ctx context.Context, op platformbackup.Operation, message string) error {
	if !platformbackup.RecoveryCleanupFromContext(ctx) || op.Kind != "restore" || len(message) < 1 || len(message) > 512 {
		return ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	binding, err := s.neonRecoveryFenceTx(ctx, tx, op, true)
	if err != nil {
		return err
	}
	observation := map[string]any{"status": "failed", "phase": "recovery-isolated", "message": message, "recovery_operation_id": op.ID}
	tag, err := tx.Exec(ctx, `UPDATE managed_platforms SET status='failed',observation=$4,updated_at=now()
		WHERE id=$1 AND revision=$2 AND kind='neon' AND deleted_at IS NULL
		AND $3=(SELECT operation_id FROM managed_platform_neon_recovery_bindings WHERE target_platform_id=$1 AND target_revision=$2)`, binding.TargetPlatformID, binding.TargetRevision, binding.OperationID, JSON(observation))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: managed platform recovery target changed", ErrConflict)
	}
	return tx.Commit(ctx)
}
