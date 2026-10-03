//go:build hakopod_native_acceptance && linux

package store

import (
	"context"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"
)

// The native fault holds only the revision fence. Controller callbacks may
// acquire their own state lock and commit the resulting placement concurrently.
func (s *Store) WithNativeNeonRevision(ctx context.Context, op ManagedPlatformOperation, apply func() error) error {
	if apply == nil || op.PlatformID == "" || op.ID == "" || op.Revision < 1 || op.Status != "succeeded" || op.Kind != "create" && op.Kind != "update" {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := scanManagedPlatformOperation(tx.QueryRow(ctx, `SELECT `+managedPlatformOperationQualifiedColumns+` FROM managed_platform_operations o JOIN managed_platforms p ON p.id=o.platform_id AND p.revision=o.revision WHERE o.id=$1 AND o.platform_id=$2 AND o.revision=$3 AND o.status='succeeded' AND p.kind='neon' AND p.status='ready' AND p.deleted_at IS NULL FOR SHARE OF p,o`, op.ID, op.PlatformID, op.Revision))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !reflect.DeepEqual(current, op) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if err = apply(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
