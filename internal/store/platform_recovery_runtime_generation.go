package store

import (
	"context"
	"errors"
	"strings"

	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/jackc/pgx/v5"
)

func (s *Store) completePlatformRecoveryWorkloadGeneration(ctx context.Context, op platformbackup.Operation, name, uid string, oldGeneration, newGeneration int64, token string, replicas *int32, mutation *PlatformRuntimeMutation) error {
	if oldGeneration < 1 || newGeneration < oldGeneration || newGeneration > oldGeneration+1 {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var platformID, kind, storedToken, specDigest string
	var transitionGeneration int64
	var complete bool
	var revision int64
	var target int32
	err = tx.QueryRow(ctx, `SELECT d.platform_id,d.workload_kind,p.revision,d.transition_token,d.target_replicas,COALESCE(d.desired_spec_sha256,''),COALESCE(d.transition_generation,0),d.transition_complete
 FROM managed_platform_recovery_deployments d JOIN managed_platform_recovery_operations o ON o.id=d.operation_id
 JOIN managed_platforms p ON p.id=d.platform_id AND p.deleted_at IS NULL
 WHERE d.operation_id=$1 AND d.deployment_name=$2 AND d.deployment_uid=$3 AND d.current_generation=$4
 AND o.lease=$5 AND o.status='running' AND o.lease_until>=clock_timestamp()
 AND ((p.id=o.source_platform_id AND p.revision=o.expected_source_revision) OR (p.id=o.target_platform_id AND p.revision=o.expected_target_revision))
 FOR UPDATE OF d,o,p`, op.ID, name, uid, oldGeneration, op.Lease).Scan(&platformID, &kind, &revision, &storedToken, &target, &specDigest, &transitionGeneration, &complete)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if replicas != nil && (token == "" || storedToken != token || target != *replicas || newGeneration != oldGeneration+1) {
		return ErrConflict
	}
	if mutation != nil {
		if kind != "statefulset" || complete || mutation.Component != kind+"."+name || mutation.Token != storedToken || mutation.SpecSHA256 != specDigest || mutation.NewGeneration != transitionGeneration {
			return ErrConflict
		}
	} else if specDigest != "" {
		return ErrConflict
	}

	claim, _, err := platformRuntimeClaimTx(ctx, tx, platformID, revision, kind+"."+name)
	if err != nil {
		return err
	}
	if claim.ResourceID != uid || claim.ImmutableGeneration != oldGeneration {
		return ErrConflict
	}
	if err = advancePlatformRuntimeGenerationTx(ctx, tx, claim, newGeneration); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE managed_platform_recovery_deployments SET current_generation=$5,transition_complete=true,updated_at=now() WHERE operation_id=$1 AND deployment_name=$2 AND deployment_uid=$3 AND current_generation=$4`, op.ID, name, uid, oldGeneration, newGeneration)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func platformRecoveryRuntimeFenceTx(ctx context.Context, tx pgx.Tx, op platformbackup.Operation, platformID string) (int64, error) {
	var revision int64
	err := tx.QueryRow(ctx, `SELECT p.revision FROM managed_platform_recovery_operations o JOIN managed_platforms p ON p.id=$3 AND p.deleted_at IS NULL WHERE o.id=$1 AND o.lease=$2 AND o.status='running' AND o.lease_until>=clock_timestamp() AND ((p.id=o.source_platform_id AND p.revision=o.expected_source_revision) OR (p.id=o.target_platform_id AND p.revision=o.expected_target_revision)) FOR UPDATE OF o,p`, op.ID, op.Lease, platformID).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrConflict
	}
	return revision, err
}

func (s *Store) PlatformRecoveryWorkloadMutation(ctx context.Context, op platformbackup.Operation, platformID, name string) (PlatformRuntimeMutation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return PlatformRuntimeMutation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = platformRecoveryRuntimeFenceTx(ctx, tx, op, platformID); err != nil {
		return PlatformRuntimeMutation{}, err
	}
	m, err := scanPlatformRuntimeMutation(tx.QueryRow(ctx, `SELECT workload_kind||'.'||deployment_name,deployment_uid,current_generation,transition_generation,transition_token,desired_spec_sha256 FROM managed_platform_recovery_deployments WHERE operation_id=$1 AND platform_id=$2 AND deployment_name=$3 AND workload_kind='statefulset' AND transition_complete=false`, op.ID, platformID, name))
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return m, err
	}
	return m, tx.Commit(ctx)
}

func (s *Store) PreparePlatformRecoveryWorkloadMutation(ctx context.Context, op platformbackup.Operation, platformID string, m PlatformRuntimeMutation, replicas, target int32, claimedGeneration int64) error {
	if !strings.HasPrefix(m.Component, "statefulset.") || !runtimeWorkloadComponent(m.Component) || m.ResourceID == "" || m.OldGeneration < 1 || m.NewGeneration < m.OldGeneration || m.NewGeneration > m.OldGeneration+1 || !neonRecoveryTransitionToken.MatchString(m.Token) || !runtimeMutationDigest.MatchString(m.SpecSHA256) {
		return ErrInput
	}
	name := strings.TrimPrefix(m.Component, "statefulset.")
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	revision, err := platformRecoveryRuntimeFenceTx(ctx, tx, op, platformID)
	if err != nil {
		return err
	}
	claim, _, err := platformRuntimeClaimTx(ctx, tx, platformID, revision, m.Component)
	if err != nil {
		return err
	}
	if claim.ResourceID != m.ResourceID || claim.ImmutableGeneration != m.OldGeneration {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO managed_platform_recovery_deployments(operation_id,platform_id,workload_kind,deployment_name,deployment_uid,baseline_generation,current_generation,prior_replicas,target_replicas,transition_token,desired_spec_sha256,transition_generation,transition_complete)
 SELECT $1,$2,'statefulset',$3,$4,$5,$5,$6,$7,$8,$9,$10,false WHERE $5::bigint=$11::bigint ON CONFLICT(operation_id,deployment_name) DO NOTHING`, op.ID, platformID, name, m.ResourceID, m.OldGeneration, replicas, target, m.Token, m.SpecSHA256, m.NewGeneration, claimedGeneration); err != nil {
		return err
	}
	var stored PlatformRuntimeMutation
	var storedPlatform string
	var complete bool
	err = tx.QueryRow(ctx, `SELECT workload_kind||'.'||deployment_name,deployment_uid,current_generation,COALESCE(transition_generation,0),transition_token,COALESCE(desired_spec_sha256,''),platform_id,transition_complete FROM managed_platform_recovery_deployments WHERE operation_id=$1 AND deployment_name=$2 FOR UPDATE`, op.ID, name).Scan(&stored.Component, &stored.ResourceID, &stored.OldGeneration, &stored.NewGeneration, &stored.Token, &stored.SpecSHA256, &storedPlatform, &complete)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if storedPlatform != platformID || stored.Component != m.Component || stored.ResourceID != m.ResourceID || stored.OldGeneration != m.OldGeneration || !complete && stored != m {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE managed_platform_recovery_deployments SET target_replicas=$3,transition_token=$4,desired_spec_sha256=$5,transition_generation=$6,transition_complete=false,updated_at=now() WHERE operation_id=$1 AND deployment_name=$2`, op.ID, name, target, m.Token, m.SpecSHA256, m.NewGeneration); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CompletePlatformRecoveryWorkloadMutation(ctx context.Context, op platformbackup.Operation, m PlatformRuntimeMutation) error {
	if !strings.HasPrefix(m.Component, "statefulset.") || !runtimeWorkloadComponent(m.Component) || !neonRecoveryTransitionToken.MatchString(m.Token) || !runtimeMutationDigest.MatchString(m.SpecSHA256) {
		return ErrInput
	}
	return s.completePlatformRecoveryWorkloadGeneration(ctx, op, strings.TrimPrefix(m.Component, "statefulset."), m.ResourceID, m.OldGeneration, m.NewGeneration, "", nil, &m)
}

// The runtime calls this only after a metadata compare-and-swap has changed the
// workload resourceVersion without changing its spec or generation. Any delayed
// original write then fails its old resourceVersion precondition.
func (s *Store) AbandonUnappliedPlatformRecoveryWorkloadMutation(ctx context.Context, op platformbackup.Operation, platformID string, m PlatformRuntimeMutation) error {
	if !strings.HasPrefix(m.Component, "statefulset.") || !runtimeWorkloadComponent(m.Component) || !neonRecoveryTransitionToken.MatchString(m.Token) || !runtimeMutationDigest.MatchString(m.SpecSHA256) {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	revision, err := platformRecoveryRuntimeFenceTx(ctx, tx, op, platformID)
	if err != nil {
		return err
	}
	claim, _, err := platformRuntimeClaimTx(ctx, tx, platformID, revision, m.Component)
	if err != nil {
		return err
	}
	if claim.ResourceID != m.ResourceID || claim.ImmutableGeneration != m.OldGeneration {
		return ErrConflict
	}
	result, err := tx.Exec(ctx, `UPDATE managed_platform_recovery_deployments SET transition_complete=true,updated_at=now() WHERE operation_id=$1 AND platform_id=$2 AND workload_kind='statefulset' AND deployment_name=$3 AND deployment_uid=$4 AND current_generation=$5 AND transition_generation=$6 AND transition_token=$7 AND desired_spec_sha256=$8 AND transition_complete=false`, op.ID, platformID, strings.TrimPrefix(m.Component, "statefulset."), m.ResourceID, m.OldGeneration, m.NewGeneration, m.Token, m.SpecSHA256)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}
