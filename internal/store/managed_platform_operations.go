package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hakopod/hakopod/internal/managedplatform"

	"github.com/jackc/pgx/v5"
)

var managedPlatformComponent = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.:-]{0,61}[a-z0-9])?$`)

const managedPlatformOperationQualifiedColumns = `o.id,o.platform_id,o.revision,o.kind,o.status,o.phase,o.message,o.desired_spec,o.resolved_plan,o.encrypted_snapshot,o.review,o.review_id,o.authority_fingerprint,o.created_at,o.started_at,o.finished_at,o.identity_id,o.key_id,o.lease,o.lease_until,o.attempt`

func (s *Store) managedPlatformOperationFenceTx(ctx context.Context, tx pgx.Tx, supplied ManagedPlatformOperation) (ManagedPlatformOperation, string, string, error) {
	if supplied.Maintenance {
		return s.managedPlatformMaintenanceFenceTx(ctx, tx, supplied)
	}
	var project, environment string
	var stored ManagedPlatformOperation
	row := tx.QueryRow(ctx, `SELECT `+managedPlatformOperationQualifiedColumns+`,p.project,p.environment
		FROM managed_platform_operations o JOIN managed_platforms p ON p.id=o.platform_id
		WHERE o.id=$1 AND o.platform_id=$2 AND o.revision=$3 AND o.lease=$4 AND o.status='running' AND o.lease_until>clock_timestamp()
		AND p.revision=o.revision AND p.deleted_at IS NULL
		FOR UPDATE OF o,p`, supplied.ID, supplied.PlatformID, supplied.Revision, supplied.Lease)
	err := row.Scan(&stored.ID, &stored.PlatformID, &stored.Revision, &stored.Kind, &stored.Status, &stored.Phase, &stored.Message, &stored.Spec, &stored.Plan, &stored.EncryptedSnapshot, &stored.Review, &stored.ReviewID, &stored.AuthorityFingerprint, &stored.CreatedAt, &stored.StartedAt, &stored.FinishedAt, &stored.IdentityID, &stored.KeyID, &stored.Lease, &stored.LeaseUntil, &stored.Attempt, &project, &environment)
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedPlatformOperation{}, "", "", ErrConflict
	}
	if err != nil {
		return ManagedPlatformOperation{}, "", "", err
	}
	if stored.Kind != supplied.Kind || stored.IdentityID != supplied.IdentityID || stored.KeyID != supplied.KeyID || stored.ReviewID != supplied.ReviewID ||
		!bytes.Equal(JSON(stored.Spec), JSON(supplied.Spec)) || !bytes.Equal(JSON(stored.Plan), JSON(supplied.Plan)) ||
		!bytes.Equal(stored.EncryptedSnapshot, supplied.EncryptedSnapshot) || !bytes.Equal(JSON(stored.Review), JSON(supplied.Review)) ||
		!bytes.Equal(stored.AuthorityFingerprint, supplied.AuthorityFingerprint) {
		return ManagedPlatformOperation{}, "", "", ErrConflict
	}
	authority, fingerprint, err := s.managedPlatformAuthorityTx(ctx, tx, stored.KeyID)
	if err != nil {
		return ManagedPlatformOperation{}, "", "", err
	}
	if authority.Principal.ID != stored.IdentityID || !authority.Principal.AllowsManagedPlatform(project, environment, true) || !bytes.Equal(fingerprint, stored.AuthorityFingerprint) {
		return ManagedPlatformOperation{}, "", "", ErrForbidden
	}
	return stored, project, environment, nil
}

func (s *Store) ClaimManagedPlatformOperation(ctx context.Context) (ManagedPlatformOperation, error) {
	return retryManagedPlatformTransaction(ctx, func() (ManagedPlatformOperation, error) {
		return s.claimManagedPlatformOperation(ctx)
	})
}

func (s *Store) claimManagedPlatformOperation(ctx context.Context) (ManagedPlatformOperation, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ManagedPlatformOperation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044269)"); err != nil {
		return ManagedPlatformOperation{}, err
	}
	var active int
	if err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM managed_platform_operations WHERE status='running' AND lease_until>=clock_timestamp())+
		(SELECT count(*) FROM managed_platform_maintenance WHERE status='running' AND lease_until>=clock_timestamp())`).Scan(&active); err != nil {
		return ManagedPlatformOperation{}, err
	}
	if active >= 8 {
		return ManagedPlatformOperation{}, pgx.ErrNoRows
	}
	if _, err = tx.Exec(ctx, `WITH exhausted AS (
		UPDATE managed_platform_operations SET status='failed',phase='attempts-exhausted',message='Reconciliation stopped after 240 bounded attempts.',lease='',lease_until=NULL,finished_at=now()
		WHERE (status='queued' OR status='running' AND lease_until<clock_timestamp()) AND attempt>=240 RETURNING platform_id,revision
	) UPDATE managed_platforms p SET status='failed',updated_at=now() FROM exhausted e WHERE p.id=e.platform_id AND p.revision=e.revision`); err != nil {
		return ManagedPlatformOperation{}, err
	}
	lease := NewID()
	op, err := scanManagedPlatformOperation(tx.QueryRow(ctx, `UPDATE managed_platform_operations SET status='running',lease=$1,lease_until=clock_timestamp()+interval '30 seconds',attempt=attempt+1,started_at=COALESCE(started_at,now())
		WHERE id=(SELECT o.id FROM managed_platform_operations o JOIN managed_platforms p ON p.id=o.platform_id
		WHERE (o.status='queued' OR o.status='running' AND o.lease_until<clock_timestamp()) AND o.next_attempt_at<=clock_timestamp() AND o.attempt<240
		AND p.revision=o.revision AND p.deleted_at IS NULL
		AND NOT EXISTS(SELECT 1 FROM managed_platform_operations active WHERE active.platform_id=o.platform_id AND active.status='running' AND active.lease_until>=clock_timestamp())
		AND NOT EXISTS(SELECT 1 FROM managed_platform_maintenance maintenance WHERE maintenance.platform_id=o.platform_id AND maintenance.status='running' AND maintenance.lease_until>=clock_timestamp())
		AND NOT EXISTS(SELECT 1 FROM managed_platform_recovery_operations recovery WHERE (recovery.source_platform_id=o.platform_id OR recovery.target_platform_id=o.platform_id) AND recovery.status IN ('queued','running'))
		ORDER BY o.next_attempt_at,o.created_at,o.id FOR UPDATE OF o SKIP LOCKED LIMIT 1)
		RETURNING `+managedPlatformOperationColumns, lease))
	if errors.Is(err, pgx.ErrNoRows) {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return op, commitErr
		}
		return op, err
	}
	if err != nil {
		return op, err
	}
	var project, environment string
	if err = tx.QueryRow(ctx, "SELECT project,environment FROM managed_platforms WHERE id=$1 AND revision=$2 AND deleted_at IS NULL FOR SHARE", op.PlatformID, op.Revision).Scan(&project, &environment); err != nil {
		return ManagedPlatformOperation{}, err
	}
	authority, fingerprint, authorityErr := s.managedPlatformAuthorityTx(ctx, tx, op.KeyID)
	if authorityErr != nil || authority.Principal.ID != op.IdentityID || !authority.Principal.AllowsManagedPlatform(project, environment, true) || !bytes.Equal(fingerprint, op.AuthorityFingerprint) {
		if authorityErr != nil && !errors.Is(authorityErr, ErrUnauthorized) && !errors.Is(authorityErr, ErrForbidden) {
			return ManagedPlatformOperation{}, authorityErr
		}
		if _, err = tx.Exec(ctx, "UPDATE managed_platform_operations SET status='failed',phase='authorization-revoked',message='The reviewed authority changed before reconciliation.',lease='',lease_until=NULL,finished_at=now() WHERE id=$1 AND lease=$2", op.ID, lease); err != nil {
			return ManagedPlatformOperation{}, err
		}
		if _, err = tx.Exec(ctx, "UPDATE managed_platforms SET status='failed',updated_at=now() WHERE id=$1 AND revision=$2", op.PlatformID, op.Revision); err != nil {
			return ManagedPlatformOperation{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return ManagedPlatformOperation{}, err
		}
		return ManagedPlatformOperation{}, pgx.ErrNoRows
	}
	if err = tx.Commit(ctx); err != nil {
		return ManagedPlatformOperation{}, err
	}
	return op, nil
}

func (s *Store) CheckManagedPlatformOperation(ctx context.Context, op ManagedPlatformOperation) error {
	return retryManagedPlatformWrite(ctx, func() error { return s.checkManagedPlatformOperation(ctx, op) })
}

func (s *Store) checkManagedPlatformOperation(ctx context.Context, op ManagedPlatformOperation) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if op, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) HeartbeatManagedPlatformOperation(ctx context.Context, op ManagedPlatformOperation) error {
	return retryManagedPlatformWrite(ctx, func() error { return s.heartbeatManagedPlatformOperation(ctx, op) })
}

func (s *Store) heartbeatManagedPlatformOperation(ctx context.Context, op ManagedPlatformOperation) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if op, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return err
	}
	query, id := "UPDATE managed_platform_operations SET lease_until=clock_timestamp()+interval '30 seconds' WHERE id=$1 AND lease=$2 AND status='running' AND lease_until>clock_timestamp()", op.ID
	if op.Maintenance {
		query, id = "UPDATE managed_platform_maintenance SET lease_until=clock_timestamp()+interval '30 seconds',updated_at=now() WHERE id=$1 AND lease=$2 AND status='running' AND lease_until>clock_timestamp()", op.MaintenanceID
	}
	tag, err := tx.Exec(ctx, query, id, op.Lease)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Store) RecordManagedPlatformStep(ctx context.Context, op ManagedPlatformOperation, status, phase, message string, observation map[string]any) error {
	return retryManagedPlatformWrite(ctx, func() error {
		return s.recordManagedPlatformStep(ctx, op, status, phase, message, observation)
	})
}

func (s *Store) recordManagedPlatformStep(ctx context.Context, op ManagedPlatformOperation, status, phase, message string, observation map[string]any) error {
	if op.Maintenance {
		return s.recordManagedPlatformMaintenanceStep(ctx, op, status, phase, message, observation)
	}
	if status != "queued" && status != "succeeded" && status != "failed" && status != "cancelled" || len(phase) > 64 || len(message) > 512 {
		return ErrInput
	}
	if observation == nil {
		observation = map[string]any{}
	}
	encodedObservation := JSON(observation)
	if len(encodedObservation) > MaxManagedPlatformObservationBytes {
		return ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if op, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return err
	}
	if status == "succeeded" && op.Kind == "delete" {
		var owned bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_component_resources WHERE platform_id=$1 AND released_at IS NULL)
			OR EXISTS(SELECT 1 FROM platform_resource_intents WHERE platform_id=$1 AND released_at IS NULL)`, op.PlatformID).Scan(&owned); err != nil {
			return err
		}
		if owned {
			return fmt.Errorf("%w: managed platform resources remain owned", ErrConflict)
		}
	}
	if status == "succeeded" && (s.RequireManagedPlatformAdmission || s.ManagedPlatformCapacityBudget != nil) {
		if err = s.contractManagedPlatformCapacity(ctx, tx, op); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, "UPDATE managed_platform_operations SET status=$3,phase=$4,message=$5,next_attempt_at=clock_timestamp()+interval '3 seconds',lease='',lease_until=NULL,finished_at=CASE WHEN $3='queued' THEN NULL ELSE now() END WHERE id=$1 AND lease=$2 AND status='running' AND lease_until>clock_timestamp()", op.ID, op.Lease, status, phase, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	platformStatus := "pending"
	if status == "succeeded" {
		platformStatus = "ready"
		if op.Kind == "delete" {
			platformStatus = "deleted"
		}
	} else if status == "failed" || status == "cancelled" {
		platformStatus = "failed"
	}
	tag, err = tx.Exec(ctx, "UPDATE managed_platforms SET status=$3,observation=$4,updated_at=now(),deleted_at=CASE WHEN $3='deleted' THEN now() ELSE deleted_at END WHERE id=$1 AND revision=$2 AND deleted_at IS NULL", op.PlatformID, op.Revision, platformStatus, encodedObservation)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if status == "succeeded" && op.Kind != "delete" && (op.Spec.Kind == "neon" || op.Spec.TLSMode == "managed") {
		_, err = tx.Exec(ctx, `INSERT INTO managed_platform_maintenance(id,platform_id,revision,operation_id,next_attempt_at)
			VALUES($1,$2,$3,$4,clock_timestamp()+CASE WHEN $5::text='neon' THEN interval '30 seconds' ELSE interval '12 hours' END)
			ON CONFLICT(platform_id) DO UPDATE SET revision=EXCLUDED.revision,operation_id=EXCLUDED.operation_id,status='idle',phase='scheduled',message='',observation='{}',attempt=0,next_attempt_at=EXCLUDED.next_attempt_at,lease='',lease_until=NULL,updated_at=now()`, NewID(), op.PlatformID, op.Revision, op.ID, op.Spec.Kind)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) FinishManagedPlatformOperation(ctx context.Context, op ManagedPlatformOperation, status, phase, message string, observation map[string]any) error {
	if status != "succeeded" && status != "failed" && status != "cancelled" {
		return ErrInput
	}
	return s.RecordManagedPlatformStep(ctx, op, status, phase, message, observation)
}

func validPlatformResourceClaim(op ManagedPlatformOperation, claim PlatformResourceClaim) bool {
	if claim.PlatformID != op.PlatformID || claim.PlatformRevision != op.Revision || claim.OwnerOperationID != op.ID || claim.ImmutableGeneration < 1 {
		return false
	}
	if !managedPlatformComponent.MatchString(claim.Component) || len(claim.ResourceID) < 1 || len(claim.ResourceID) > 255 {
		return false
	}
	return claim.Kind == "neon_tenant" || claim.Kind == "neon_timeline" || claim.Kind == "runtime_component"
}

func managedPlatformClaimComponentAllowed(op ManagedPlatformOperation, component, kind string) bool {
	components := make(map[string]bool, len(op.Plan.Components))
	storage := make(map[string]bool)
	for _, planned := range op.Plan.Components {
		components[planned.Name] = true
		for _, key := range planned.StorageKeys {
			storage[key] = true
		}
	}
	if op.Spec.Kind == "neon" {
		return managedNeonClaimComponentAllowed(op, components, component, kind)
	}
	if op.Spec.Kind != "supabase" || kind != "runtime_component" {
		return false
	}
	resourceKind, name, ok := strings.Cut(component, ".")
	if !ok || name == "" {
		return false
	}
	if resourceKind == "namespace" {
		return name == op.Plan.Namespace
	}
	if resourceKind == "secret" {
		if managedplatform.ManagedTLSSecretAllowed(op.Spec, name) {
			return true
		}
		for _, ref := range op.Spec.Secrets {
			if name == fmt.Sprintf("%s-r%d", ref.Name, ref.Revision) {
				return true
			}
		}
		return false
	}
	if resourceKind == "configmap" {
		for _, base := range []string{"supabase-database-bootstrap", "supabase-database-credentials", "supabase-envoy-public", "supabase-functions", "supabase-pooler"} {
			if name == fmt.Sprintf("%s-r%d", base, op.Revision) {
				return true
			}
		}
		return false
	}
	if resourceKind == "pvc" {
		name, ok = strings.CutPrefix(name, "supabase-")
		return ok && storage[name]
	}
	if resourceKind == "networkpolicy" {
		if name == "supabase-default-deny-and-internal" || name == "supabase-envoy-ingress" || name == "supabase-edge-approved-https" {
			return true
		}
		for planned := range components {
			if name == "supabase-"+planned+"-internal" {
				return true
			}
		}
		return false
	}
	for planned := range components {
		workload := "supabase-" + planned
		if resourceKind == "deployment" && planned != "database" && name == workload || resourceKind == "statefulset" && planned == "database" && name == workload {
			return true
		}
		if resourceKind == "service" {
			service := map[string]string{"api-gateway": "api-gw", "database": "db", "edge-runtime": "functions", "image-proxy": "imgproxy", "pooler": "supavisor", "postgres-meta": "meta"}[planned]
			if service == "" {
				service = planned
			}
			if name == service {
				return true
			}
		}
	}
	return false
}

func managedNeonClaimComponentAllowed(op ManagedPlatformOperation, components map[string]bool, component, kind string) bool {
	if op.Spec.Neon == nil {
		return false
	}
	if kind == "neon_tenant" {
		return component == "tenant" && components["storage-controller"]
	}
	if kind == "neon_timeline" {
		return component == "timeline" && components["storage-controller"] && components["safekeeper"]
	}
	if kind != "runtime_component" {
		return false
	}
	indexed := func(prefix string, count int) bool {
		for i := 0; i < count; i++ {
			if component == fmt.Sprintf(prefix, i) {
				return true
			}
		}
		return false
	}
	if components["compute"] && indexed("compute-compute-%d", op.Spec.Neon.ComputeReplicas) {
		return true
	}
	if components["pageserver"] && indexed("pageserver-registration-%d", op.Spec.Neon.Pageservers) {
		return true
	}
	if components["safekeeper"] && indexed("safekeeper-registration-%d", op.Spec.Neon.Safekeepers) {
		return true
	}
	resourceKind, name, ok := strings.Cut(component, ".")
	if !ok || name == "" {
		return false
	}
	if resourceKind == "namespace" {
		return name == "managed-platform-"+op.PlatformID
	}
	if resourceKind == "secret" {
		if managedplatform.ManagedTLSSecretAllowed(op.Spec, name) {
			return true
		}
		if components["storage-controller"] && name == managedplatform.NeonControllerCallbackSecretName(op.Revision) {
			return true
		}
		for _, ref := range op.Spec.Secrets {
			if name == fmt.Sprintf("%s-r%d", ref.Name, ref.Revision) {
				return true
			}
		}
		return false
	}
	if resourceKind == "configmap" {
		if component == "configmap.neon-proxy-control-plane-ca-r"+fmt.Sprint(op.Revision) && components["proxy"] {
			return true
		}
		if component == "configmap.neon-controller-database-r"+fmt.Sprint(op.Revision) && components["controller-database"] {
			return true
		}
		if components["pageserver"] && indexed("configmap.neon-pageserver-%d-r"+fmt.Sprint(op.Revision), op.Spec.Neon.Pageservers) {
			return true
		}
		if !components["compute"] || !components["compute-tls"] {
			return false
		}
		for i := 0; i < op.Spec.Neon.ComputeReplicas; i++ {
			base := fmt.Sprintf("neon-compute-%d-tls-r%d", i, op.Revision)
			// Retain admission for owned snapshots created before content addressing.
			if name == base {
				return true
			}
			digest, ok := strings.CutPrefix(name, base+"-")
			if !ok || len(digest) != 16 || strings.IndexFunc(digest, func(r rune) bool {
				return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')
			}) != -1 {
				continue
			}
			return true
		}
		return false
	}
	if resourceKind == "pvc" {
		if name == "neon-controller-database" && components["controller-database"] {
			return true
		}
		return components["pageserver"] && indexed("pvc.neon-pageserver-%d", op.Spec.Neon.Pageservers) ||
			components["safekeeper"] && indexed("pvc.neon-safekeeper-%d", op.Spec.Neon.Safekeepers) ||
			components["compute"] && indexed("pvc.neon-compute-cache-%d", op.Spec.Neon.ComputeReplicas)
	}
	if resourceKind == "service" {
		for _, planned := range []string{"broker", "controller-database", "proxy", "storage-controller"} {
			if name == "neon-"+planned && components[planned] {
				return true
			}
		}
		return components["pageserver"] && indexed("service.neon-pageserver-%d", op.Spec.Neon.Pageservers) ||
			components["safekeeper"] && indexed("service.neon-safekeeper-%d", op.Spec.Neon.Safekeepers) ||
			components["compute"] && (indexed("service.neon-compute-%d", op.Spec.Neon.ComputeReplicas) || indexed("service.neon-compute-%d-control", op.Spec.Neon.ComputeReplicas))
	}
	if resourceKind == "deployment" {
		return name == "neon-broker" && components["broker"] || name == "neon-proxy" && components["proxy"] || name == "neon-storage-controller" && components["storage-controller"]
	}
	if resourceKind == "statefulset" {
		if name == "neon-controller-database" && components["controller-database"] {
			return true
		}
		return components["pageserver"] && indexed("statefulset.neon-pageserver-%d", op.Spec.Neon.Pageservers) ||
			components["safekeeper"] && indexed("statefulset.neon-safekeeper-%d", op.Spec.Neon.Safekeepers) ||
			components["compute"] && indexed("statefulset.neon-compute-%d", op.Spec.Neon.ComputeReplicas)
	}
	if resourceKind == "networkpolicy" {
		for _, policy := range []string{
			"neon-default-deny-and-internal",
			"neon-proxy-ingress",
			"neon-proxy-control-plane-egress",
			"neon-storage-controller-control-ingress",
			"neon-compute-control-ingress",
			"neon-pageserver-control-ingress",
			"neon-safekeeper-control-ingress",
			"neon-broker-internal",
			"neon-controller-database-internal",
			"neon-storage-controller-internal",
			"neon-pageserver-internal",
			"neon-safekeeper-internal",
			"neon-compute-internal",
			"neon-proxy-compute-egress",
			"neon-approved-external-https",
		} {
			if name == policy {
				return true
			}
		}
	}
	return false
}

func managedPlatformClaimAllowedTx(ctx context.Context, tx pgx.Tx, op ManagedPlatformOperation, component, kind string) (bool, error) {
	if managedPlatformClaimComponentAllowed(op, component, kind) {
		return true, nil
	}
	if op.Revision <= 1 {
		return false, nil
	}
	prior, err := scanManagedPlatformOperation(tx.QueryRow(ctx, "SELECT "+managedPlatformOperationColumns+" FROM managed_platform_operations WHERE platform_id=$1 AND revision=$2 FOR SHARE", op.PlatformID, op.Revision-1))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return managedPlatformClaimComponentAllowed(prior, component, kind), nil
}

func scanPlatformResourceClaim(row pgx.Row) (PlatformResourceClaim, error) {
	var claim PlatformResourceClaim
	err := row.Scan(&claim.PlatformID, &claim.PlatformRevision, &claim.Component, &claim.Kind, &claim.ResourceID, &claim.ImmutableGeneration, &claim.OwnerOperationID, &claim.ReleasedAt)
	return claim, err
}

const platformResourceClaimColumns = `platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id,released_at`

func validPlatformResourceIntent(op ManagedPlatformOperation, intent PlatformResourceIntent, requireID bool) bool {
	if intent.PlatformID != op.PlatformID || intent.PlatformRevision != op.Revision || intent.OwnerOperationID != op.ID {
		return false
	}
	if requireID && !managedPlatformID.MatchString(intent.ID) || !requireID && intent.ID != "" {
		return false
	}
	if !managedPlatformComponent.MatchString(intent.Component) || len(intent.ExternalKey) < 1 || len(intent.ExternalKey) > 255 {
		return false
	}
	return intent.Kind == "neon_tenant" || intent.Kind == "neon_timeline" || intent.Kind == "runtime_component"
}

const platformResourceIntentColumns = `id,platform_id,platform_revision,component,resource_kind,external_key,owner_operation_id,created_at,confirmed_at,released_at`

func scanPlatformResourceIntent(row pgx.Row) (PlatformResourceIntent, error) {
	var intent PlatformResourceIntent
	err := row.Scan(&intent.ID, &intent.PlatformID, &intent.PlatformRevision, &intent.Component, &intent.Kind, &intent.ExternalKey, &intent.OwnerOperationID, &intent.CreatedAt, &intent.ConfirmedAt, &intent.ReleasedAt)
	return intent, err
}

// ReservePlatformResourceIntent records permission to attempt one exact
// external create. A reservation remains pending after an ambiguous provider
// response and must never be interpreted as ownership.
func (s *Store) ReservePlatformResourceIntent(ctx context.Context, op ManagedPlatformOperation, requested PlatformResourceIntent) (PlatformResourceIntent, error) {
	return retryManagedPlatformTransaction(ctx, func() (PlatformResourceIntent, error) {
		return s.reservePlatformResourceIntent(ctx, op, requested)
	})
}

func (s *Store) reservePlatformResourceIntent(ctx context.Context, op ManagedPlatformOperation, requested PlatformResourceIntent) (PlatformResourceIntent, error) {
	if !validPlatformResourceIntent(op, requested, false) {
		return PlatformResourceIntent{}, ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return PlatformResourceIntent{}, err
	}
	defer tx.Rollback(ctx)
	if op, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return PlatformResourceIntent{}, err
	}
	if !managedPlatformClaimComponentAllowed(op, requested.Component, requested.Kind) {
		return PlatformResourceIntent{}, ErrInput
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,691))", op.PlatformID); err != nil {
		return PlatformResourceIntent{}, err
	}
	existing, err := scanPlatformResourceIntent(tx.QueryRow(ctx, "SELECT "+platformResourceIntentColumns+" FROM platform_resource_intents WHERE platform_id=$1 AND platform_revision=$2 AND component=$3 AND resource_kind=$4 AND released_at IS NULL FOR UPDATE", requested.PlatformID, requested.PlatformRevision, requested.Component, requested.Kind))
	if err == nil {
		if existing.ExternalKey != requested.ExternalKey || existing.OwnerOperationID != requested.OwnerOperationID {
			return PlatformResourceIntent{}, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return PlatformResourceIntent{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return PlatformResourceIntent{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM platform_component_resources WHERE platform_id=$1 AND released_at IS NULL) +
		(SELECT count(*) FROM platform_resource_intents WHERE platform_id=$1 AND confirmed_at IS NULL AND released_at IS NULL)`, op.PlatformID).Scan(&count); err != nil {
		return PlatformResourceIntent{}, err
	}
	if count >= MaxManagedPlatformResources {
		return PlatformResourceIntent{}, fmt.Errorf("%w: managed platform resource inventory exceeds %d", ErrConflict, MaxManagedPlatformResources)
	}
	requested.ID = NewID()
	requested, err = scanPlatformResourceIntent(tx.QueryRow(ctx, `INSERT INTO platform_resource_intents(id,platform_id,platform_revision,component,resource_kind,external_key,owner_operation_id)
		VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+platformResourceIntentColumns, requested.ID, requested.PlatformID, requested.PlatformRevision, requested.Component, requested.Kind, requested.ExternalKey, requested.OwnerOperationID))
	if err != nil {
		return PlatformResourceIntent{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PlatformResourceIntent{}, err
	}
	return requested, nil
}

// ConfirmPlatformResourceIntent atomically converts one pending intent into
// an exact ownership claim derived from a validated provider response.
func (s *Store) ConfirmPlatformResourceIntent(ctx context.Context, op ManagedPlatformOperation, intent PlatformResourceIntent, claim PlatformResourceClaim) error {
	return retryManagedPlatformWrite(ctx, func() error { return s.confirmPlatformResourceIntent(ctx, op, intent, claim) })
}

func (s *Store) confirmPlatformResourceIntent(ctx context.Context, op ManagedPlatformOperation, intent PlatformResourceIntent, claim PlatformResourceClaim) error {
	if !validPlatformResourceIntent(op, intent, true) || !validPlatformResourceClaim(op, claim) || intent.Component != claim.Component || intent.Kind != claim.Kind {
		return ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if op, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return err
	}
	if !managedPlatformClaimComponentAllowed(op, claim.Component, claim.Kind) {
		return ErrInput
	}
	stored, err := scanPlatformResourceIntent(tx.QueryRow(ctx, "SELECT "+platformResourceIntentColumns+" FROM platform_resource_intents WHERE id=$1 AND platform_id=$2 AND platform_revision=$3 AND component=$4 AND resource_kind=$5 AND external_key=$6 AND owner_operation_id=$7 AND released_at IS NULL FOR UPDATE", intent.ID, intent.PlatformID, intent.PlatformRevision, intent.Component, intent.Kind, intent.ExternalKey, intent.OwnerOperationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if stored.ConfirmedAt != nil {
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_component_resources WHERE intent_id=$1 AND platform_id=$2 AND platform_revision=$3 AND component=$4 AND resource_kind=$5 AND resource_id=$6 AND immutable_generation=$7 AND owner_operation_id=$8 AND released_at IS NULL)`, intent.ID, claim.PlatformID, claim.PlatformRevision, claim.Component, claim.Kind, claim.ResourceID, claim.ImmutableGeneration, claim.OwnerOperationID).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return ErrConflict
		}
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,692))", claim.Kind+"\x1f"+claim.ResourceID); err != nil {
		return err
	}
	var recoveryCollision bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_component_recovery_overrides WHERE resource_kind=$1 AND replacement_resource_id=$2 AND replacement_released_at IS NULL AND adopted_at IS NULL)`, claim.Kind, claim.ResourceID).Scan(&recoveryCollision); err != nil {
		return err
	}
	if recoveryCollision {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform_component_resources(platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id,intent_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, claim.PlatformID, claim.PlatformRevision, claim.Component, claim.Kind, claim.ResourceID, claim.ImmutableGeneration, claim.OwnerOperationID, intent.ID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, "UPDATE platform_resource_intents SET confirmed_at=now() WHERE id=$1 AND confirmed_at IS NULL AND released_at IS NULL", intent.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Store) PlatformResourceIntents(ctx context.Context, op ManagedPlatformOperation, revision int64) ([]PlatformResourceIntent, error) {
	return retryManagedPlatformTransaction(ctx, func() ([]PlatformResourceIntent, error) {
		return s.platformResourceIntents(ctx, op, revision)
	})
}

func (s *Store) platformResourceIntents(ctx context.Context, op ManagedPlatformOperation, revision int64) ([]PlatformResourceIntent, error) {
	if revision < 1 || revision != op.Revision && revision != op.Revision-1 {
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
	rows, err := tx.Query(ctx, `SELECT `+platformResourceIntentColumns+` FROM platform_resource_intents
		WHERE platform_id=$1 AND platform_revision=$2 AND released_at IS NULL
		ORDER BY component,resource_kind LIMIT $3`, op.PlatformID, revision, MaxManagedPlatformResources+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	intents := make([]PlatformResourceIntent, 0)
	for rows.Next() {
		var intent PlatformResourceIntent
		if err = rows.Scan(&intent.ID, &intent.PlatformID, &intent.PlatformRevision, &intent.Component, &intent.Kind, &intent.ExternalKey, &intent.OwnerOperationID, &intent.CreatedAt, &intent.ConfirmedAt, &intent.ReleasedAt); err != nil {
			return nil, err
		}
		intents = append(intents, intent)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(intents) > MaxManagedPlatformResources {
		return nil, ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return intents, nil
}

// CancelPlatformResourceIntent releases a still-pending reservation only.
// Callers must first prove that the external object does not exist.
func (s *Store) CancelPlatformResourceIntent(ctx context.Context, op ManagedPlatformOperation, intent PlatformResourceIntent) error {
	return retryManagedPlatformWrite(ctx, func() error { return s.cancelPlatformResourceIntent(ctx, op, intent) })
}

func (s *Store) cancelPlatformResourceIntent(ctx context.Context, op ManagedPlatformOperation, intent PlatformResourceIntent) error {
	if !managedPlatformID.MatchString(intent.ID) || intent.PlatformID != op.PlatformID || intent.PlatformRevision != op.Revision && intent.PlatformRevision != op.Revision-1 || !managedPlatformComponent.MatchString(intent.Component) || len(intent.ExternalKey) < 1 || len(intent.ExternalKey) > 255 || intent.OwnerOperationID == "" {
		return ErrInput
	}
	if intent.Kind != "neon_tenant" && intent.Kind != "neon_timeline" && intent.Kind != "runtime_component" {
		return ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if op, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return err
	}
	intentOperation, err := scanManagedPlatformOperation(tx.QueryRow(ctx, "SELECT "+managedPlatformOperationColumns+" FROM managed_platform_operations WHERE id=$1 AND platform_id=$2 AND revision=$3 FOR SHARE", intent.OwnerOperationID, intent.PlatformID, intent.PlatformRevision))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if !managedPlatformClaimComponentAllowed(intentOperation, intent.Component, intent.Kind) {
		return ErrInput
	}
	tag, err := tx.Exec(ctx, `UPDATE platform_resource_intents SET released_at=now()
		WHERE id=$1 AND platform_id=$2 AND platform_revision=$3 AND component=$4 AND resource_kind=$5 AND external_key=$6 AND owner_operation_id=$7 AND confirmed_at IS NULL AND released_at IS NULL`, intent.ID, intent.PlatformID, intent.PlatformRevision, intent.Component, intent.Kind, intent.ExternalKey, intent.OwnerOperationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Store) ClaimPlatformResource(ctx context.Context, op ManagedPlatformOperation, claim PlatformResourceClaim) error {
	return retryManagedPlatformWrite(ctx, func() error { return s.claimPlatformResource(ctx, op, claim) })
}

func (s *Store) claimPlatformResource(ctx context.Context, op ManagedPlatformOperation, claim PlatformResourceClaim) error {
	if !validPlatformResourceClaim(op, claim) {
		return ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if op, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return err
	}
	if !managedPlatformClaimComponentAllowed(op, claim.Component, claim.Kind) {
		return ErrInput
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,691))", op.PlatformID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,692))", claim.Kind+"\x1f"+claim.ResourceID); err != nil {
		return err
	}
	var recoveryCollision bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_component_recovery_overrides WHERE resource_kind=$1 AND replacement_resource_id=$2 AND replacement_released_at IS NULL AND adopted_at IS NULL)`, claim.Kind, claim.ResourceID).Scan(&recoveryCollision); err != nil {
		return err
	}
	if recoveryCollision {
		return ErrConflict
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM platform_component_resources WHERE platform_id=$1 AND released_at IS NULL) +
		(SELECT count(*) FROM platform_resource_intents WHERE platform_id=$1 AND confirmed_at IS NULL AND released_at IS NULL)`, op.PlatformID).Scan(&count); err != nil {
		return err
	}
	var existing PlatformResourceClaim
	existing, err = scanPlatformResourceClaim(tx.QueryRow(ctx, "SELECT "+platformResourceClaimColumns+" FROM platform_component_resources WHERE platform_id=$1 AND platform_revision=$2 AND component=$3 AND resource_kind=$4 FOR UPDATE", claim.PlatformID, claim.PlatformRevision, claim.Component, claim.Kind))
	if err == nil {
		if existing.ResourceID != claim.ResourceID || existing.ImmutableGeneration != claim.ImmutableGeneration || existing.OwnerOperationID != claim.OwnerOperationID || existing.ReleasedAt != nil {
			return ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if count >= MaxManagedPlatformResources {
		return fmt.Errorf("%w: managed platform resource inventory exceeds %d", ErrConflict, MaxManagedPlatformResources)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO platform_component_resources(platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id) VALUES($1,$2,$3,$4,$5,$6,$7)", claim.PlatformID, claim.PlatformRevision, claim.Component, claim.Kind, claim.ResourceID, claim.ImmutableGeneration, claim.OwnerOperationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Asking for the prior revision includes older claims still owned after a
// failed operation. The same resource bound and current-operation fence apply.
func (s *Store) PlatformResourceClaims(ctx context.Context, op ManagedPlatformOperation, revision int64) ([]PlatformResourceClaim, error) {
	return retryManagedPlatformTransaction(ctx, func() ([]PlatformResourceClaim, error) {
		return s.platformResourceClaims(ctx, op, revision)
	})
}

func (s *Store) platformResourceClaims(ctx context.Context, op ManagedPlatformOperation, revision int64) ([]PlatformResourceClaim, error) {
	if revision < 1 || revision != op.Revision && revision != op.Revision-1 {
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
	rows, err := tx.Query(ctx, `SELECT `+platformResourceClaimColumns+` FROM effective_platform_component_resources
		WHERE platform_id=$1 AND (platform_revision=$2 OR $2=$3-1 AND platform_revision<$2)
		ORDER BY component,resource_kind LIMIT $4`, op.PlatformID, revision, op.Revision, MaxManagedPlatformResources+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	claims := make([]PlatformResourceClaim, 0)
	for rows.Next() {
		var claim PlatformResourceClaim
		if err = rows.Scan(&claim.PlatformID, &claim.PlatformRevision, &claim.Component, &claim.Kind, &claim.ResourceID, &claim.ImmutableGeneration, &claim.OwnerOperationID, &claim.ReleasedAt); err != nil {
			return nil, err
		}
		claims = append(claims, claim)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(claims) > MaxManagedPlatformResources {
		return nil, ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return claims, nil
}

func (s *Store) VerifyPlatformResourceClaim(ctx context.Context, op ManagedPlatformOperation, claim PlatformResourceClaim) error {
	return retryManagedPlatformWrite(ctx, func() error { return s.verifyPlatformResourceClaim(ctx, op, claim) })
}

func (s *Store) verifyPlatformResourceClaim(ctx context.Context, op ManagedPlatformOperation, claim PlatformResourceClaim) error {
	if !validPlatformResourceClaim(op, claim) {
		return ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if op, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return err
	}
	allowed, err := managedPlatformClaimAllowedTx(ctx, tx, op, claim.Component, claim.Kind)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrInput
	}
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM effective_platform_component_resources
		WHERE platform_id=$1 AND platform_revision=$2 AND component=$3 AND resource_kind=$4
		AND resource_id=$5 AND immutable_generation=$6 AND owner_operation_id=$7)`, claim.PlatformID, claim.PlatformRevision, claim.Component, claim.Kind, claim.ResourceID, claim.ImmutableGeneration, claim.OwnerOperationID).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Store) AdvancePlatformResourceClaim(ctx context.Context, op ManagedPlatformOperation, prior PlatformResourceClaim) error {
	return retryManagedPlatformWrite(ctx, func() error { return s.advancePlatformResourceClaim(ctx, op, prior) })
}

func (s *Store) advancePlatformResourceClaim(ctx context.Context, op ManagedPlatformOperation, prior PlatformResourceClaim) error {
	if prior.PlatformID != op.PlatformID || prior.PlatformRevision < 1 || prior.PlatformRevision >= op.Revision || prior.ImmutableGeneration < 1 || !managedPlatformComponent.MatchString(prior.Component) || len(prior.ResourceID) < 1 || len(prior.ResourceID) > 255 || prior.OwnerOperationID == "" {
		return ErrInput
	}
	if prior.Kind != "neon_tenant" && prior.Kind != "neon_timeline" && prior.Kind != "runtime_component" {
		return ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if op, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return err
	}
	priorOperation, err := scanManagedPlatformOperation(tx.QueryRow(ctx, "SELECT "+managedPlatformOperationColumns+" FROM managed_platform_operations WHERE id=$1 AND platform_id=$2 AND revision=$3 FOR SHARE", prior.OwnerOperationID, prior.PlatformID, prior.PlatformRevision))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if !managedPlatformClaimComponentAllowed(priorOperation, prior.Component, prior.Kind) {
		return ErrInput
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_platform_runtime_mutations WHERE platform_id=$1 AND platform_revision=$2 AND component=$3 AND completed_at IS NULL)`, prior.PlatformID, prior.PlatformRevision, prior.Component).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return ErrConflict
	}
	var baseResourceID, sourceResourceID, overrideOperationID string
	var baseGeneration, sourceGeneration int64
	err = tx.QueryRow(ctx, `SELECT e.base_resource_id,e.base_generation,e.source_resource_id,e.source_generation,COALESCE(e.recovery_operation_id,'') FROM effective_platform_component_resources e
		JOIN platform_component_resources b USING(platform_id,platform_revision,component,resource_kind)
		WHERE e.platform_id=$1 AND e.platform_revision=$2 AND e.component=$3 AND e.resource_kind=$4 AND e.owner_operation_id=$5
		AND e.resource_id=$6 AND e.immutable_generation=$7 FOR UPDATE OF b`, prior.PlatformID, prior.PlatformRevision, prior.Component, prior.Kind, prior.OwnerOperationID, prior.ResourceID, prior.ImmutableGeneration).Scan(&baseResourceID, &baseGeneration, &sourceResourceID, &sourceGeneration, &overrideOperationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if overrideOperationID == "" && baseResourceID == prior.ResourceID && baseGeneration == prior.ImmutableGeneration {
		tag, err := tx.Exec(ctx, `UPDATE platform_component_resources SET platform_revision=$1,owner_operation_id=$2
			WHERE platform_id=$3 AND platform_revision=$4 AND component=$5 AND resource_kind=$6 AND resource_id=$7 AND immutable_generation=$8 AND owner_operation_id=$9 AND released_at IS NULL`, op.Revision, op.ID, prior.PlatformID, prior.PlatformRevision, prior.Component, prior.Kind, prior.ResourceID, prior.ImmutableGeneration, prior.OwnerOperationID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			var valid bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_component_resources WHERE platform_id=$1 AND platform_revision=$2 AND component=$3 AND resource_kind=$4 AND resource_id=$5 AND immutable_generation=$6 AND owner_operation_id=$7 AND released_at IS NULL)`, op.PlatformID, op.Revision, prior.Component, prior.Kind, prior.ResourceID, prior.ImmutableGeneration, op.ID).Scan(&valid); err != nil {
				return err
			}
			if !valid {
				return ErrConflict
			}
		}
		if err = propagateNeonRecoveryLineageTx(ctx, tx, prior.PlatformID, prior.PlatformRevision, op.Revision, ""); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	tag, err := tx.Exec(ctx, `UPDATE platform_component_resources SET released_at=now() WHERE platform_id=$1 AND platform_revision=$2 AND component=$3 AND resource_kind=$4 AND resource_id=$5 AND immutable_generation=$6 AND owner_operation_id=$7 AND released_at IS NULL`, prior.PlatformID, prior.PlatformRevision, prior.Component, prior.Kind, baseResourceID, baseGeneration, prior.OwnerOperationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform_component_resources(platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, op.PlatformID, op.Revision, prior.Component, prior.Kind, prior.ResourceID, prior.ImmutableGeneration, op.ID); err != nil {
		return err
	}
	if overrideOperationID != "" {
		tag, err = tx.Exec(ctx, `UPDATE platform_component_recovery_overrides SET phase='adopted',adopted_at=now(),updated_at=now() WHERE recovery_operation_id=$1 AND platform_id=$2 AND platform_revision=$3 AND component=$4 AND resource_kind=$5 AND prior_owner_operation_id=$6 AND replacement_resource_id=$7 AND COALESCE(runtime_generation,replacement_generation)=$8 AND phase='confirmed' AND replacement_released_at IS NULL`, overrideOperationID, prior.PlatformID, prior.PlatformRevision, prior.Component, prior.Kind, prior.OwnerOperationID, sourceResourceID, sourceGeneration)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
	}
	if err = propagateNeonRecoveryLineageTx(ctx, tx, prior.PlatformID, prior.PlatformRevision, op.Revision, overrideOperationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// NeonRecoveryBindingForLifecycle resolves restored identity before an update
// adopts its first resource. The live operation fence limits the lookup to
// the current revision and its immediate predecessor on this platform.
func (s *Store) NeonRecoveryBindingForLifecycle(ctx context.Context, op ManagedPlatformOperation) (NeonRecoveryBinding, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return NeonRecoveryBinding{}, err
	}
	defer tx.Rollback(ctx)
	if op, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return NeonRecoveryBinding{}, err
	}
	var binding NeonRecoveryBinding
	err = tx.QueryRow(ctx, `SELECT b.operation_id,b.target_platform_id,b.target_revision,b.artifact_id,b.manifest_sha256,b.tenant_id,b.timeline_id,b.tenant_generation,b.timeline_generation,b.staging_prefix
		FROM managed_platform_neon_recovery_lineage l
		JOIN managed_platform_neon_recovery_bindings b ON b.operation_id=l.recovery_operation_id AND b.target_platform_id=l.platform_id
		JOIN managed_platform_recovery_operations r ON r.id=b.operation_id AND r.status='succeeded'
		WHERE l.platform_id=$1 AND (l.platform_revision=$2 OR ($3 IN ('update','delete') AND l.platform_revision=$2-1))
		ORDER BY l.platform_revision DESC LIMIT 1`, op.PlatformID, op.Revision, op.Kind).Scan(&binding.OperationID, &binding.TargetPlatformID, &binding.TargetRevision, &binding.ArtifactID, &binding.ManifestSHA256, &binding.TenantID, &binding.TimelineID, &binding.TenantGeneration, &binding.TimelineGeneration, &binding.StagingPrefix)
	if err != nil {
		return NeonRecoveryBinding{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return NeonRecoveryBinding{}, err
	}
	return binding, nil
}

func propagateNeonRecoveryLineageTx(ctx context.Context, tx pgx.Tx, platformID string, fromRevision, toRevision int64, recoveryOperationID string) error {
	var origin string
	var err error
	if recoveryOperationID == "" {
		err = tx.QueryRow(ctx, `SELECT l.recovery_operation_id FROM managed_platform_neon_recovery_lineage l JOIN managed_platform_neon_recovery_bindings b ON b.operation_id=l.recovery_operation_id AND b.target_platform_id=l.platform_id WHERE l.platform_id=$1 AND l.platform_revision=$2`, platformID, fromRevision).Scan(&origin)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
	} else {
		err = tx.QueryRow(ctx, `SELECT operation_id FROM managed_platform_neon_recovery_bindings WHERE operation_id=$1 AND target_platform_id=$2 AND target_revision=$3`, recoveryOperationID, platformID, fromRevision).Scan(&origin)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO managed_platform_neon_recovery_lineage(platform_id,platform_revision,recovery_operation_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, platformID, toRevision, origin); err != nil {
		return err
	}
	var stored string
	if err = tx.QueryRow(ctx, `SELECT recovery_operation_id FROM managed_platform_neon_recovery_lineage WHERE platform_id=$1 AND platform_revision=$2`, platformID, toRevision).Scan(&stored); err != nil {
		return err
	}
	if stored != origin {
		return ErrConflict
	}
	return nil
}

func (s *Store) ReleasePlatformResourceClaim(ctx context.Context, op ManagedPlatformOperation, claim PlatformResourceClaim) error {
	return retryManagedPlatformWrite(ctx, func() error { return s.releasePlatformResourceClaim(ctx, op, claim) })
}

func (s *Store) releasePlatformResourceClaim(ctx context.Context, op ManagedPlatformOperation, claim PlatformResourceClaim) error {
	if !validPlatformResourceClaim(op, claim) {
		return ErrInput
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if op, _, _, err = s.managedPlatformOperationFenceTx(ctx, tx, op); err != nil {
		return err
	}
	allowed, err := managedPlatformClaimAllowedTx(ctx, tx, op, claim.Component, claim.Kind)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrInput
	}
	var intentID, baseResourceID, sourceResourceID, recoveryOperationID string
	var baseGeneration, sourceGeneration int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(e.intent_id,''),e.base_resource_id,e.base_generation,e.source_resource_id,e.source_generation,COALESCE(e.recovery_operation_id,'') FROM effective_platform_component_resources e
		JOIN platform_component_resources b USING(platform_id,platform_revision,component,resource_kind)
		WHERE e.platform_id=$1 AND e.platform_revision=$2 AND e.component=$3 AND e.resource_kind=$4 AND e.owner_operation_id=$7
		AND e.resource_id=$5 AND e.immutable_generation=$6 FOR UPDATE OF b`, claim.PlatformID, claim.PlatformRevision, claim.Component, claim.Kind, claim.ResourceID, claim.ImmutableGeneration, claim.OwnerOperationID).Scan(&intentID, &baseResourceID, &baseGeneration, &sourceResourceID, &sourceGeneration, &recoveryOperationID); errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE platform_component_resources SET released_at=now()
		WHERE platform_id=$1 AND platform_revision=$2 AND component=$3 AND resource_kind=$4 AND resource_id=$5 AND immutable_generation=$6 AND owner_operation_id=$7 AND released_at IS NULL`, claim.PlatformID, claim.PlatformRevision, claim.Component, claim.Kind, baseResourceID, baseGeneration, claim.OwnerOperationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if recoveryOperationID != "" {
		tag, err = tx.Exec(ctx, `UPDATE platform_component_recovery_overrides SET phase='replacement_released',replacement_released_at=now(),updated_at=now() WHERE recovery_operation_id=$1 AND platform_id=$2 AND platform_revision=$3 AND component=$4 AND resource_kind=$5 AND replacement_resource_id=$6 AND COALESCE(runtime_generation,replacement_generation)=$7 AND phase='confirmed' AND replacement_released_at IS NULL`, recoveryOperationID, claim.PlatformID, claim.PlatformRevision, claim.Component, claim.Kind, sourceResourceID, sourceGeneration)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
	}
	if intentID != "" {
		tag, err = tx.Exec(ctx, "UPDATE platform_resource_intents SET released_at=now() WHERE id=$1 AND confirmed_at IS NOT NULL AND released_at IS NULL", intentID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
	}
	return tx.Commit(ctx)
}
