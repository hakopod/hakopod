package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// PlatformRuntimeMutation records a normalized Kubernetes write before it is
// sent, so a lost response can be recovered without adopting an unknown change.
type PlatformRuntimeMutation struct {
	Component     string
	ResourceID    string
	OldGeneration int64
	NewGeneration int64
	Token         string
	SpecSHA256    string
}

var runtimeMutationToken = regexp.MustCompile(`^[0-9a-f]{32}$`)
var runtimeMutationDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func runtimeWorkloadComponent(component string) bool {
	return managedPlatformComponent.MatchString(component) && (strings.HasPrefix(component, "deployment.") || strings.HasPrefix(component, "statefulset."))
}

const runtimeMutationColumns = `component,resource_id,old_generation,new_generation,transition_token,spec_sha256`

func scanPlatformRuntimeMutation(row pgx.Row) (PlatformRuntimeMutation, error) {
	var m PlatformRuntimeMutation
	err := row.Scan(&m.Component, &m.ResourceID, &m.OldGeneration, &m.NewGeneration, &m.Token, &m.SpecSHA256)
	return m, err
}

func (s *Store) PlatformRuntimeMutation(ctx context.Context, op ManagedPlatformOperation, component string) (PlatformRuntimeMutation, error) {
	if !runtimeWorkloadComponent(component) {
		return PlatformRuntimeMutation{}, ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return PlatformRuntimeMutation{}, err
	}
	defer tx.Rollback(ctx)
	if _, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return PlatformRuntimeMutation{}, err
	}
	m, err := scanPlatformRuntimeMutation(tx.QueryRow(ctx, `SELECT `+runtimeMutationColumns+` FROM managed_platform_runtime_mutations WHERE platform_id=$1 AND platform_revision=$2 AND operation_id=$3 AND component=$4 AND completed_at IS NULL`, op.PlatformID, op.Revision, op.ID, component))
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return m, err
	}
	return m, tx.Commit(ctx)
}

func validPlatformRuntimeMutation(op ManagedPlatformOperation, claim PlatformResourceClaim, m PlatformRuntimeMutation) bool {
	return validPlatformResourceClaim(op, claim) && claim.Kind == "runtime_component" && runtimeWorkloadComponent(claim.Component) &&
		m.Component == claim.Component && m.ResourceID == claim.ResourceID && m.OldGeneration == claim.ImmutableGeneration &&
		m.NewGeneration >= m.OldGeneration && m.NewGeneration <= m.OldGeneration+1 && runtimeMutationToken.MatchString(m.Token) && runtimeMutationDigest.MatchString(m.SpecSHA256)
}

func (s *Store) PreparePlatformRuntimeMutation(ctx context.Context, op ManagedPlatformOperation, claim PlatformResourceClaim, m PlatformRuntimeMutation) error {
	if !validPlatformRuntimeMutation(op, claim, m) {
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
	current, _, err := platformRuntimeClaimTx(ctx, tx, op.PlatformID, op.Revision, claim.Component)
	if err != nil {
		return err
	}
	if current.ResourceID != claim.ResourceID || current.ImmutableGeneration != claim.ImmutableGeneration || current.OwnerOperationID != claim.OwnerOperationID {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO managed_platform_runtime_mutations(platform_id,platform_revision,component,operation_id,resource_id,old_generation,new_generation,transition_token,spec_sha256)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(platform_id,platform_revision,component) DO UPDATE SET
 resource_id=EXCLUDED.resource_id,old_generation=EXCLUDED.old_generation,new_generation=EXCLUDED.new_generation,transition_token=EXCLUDED.transition_token,spec_sha256=EXCLUDED.spec_sha256,completed_at=NULL,created_at=now()
 WHERE managed_platform_runtime_mutations.completed_at IS NOT NULL AND managed_platform_runtime_mutations.operation_id=EXCLUDED.operation_id`, op.PlatformID, op.Revision, m.Component, op.ID, m.ResourceID, m.OldGeneration, m.NewGeneration, m.Token, m.SpecSHA256)
	if err != nil {
		return err
	}
	stored, err := scanPlatformRuntimeMutation(tx.QueryRow(ctx, `SELECT `+runtimeMutationColumns+` FROM managed_platform_runtime_mutations WHERE platform_id=$1 AND platform_revision=$2 AND operation_id=$3 AND component=$4 AND completed_at IS NULL FOR UPDATE`, op.PlatformID, op.Revision, op.ID, m.Component))
	if err != nil {
		return err
	}
	if stored != m {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Store) CompletePlatformRuntimeMutation(ctx context.Context, op ManagedPlatformOperation, claim PlatformResourceClaim, m PlatformRuntimeMutation) error {
	if !validPlatformRuntimeMutation(op, claim, m) {
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
	stored, err := scanPlatformRuntimeMutation(tx.QueryRow(ctx, `SELECT `+runtimeMutationColumns+` FROM managed_platform_runtime_mutations WHERE platform_id=$1 AND platform_revision=$2 AND operation_id=$3 AND component=$4 AND completed_at IS NULL FOR UPDATE`, op.PlatformID, op.Revision, op.ID, m.Component))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if stored != m {
		return ErrConflict
	}
	if err = advancePlatformRuntimeGenerationTx(ctx, tx, claim, m.NewGeneration); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE managed_platform_runtime_mutations SET completed_at=now() WHERE platform_id=$1 AND platform_revision=$2 AND component=$3`, op.PlatformID, op.Revision, m.Component); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// The caller holds the lifecycle or recovery lease in this transaction. Lock
// the original claim and preserve any immutable restore replacement evidence.
func platformRuntimeClaimTx(ctx context.Context, tx pgx.Tx, platformID string, revision int64, component string) (PlatformResourceClaim, string, error) {
	var claim PlatformResourceClaim
	var recoveryID string
	err := tx.QueryRow(ctx, `SELECT b.platform_id,b.platform_revision,b.component,b.resource_kind,
 COALESCE(r.replacement_resource_id,b.resource_id),COALESCE(r.runtime_generation,r.replacement_generation,b.immutable_generation),b.owner_operation_id,COALESCE(r.recovery_operation_id,'')
 FROM platform_component_resources b LEFT JOIN platform_component_recovery_overrides r ON r.platform_id=b.platform_id AND r.platform_revision=b.platform_revision AND r.component=b.component AND r.resource_kind=b.resource_kind AND r.phase='confirmed' AND r.replacement_released_at IS NULL
 WHERE b.platform_id=$1 AND b.platform_revision=$2 AND b.component=$3 AND b.resource_kind='runtime_component' AND b.released_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM platform_component_recovery_overrides pending WHERE pending.platform_id=b.platform_id AND pending.platform_revision=b.platform_revision AND pending.component=b.component AND pending.resource_kind=b.resource_kind AND pending.phase IN ('prior_released','reserved','replacement_released','complete','empty_complete'))
 FOR UPDATE OF b`, platformID, revision, component).Scan(&claim.PlatformID, &claim.PlatformRevision, &claim.Component, &claim.Kind, &claim.ResourceID, &claim.ImmutableGeneration, &claim.OwnerOperationID, &recoveryID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrConflict
	}
	return claim, recoveryID, err
}

func advancePlatformRuntimeGenerationTx(ctx context.Context, tx pgx.Tx, expected PlatformResourceClaim, generation int64) error {
	if !runtimeWorkloadComponent(expected.Component) || generation < expected.ImmutableGeneration || generation > expected.ImmutableGeneration+1 {
		return ErrInput
	}
	current, recoveryID, err := platformRuntimeClaimTx(ctx, tx, expected.PlatformID, expected.PlatformRevision, expected.Component)
	if err != nil {
		return err
	}
	if current.ResourceID != expected.ResourceID || current.ImmutableGeneration != expected.ImmutableGeneration || current.OwnerOperationID != expected.OwnerOperationID {
		return ErrConflict
	}
	if recoveryID != "" {
		result, err := tx.Exec(ctx, `UPDATE platform_component_recovery_overrides SET runtime_generation=$6,updated_at=now() WHERE recovery_operation_id=$1 AND component=$2 AND resource_kind='runtime_component' AND replacement_resource_id=$3 AND COALESCE(runtime_generation,replacement_generation)=$4 AND prior_owner_operation_id=$5 AND phase='confirmed' AND replacement_released_at IS NULL`, recoveryID, expected.Component, expected.ResourceID, expected.ImmutableGeneration, expected.OwnerOperationID, generation)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	}
	result, err := tx.Exec(ctx, `UPDATE platform_component_resources SET immutable_generation=$7 WHERE platform_id=$1 AND platform_revision=$2 AND component=$3 AND resource_kind='runtime_component' AND resource_id=$4 AND immutable_generation=$5 AND owner_operation_id=$6 AND released_at IS NULL`, expected.PlatformID, expected.PlatformRevision, expected.Component, expected.ResourceID, expected.ImmutableGeneration, expected.OwnerOperationID, generation)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

// Admission holds the shared claim lock before platform locks, so a maintenance
// claim cannot race this check and become stranded by a newer operation.
func rejectPlatformRuntimeOverlapTx(ctx context.Context, tx pgx.Tx, platformIDs []string) error {
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_platform_maintenance WHERE platform_id=ANY($1) AND status='running' AND lease_until>=clock_timestamp()) OR EXISTS(SELECT 1 FROM managed_platform_runtime_mutations WHERE platform_id=ANY($1) AND completed_at IS NULL)`, platformIDs).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return ErrConflict
	}
	return nil
}

func rejectUnfinishedPlatformMaintenanceRecoveryTx(ctx context.Context, tx pgx.Tx, platformIDs []string) error {
	var unfinished bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_platform_maintenance m JOIN managed_platforms p ON p.id=m.platform_id AND p.revision=m.revision WHERE m.platform_id=ANY($1) AND m.phase<>'restore-isolated' AND (m.status='running' OR m.attempt>0 OR m.phase='retry-cooldown' OR p.observation->'maintenance'->>'status' IN ('pending','failed')))`, platformIDs).Scan(&unfinished); err != nil {
		return err
	}
	if unfinished {
		return fmt.Errorf("%w: certificate maintenance has not finished; retry recovery after maintenance completes", ErrConflict)
	}
	return nil
}
