package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// NeonTimelineDeletionIntents retains original creation authority when a new
// reviewed delete follows a failed delete. Ordinary creation reads stay scoped
// to the current or immediately preceding revision.
func (s *Store) NeonTimelineDeletionIntents(ctx context.Context, op ManagedPlatformOperation, tenantID, timelineID, parentToken string) ([]PlatformResourceIntent, error) {
	return retryManagedPlatformTransaction(ctx, func() ([]PlatformResourceIntent, error) {
		return s.neonTimelineDeletionIntents(ctx, op, tenantID, timelineID, parentToken)
	})
}

func (s *Store) neonTimelineDeletionIntents(ctx context.Context, op ManagedPlatformOperation, tenantID, timelineID, parentToken string) ([]PlatformResourceIntent, error) {
	if op.Kind != "delete" || op.Spec.Kind != "neon" || op.Maintenance || !managedPlatformID.MatchString(tenantID) || !managedPlatformID.MatchString(timelineID) || parentToken != "" && !managedPlatformID.MatchString(parentToken) {
		return nil, ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if op, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return nil, err
	}
	// Include mismatched pending timelines so corruption cannot look like
	// absence. There can be only one pending child and its confirmed parent.
	rows, err := tx.Query(ctx, `SELECT `+platformResourceIntentColumns+` FROM platform_resource_intents
		WHERE platform_id=$1 AND released_at IS NULL
		AND (id=$2 OR (confirmed_at IS NULL AND (component='timeline' OR resource_kind='neon_timeline')))
		ORDER BY component,id LIMIT 3`, op.PlatformID, parentToken)
	if err != nil {
		return nil, err
	}
	items := make([]PlatformResourceIntent, 0, 2)
	for rows.Next() {
		item, scanErr := scanPlatformResourceIntent(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(items) > 2 {
		return nil, ErrConflict
	}
	var parent, pending *PlatformResourceIntent
	for i := range items {
		item := &items[i]
		if item.PlatformID != op.PlatformID || item.PlatformRevision < 1 || item.PlatformRevision >= op.Revision || !managedPlatformID.MatchString(item.ID) || !managedPlatformID.MatchString(item.OwnerOperationID) {
			return nil, ErrConflict
		}
		if item.ID == parentToken {
			if parent != nil || item.Component != "tenant" || item.Kind != "neon_tenant" || item.ExternalKey != tenantID || item.ConfirmedAt == nil {
				return nil, ErrConflict
			}
			parent = item
		} else {
			if pending != nil || item.Component != "timeline" || item.Kind != "neon_timeline" || item.ExternalKey != tenantID+"/"+timelineID || item.ConfirmedAt != nil {
				return nil, ErrConflict
			}
			pending = item
		}
	}
	if pending != nil && (parent == nil || parent.PlatformRevision != pending.PlatformRevision || parent.OwnerOperationID != pending.OwnerOperationID) {
		return nil, ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return items, nil
}

// CancelNeonTimelineDeletionIntent releases an original pending child only
// after the runtime proves its owned parent and remote data have been deleted.
// The current delete lease and the original creating operation are rechecked.
func (s *Store) CancelNeonTimelineDeletionIntent(ctx context.Context, op ManagedPlatformOperation, intent PlatformResourceIntent) error {
	if op.Kind != "delete" || op.Spec.Kind != "neon" || op.Maintenance || intent.Component != "timeline" || intent.Kind != "neon_timeline" {
		return ErrInput
	}
	return retryManagedPlatformWrite(ctx, func() error {
		return s.cancelPlatformResourceIntent(ctx, op, intent, true)
	})
}
