package store

import (
	"bytes"
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (s *Store) ClaimManagedPlatformMaintenance(ctx context.Context) (ManagedPlatformOperation, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ManagedPlatformOperation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044269)"); err != nil {
		return ManagedPlatformOperation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE managed_platform_maintenance SET status='idle',phase='retry-cooldown',message='Maintenance retries are cooling down.',attempt=0,next_attempt_at=clock_timestamp()+interval '6 hours',lease='',lease_until=NULL,updated_at=now()
		WHERE (status='idle' OR status='running' AND lease_until<clock_timestamp()) AND attempt>=16`); err != nil {
		return ManagedPlatformOperation{}, err
	}
	var active int
	if err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM managed_platform_operations WHERE status='running' AND lease_until>=clock_timestamp())+
		(SELECT count(*) FROM managed_platform_maintenance WHERE status='running' AND lease_until>=clock_timestamp())`).Scan(&active); err != nil {
		return ManagedPlatformOperation{}, err
	}
	if active >= 8 {
		if err = tx.Commit(ctx); err != nil {
			return ManagedPlatformOperation{}, err
		}
		return ManagedPlatformOperation{}, pgx.ErrNoRows
	}
	lease := NewID()
	var maintenanceID, operationID string
	err = tx.QueryRow(ctx, `UPDATE managed_platform_maintenance SET status='running',lease=$1,lease_until=clock_timestamp()+interval '30 seconds',attempt=attempt+1,updated_at=now()
		WHERE id=(SELECT m.id FROM managed_platform_maintenance m JOIN managed_platforms p ON p.id=m.platform_id AND p.revision=m.revision AND p.deleted_at IS NULL
			JOIN managed_platform_operations o ON o.id=m.operation_id AND o.platform_id=m.platform_id AND o.revision=m.revision AND o.status='succeeded'
			WHERE (m.status='idle' OR m.status='running' AND m.lease_until<clock_timestamp()) AND m.next_attempt_at<=clock_timestamp() AND m.attempt<16 AND p.status='ready' AND p.desired_spec->>'tls_mode'='managed'
			AND NOT EXISTS(SELECT 1 FROM managed_platform_operations active WHERE active.platform_id=m.platform_id AND active.status IN ('queued','running'))
			AND NOT EXISTS(SELECT 1 FROM managed_platform_recovery_operations recovery WHERE (recovery.source_platform_id=m.platform_id OR recovery.target_platform_id=m.platform_id) AND recovery.status IN ('queued','running'))
			AND NOT (p.kind='supabase' AND EXISTS(SELECT 1 FROM managed_platform_recovery_operations recovery WHERE recovery.kind='restore' AND recovery.target_platform_id=p.id AND recovery.expected_target_revision=p.revision AND recovery.status IN ('succeeded','failed','cancelled')))
			ORDER BY m.next_attempt_at,m.platform_id FOR UPDATE OF m SKIP LOCKED LIMIT 1)
		RETURNING id,operation_id`, lease).Scan(&maintenanceID, &operationID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = tx.Commit(ctx); err != nil {
			return ManagedPlatformOperation{}, err
		}
		return ManagedPlatformOperation{}, pgx.ErrNoRows
	}
	if err != nil {
		return ManagedPlatformOperation{}, err
	}
	op, err := scanManagedPlatformOperation(tx.QueryRow(ctx, "SELECT "+managedPlatformOperationColumns+" FROM managed_platform_operations WHERE id=$1", operationID))
	if err != nil {
		return ManagedPlatformOperation{}, err
	}
	op.Maintenance, op.MaintenanceID, op.Lease = true, maintenanceID, lease
	if err = tx.Commit(ctx); err != nil {
		return ManagedPlatformOperation{}, err
	}
	return op, nil
}

func (s *Store) managedPlatformMaintenanceFenceTx(ctx context.Context, tx pgx.Tx, supplied ManagedPlatformOperation) (ManagedPlatformOperation, string, string, error) {
	var project, environment string
	var stored ManagedPlatformOperation
	err := tx.QueryRow(ctx, `SELECT `+managedPlatformOperationQualifiedColumns+`,p.project,p.environment FROM managed_platform_maintenance m
		JOIN managed_platform_operations o ON o.id=m.operation_id JOIN managed_platforms p ON p.id=m.platform_id AND p.revision=m.revision
		WHERE m.id=$1 AND m.platform_id=$2 AND m.revision=$3 AND m.operation_id=$4 AND m.lease=$5 AND m.status='running' AND m.lease_until>clock_timestamp()
		AND o.status='succeeded' AND p.status='ready' AND p.desired_spec->>'tls_mode'='managed' AND p.deleted_at IS NULL
		AND NOT (p.kind='supabase' AND EXISTS(SELECT 1 FROM managed_platform_recovery_operations recovery WHERE recovery.kind='restore' AND recovery.target_platform_id=p.id AND recovery.expected_target_revision=p.revision AND recovery.status IN ('succeeded','failed','cancelled')))
		FOR UPDATE OF m,p`, supplied.MaintenanceID, supplied.PlatformID, supplied.Revision, supplied.ID, supplied.Lease).
		Scan(&stored.ID, &stored.PlatformID, &stored.Revision, &stored.Kind, &stored.Status, &stored.Phase, &stored.Message, &stored.Spec, &stored.Plan, &stored.EncryptedSnapshot, &stored.Review, &stored.ReviewID, &stored.AuthorityFingerprint, &stored.CreatedAt, &stored.StartedAt, &stored.FinishedAt, &stored.IdentityID, &stored.KeyID, &stored.Lease, &stored.LeaseUntil, &stored.Attempt, &project, &environment)
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedPlatformOperation{}, "", "", ErrConflict
	}
	if err != nil {
		return ManagedPlatformOperation{}, "", "", err
	}
	if stored.Kind != supplied.Kind || !bytes.Equal(JSON(stored.Spec), JSON(supplied.Spec)) || !bytes.Equal(JSON(stored.Plan), JSON(supplied.Plan)) || !bytes.Equal(stored.EncryptedSnapshot, supplied.EncryptedSnapshot) {
		return ManagedPlatformOperation{}, "", "", ErrConflict
	}
	stored.Lease, stored.Maintenance, stored.MaintenanceID = supplied.Lease, true, supplied.MaintenanceID
	return stored, project, environment, nil
}

func (s *Store) recordManagedPlatformMaintenanceStep(ctx context.Context, op ManagedPlatformOperation, status, phase, message string, observation map[string]any) error {
	if status != "queued" && status != "succeeded" && status != "failed" && status != "cancelled" || len(phase) > 64 || len(message) > 512 {
		return ErrInput
	}
	if observation == nil {
		observation = map[string]any{}
	}
	encoded := JSON(observation)
	if len(encoded) > MaxManagedPlatformObservationBytes {
		return ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, _, _, err = s.managedPlatformMaintenanceFenceTx(ctx, tx, op); err != nil {
		return err
	}
	next := "clock_timestamp()+interval '24 hours'"
	if status == "queued" {
		next = "clock_timestamp()+LEAST(interval '1 hour', interval '15 seconds' * power(2, LEAST(attempt,8)))"
	}
	tag, err := tx.Exec(ctx, `UPDATE managed_platform_maintenance SET status='idle',phase=$3,message=$4,observation=$5,next_attempt_at=`+next+`,attempt=CASE WHEN $6='succeeded' THEN 0 ELSE attempt END,lease='',lease_until=NULL,updated_at=now() WHERE id=$1 AND lease=$2 AND status='running' AND lease_until>clock_timestamp()`, op.MaintenanceID, op.Lease, phase, message, encoded, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	maintenanceStatus := "pending"
	if status == "succeeded" {
		maintenanceStatus = "succeeded"
	}
	if status == "failed" || status == "cancelled" {
		maintenanceStatus = "failed"
	}
	tag, err = tx.Exec(ctx, `UPDATE managed_platforms SET observation=observation || jsonb_strip_nulls($3::jsonb) || jsonb_build_object('maintenance',jsonb_build_object('status',$4::text,'phase',$5::text,'message',$6::text,'checked_at',clock_timestamp())),updated_at=now()
		WHERE id=$1 AND revision=$2 AND status='ready' AND desired_spec->>'tls_mode'='managed' AND deleted_at IS NULL`, op.PlatformID, op.Revision, encoded, maintenanceStatus, phase, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}
