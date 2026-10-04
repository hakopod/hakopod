package store

import (
	"context"
	"errors"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/jackc/pgx/v5"
)

func observeNeonTenantPlacementTx(ctx context.Context, tx pgx.Tx, op ManagedPlatformOperation, prior PlatformResourceClaim, observedID string, generation int64) error {
	if prior.PlatformID != op.PlatformID || prior.PlatformRevision > op.Revision || prior.PlatformRevision < 1 || prior.Component != "tenant" || prior.Kind != "neon_tenant" || prior.OwnerOperationID == "" || managedplatform.ValidateNeonTenantPlacement(prior.ResourceID, prior.ImmutableGeneration, observedID, generation) != nil {
		return ErrInput
	}
	var sourceID string
	var sourceGeneration int64
	err := tx.QueryRow(ctx, `SELECT e.source_resource_id,e.source_generation FROM effective_platform_component_resources e
 JOIN platform_component_resources b USING(platform_id,platform_revision,component,resource_kind)
 WHERE e.platform_id=$1 AND e.platform_revision=$2 AND e.component=$3 AND e.resource_kind=$4 AND e.owner_operation_id=$5 AND e.resource_id=$6 AND e.immutable_generation=$7
 FOR UPDATE OF b`, prior.PlatformID, prior.PlatformRevision, prior.Component, prior.Kind, prior.OwnerOperationID, prior.ResourceID, prior.ImmutableGeneration).Scan(&sourceID, &sourceGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO managed_platform_neon_tenant_placements(platform_id,platform_revision,component,resource_kind,owner_operation_id,source_resource_id,source_generation,observed_resource_id,observed_generation)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
 ON CONFLICT(platform_id,platform_revision,component,resource_kind) DO UPDATE SET owner_operation_id=EXCLUDED.owner_operation_id,source_resource_id=EXCLUDED.source_resource_id,source_generation=EXCLUDED.source_generation,observed_resource_id=EXCLUDED.observed_resource_id,observed_generation=EXCLUDED.observed_generation,updated_at=now()`, prior.PlatformID, prior.PlatformRevision, prior.Component, prior.Kind, prior.OwnerOperationID, sourceID, sourceGeneration, observedID, generation)
	return err
}
