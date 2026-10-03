package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

const neonKubernetesClaimComponentPattern = `^(namespace|secret|configmap|pvc|service|deployment|statefulset|networkpolicy)[.][a-z0-9]([-a-z0-9]*[a-z0-9])?([.][a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`

// NeonProviderStateEmpty proves that a leased delete has no native provider
// claims or pending creates in any revision. Kubernetes identities remain for
// the shared, UID-fenced namespace cleanup. Unknown resource kinds or component
// prefixes prevent the shortcut, as do unfinished recovery replacements.
func (s *Store) NeonProviderStateEmpty(ctx context.Context, op ManagedPlatformOperation) (bool, error) {
	if op.Kind != "delete" || op.Spec.Kind != "neon" || op.Maintenance {
		return false, ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if op, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return false, err
	}
	var empty bool
	err = tx.QueryRow(ctx, `SELECT
		NOT EXISTS(SELECT 1 FROM platform_component_resources WHERE platform_id=$1 AND released_at IS NULL
			AND (resource_kind<>'runtime_component' OR component !~ $2))
		AND NOT EXISTS(SELECT 1 FROM platform_resource_intents WHERE platform_id=$1 AND confirmed_at IS NULL AND released_at IS NULL
			AND (resource_kind<>'runtime_component' OR component !~ $2))
		AND NOT EXISTS(SELECT 1 FROM platform_component_recovery_overrides WHERE platform_id=$1
			AND (phase='reserved' OR replacement_resource_id IS NOT NULL AND replacement_released_at IS NULL))`, op.PlatformID, neonKubernetesClaimComponentPattern).Scan(&empty)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return empty, nil
}
