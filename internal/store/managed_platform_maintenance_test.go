package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/jackc/pgx/v5"
)

func enableManagedTLSFixture(item *ManagedPlatform) {
	item.Spec.TLSMode = "managed"
	item.Spec.Secrets = map[string]managedplatform.SecretReference{}
	required := managedplatform.NeonSecretKeys()
	if item.Spec.Kind == "supabase" {
		required = managedplatform.SupabaseRequiredSecretKeys()
	}
	for _, key := range item.Spec.UserSecretKeys(required) {
		item.Spec.Secrets[key] = managedplatform.SecretReference{Name: "fixture-" + key, Revision: 1}
	}
}

func TestManagedPlatformMaintenanceUsesSeparateLeaseAfterAuthorityRevocation(t *testing.T) {
	s, principal, item, plan := managedPlatformFixture(t)
	enableManagedTLSFixture(&item)
	ctx := context.Background()
	review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
	accepted, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-maintenance-fixture"), review, 0, "maintenance-create", "create")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, claimed, "succeeded", "ready", "", map[string]any{"status": "ready"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE managed_platforms SET observation='{"status":"ready","namespace_uid":"namespace-fixture","tls":{"serial":"prior"}}' WHERE id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_platform_maintenance SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE platform_id=$1", item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE api_keys SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", principal.KeyID); err != nil {
		t.Fatal(err)
	}
	maintenance, err := s.ClaimManagedPlatformMaintenance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !maintenance.Maintenance || maintenance.MaintenanceID == "" || maintenance.ID != accepted.ID || maintenance.Lease == "" {
		t.Fatal("maintenance did not retain original operation with a separate lease")
	}
	if err = s.CheckManagedPlatformOperation(ctx, maintenance); err != nil {
		t.Fatal(err)
	}
	if err = s.HeartbeatManagedPlatformOperation(ctx, maintenance); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, maintenance, "queued", "retry", "Managed platform reconciliation will retry.", map[string]any{"tls": nil}); err != nil {
		t.Fatal(err)
	}
	var namespaceUID, serial, maintenanceStatus string
	if err = s.Pool.QueryRow(ctx, `SELECT observation->>'namespace_uid',observation->'tls'->>'serial',observation->'maintenance'->>'status' FROM managed_platforms WHERE id=$1`, item.ID).Scan(&namespaceUID, &serial, &maintenanceStatus); err != nil {
		t.Fatal(err)
	}
	if namespaceUID != "namespace-fixture" || serial != "prior" || maintenanceStatus != "pending" {
		t.Fatalf("retry observation lost prior facts: %q %q %q", namespaceUID, serial, maintenanceStatus)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_platform_maintenance SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE platform_id=$1", item.ID); err != nil {
		t.Fatal(err)
	}
	maintenance, err = s.ClaimManagedPlatformMaintenance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, maintenance, "succeeded", "tls-current", "", map[string]any{"status": "ready", "tls": "current"}); err != nil {
		t.Fatal(err)
	}
	var status, phase string
	if err = s.Pool.QueryRow(ctx, "SELECT status,phase FROM managed_platform_operations WHERE id=$1", accepted.ID).Scan(&status, &phase); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || phase != "ready" {
		t.Fatalf("maintenance rewrote user operation: %s %s", status, phase)
	}
	if _, err = s.ClaimManagedPlatformMaintenance(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("completed maintenance was immediately reclaimed", err)
	}
}

func TestManagedPlatformMaintenanceExhaustionPersistsCooldown(t *testing.T) {
	s, principal, item, plan := managedPlatformFixture(t)
	enableManagedTLSFixture(&item)
	ctx := context.Background()
	review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-maintenance-cooldown"), review, 0, "maintenance-cooldown", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, op, "succeeded", "ready", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_platform_maintenance SET attempt=16,next_attempt_at=clock_timestamp()-interval '1 second' WHERE platform_id=$1", item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimManagedPlatformMaintenance(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("exhausted maintenance was claimed", err)
	}
	var attempt int
	var phase string
	var cooled bool
	if err = s.Pool.QueryRow(ctx, "SELECT attempt,phase,next_attempt_at>clock_timestamp() FROM managed_platform_maintenance WHERE platform_id=$1", item.ID).Scan(&attempt, &phase, &cooled); err != nil {
		t.Fatal(err)
	}
	if attempt != 0 || phase != "retry-cooldown" || !cooled {
		t.Fatalf("cooldown did not persist: %d %s %t", attempt, phase, cooled)
	}
}

func TestManagedPlatformOperatorTLSDoesNotScheduleMaintenance(t *testing.T) {
	s, principal, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-operator-tls"), review, 0, "operator-tls-create", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, op, "succeeded", "ready", "", nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM managed_platform_maintenance WHERE platform_id=$1", item.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("operator-managed TLS scheduled automatic maintenance")
	}
}

func TestManagedPlatformMaintenanceRejectsStaleRevision(t *testing.T) {
	s, principal, item, plan := managedPlatformFixture(t)
	enableManagedTLSFixture(&item)
	ctx := context.Background()
	review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-stale-revision"), review, 0, "maintenance-stale", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, op, "succeeded", "ready", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_platform_maintenance SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE platform_id=$1", item.ID); err != nil {
		t.Fatal(err)
	}
	maintenance, err := s.ClaimManagedPlatformMaintenance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE managed_platforms SET revision=revision+1,desired_spec=jsonb_set(desired_spec,'{neon,compute_replicas}',to_jsonb((desired_spec->'neon'->>'compute_replicas')::int+1)) WHERE id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckManagedPlatformOperation(ctx, maintenance); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision crossed maintenance fence", err)
	}
}

func TestManagedPlatformMaintenanceExcludesQueuedRecovery(t *testing.T) {
	s, p, item, plan, destination := recoveryStoreFixture(t)
	ctx := context.Background()
	current, err := s.ManagedPlatform(ctx, p, item.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	enableManagedTLSFixture(&current)
	review := managedPlatformReview(t, s, p, current, plan, current.Revision, "update")
	if _, err = s.AcceptManagedPlatform(ctx, p, current, plan, []byte("sealed-recovery-exclusion"), review, current.Revision, "maintenance-managed-update", "update"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, op, "succeeded", "ready", "", nil); err != nil {
		t.Fatal(err)
	}
	intent := backupRecoveryIntent(current, destination)
	intent.ExpectedSourceRevision = op.Revision
	recoveryReview, err := s.SavePlatformRecoveryReview(ctx, p, intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptPlatformRecovery(ctx, p, intent, recoveryReview, "maintenance-recovery-lock"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_platform_maintenance SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE platform_id=$1", item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimManagedPlatformMaintenance(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("maintenance crossed queued recovery", err)
	}
}

func TestManagedPlatformMaintenanceSharesGlobalClaimCap(t *testing.T) {
	s, p, template, plan := managedPlatformFixture(t)
	enableManagedTLSFixture(&template)
	ctx := context.Background()
	platforms := make([]ManagedPlatform, 9)
	for i := range platforms {
		item := template
		item.ID = NewID()
		item.Spec.Name = fmt.Sprintf("maintenance-cap-%d", i)
		item.Spec.Neon.ObjectStorageBucket = fmt.Sprintf("maintenance-cap-%d", i)
		review := managedPlatformReview(t, s, p, item, plan, 0, "create")
		if _, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte(fmt.Sprintf("sealed-cap-%d", i)), review, 0, fmt.Sprintf("maintenance-cap-key-%d", i), "create"); err != nil {
			t.Fatal(err)
		}
		op, err := s.ClaimManagedPlatformOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.RecordManagedPlatformStep(ctx, op, "succeeded", "ready", "", nil); err != nil {
			t.Fatal(err)
		}
		platforms[i] = item
	}
	for i, item := range platforms {
		if _, err := s.Pool.Exec(ctx, "UPDATE managed_platform_maintenance SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE platform_id=$1", item.ID); err != nil {
			t.Fatal(err)
		}
		if i < 8 {
			if _, err := s.ClaimManagedPlatformMaintenance(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.ClaimManagedPlatformMaintenance(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("maintenance exceeded shared claim cap", err)
	}
}

func TestManagedPlatformMaintenanceFenceRejectsOriginalLease(t *testing.T) {
	s, principal, item, plan := managedPlatformFixture(t)
	enableManagedTLSFixture(&item)
	ctx := context.Background()
	review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed-maintenance-fence"), review, 0, "maintenance-fence", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, op, "succeeded", "ready", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_platform_maintenance SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE platform_id=$1", item.ID); err != nil {
		t.Fatal(err)
	}
	maintenance, err := s.ClaimManagedPlatformMaintenance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	maintenance.Lease = op.Lease
	if err = s.CheckManagedPlatformOperation(ctx, maintenance); !errors.Is(err, ErrConflict) {
		t.Fatal("original operation lease crossed maintenance fence", err)
	}
}

func TestManagedPlatformMaintenanceKeepsSupabaseRestoreTargetIsolated(t *testing.T) {
	for _, terminal := range []string{"succeeded", "failed", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			s, p, source, sourcePlan, destination := recoveryStoreFixture(t)
			ctx := context.Background()
			target := ManagedPlatform{ID: NewID(), Project: source.Project, Environment: source.Environment, Spec: source.Spec}
			target.Spec.Name = "isolated-target"
			enableManagedTLSFixture(&target)
			images := map[string]string{}
			for _, component := range sourcePlan.Components {
				images[component.Name] = component.Image
			}
			plan, err := managedplatform.PlanSupabase(target.Spec, images)
			if err != nil {
				t.Fatal(err)
			}
			plan.Capability, plan.StorageClass = sourcePlan.Capability, sourcePlan.StorageClass
			review := managedPlatformReview(t, s, p, target, plan, 0, "create")
			if _, err = s.AcceptManagedPlatform(ctx, p, target, plan, []byte("sealed-isolated-target"), review, 0, "isolated-target-create", "create"); err != nil {
				t.Fatal(err)
			}
			owner, err := s.ClaimManagedPlatformOperation(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.RecordManagedPlatformStep(ctx, owner, "succeeded", "ready", "", map[string]any{"namespace_uid": "isolated-namespace", "tls": map[string]any{"serial": "original"}}); err != nil {
				t.Fatal(err)
			}
			manifest := recoveryManifestFixture(source.ID, destination.ID)
			artifactID := NewID()
			if _, err = s.Pool.Exec(ctx, `INSERT INTO managed_platform_recovery_artifacts(id,source_platform_id,source_revision,destination_id,object_key,encrypted_bytes,encrypted_sha256,manifest,manifest_sha256,published_at) VALUES($1,$2,1,$3,'fixtures/isolated-target',123,$4,$5,$6,now())`, artifactID, source.ID, destination.ID, strings.Repeat("e", 64), JSON(manifest), manifest.Digest()); err != nil {
				t.Fatal(err)
			}
			intent := platformbackup.Intent{Kind: "restore", Project: source.Project, Environment: source.Environment, SourcePlatformID: source.ID, TargetPlatformID: target.ID, ArtifactID: artifactID, ExpectedSourceRevision: 1, ExpectedTargetRevision: 1}
			recoveryReview, err := s.SavePlatformRecoveryReview(ctx, p, intent)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.AcceptPlatformRecovery(ctx, p, intent, recoveryReview, "isolated-target-restore"); err != nil {
				t.Fatal(err)
			}
			recovery, err := s.ClaimPlatformRecovery(ctx, "isolated-target-lease")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.FinishPlatformRecovery(ctx, recovery, terminal, "Restore target remains isolated."); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Pool.Exec(ctx, "UPDATE managed_platform_maintenance SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE platform_id=$1", target.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = s.ClaimManagedPlatformMaintenance(ctx); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal("maintenance would activate an isolated restore target", err)
			}
			var phase, namespaceUID, serial, message string
			if err = s.Pool.QueryRow(ctx, `SELECT observation->'maintenance'->>'phase',observation->>'namespace_uid',observation->'tls'->>'serial',observation->'maintenance'->>'message' FROM managed_platforms WHERE id=$1`, target.ID).Scan(&phase, &namespaceUID, &serial, &message); err != nil {
				t.Fatal(err)
			}
			if phase != "restore-isolated" || namespaceUID != "isolated-namespace" || serial != "original" || !strings.Contains(message, "Review a new platform revision") {
				t.Fatal("isolation did not preserve prior TLS facts and explain the hold")
			}
			var sourceObservation map[string]any
			if err = s.Pool.QueryRow(ctx, "SELECT observation FROM managed_platforms WHERE id=$1", source.ID).Scan(&sourceObservation); err != nil {
				t.Fatal(err)
			}
			if _, held := sourceObservation["maintenance"]; held {
				t.Fatal("restore held the source platform's maintenance")
			}
			// A stale or replayed maintenance lease must fail the mutation fence too.
			var maintenanceID string
			if err = s.Pool.QueryRow(ctx, `UPDATE managed_platform_maintenance SET status='running',lease='stale-restore-maintenance',lease_until=clock_timestamp()+interval '30 seconds' WHERE platform_id=$1 RETURNING id`, target.ID).Scan(&maintenanceID); err != nil {
				t.Fatal(err)
			}
			stale := owner
			stale.Maintenance, stale.MaintenanceID, stale.Lease = true, maintenanceID, "stale-restore-maintenance"
			if err = s.CheckManagedPlatformOperation(ctx, stale); !errors.Is(err, ErrConflict) {
				t.Fatal("stale maintenance crossed the restore isolation fence", err)
			}
			if _, err = s.Pool.Exec(ctx, "UPDATE managed_platform_maintenance SET status='idle',lease='',lease_until=NULL WHERE platform_id=$1", target.ID); err != nil {
				t.Fatal(err)
			}
			target, err = s.ManagedPlatform(ctx, p, target.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			review = managedPlatformReview(t, s, p, target, plan, target.Revision, "update")
			if _, err = s.AcceptManagedPlatform(ctx, p, target, plan, []byte("sealed-reviewed-target"), review, target.Revision, "isolated-target-activate", "update"); err != nil {
				t.Fatal(err)
			}
			activation, err := s.ClaimManagedPlatformOperation(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.RecordManagedPlatformStep(ctx, activation, "succeeded", "ready", "", nil); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Pool.Exec(ctx, "UPDATE managed_platform_maintenance SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE platform_id=$1", target.ID); err != nil {
				t.Fatal(err)
			}
			maintenance, err := s.ClaimManagedPlatformMaintenance(ctx)
			if err != nil || maintenance.Revision != activation.Revision || maintenance.Revision != 2 {
				t.Fatal("reviewed activation did not resume certificate maintenance", err)
			}
		})
	}
}
