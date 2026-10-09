//go:build hakopod_native_acceptance && linux

package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/platformbackup"
)

func TestValidateNativeCancellationJournalRequiresInterruptedTerminalCleanup(t *testing.T) {
	if _, err := validateNativeCancellationJournal(nil); !errors.Is(err, ErrNativeCancellationNeverStarted) {
		t.Fatalf("empty cancelled journal was not distinguished: %v", err)
	}
	if _, err := validateNativeCancellationJournal([]NeonRecoveryResource{{Phase: "reserved"}}); err == nil {
		t.Fatal("nonterminal cancellation journal qualified")
	}
	counts, err := validateNativeCancellationJournal([]NeonRecoveryResource{
		{Phase: "complete"}, {Phase: "empty_complete"}, {Phase: "untouched_complete"}, {Phase: "complete"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if counts["complete"] != 2 || counts["empty_complete"] != 1 || counts["untouched_complete"] != 1 || len(counts) != 3 {
		t.Fatalf("unexpected terminal phase counts: %#v", counts)
	}
}

func TestNativeNeonCancellationReceiptReadsCompletedCleanup(t *testing.T) {
	s, p, target, plan := managedPlatformFixture(t)
	ctx := context.Background()
	create := func(item ManagedPlatform, idem string) ManagedPlatformOperation {
		review := managedPlatformReview(t, s, p, item, plan, 0, "create")
		if _, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed"), review, 0, idem, "create"); err != nil {
			t.Fatal(err)
		}
		op, err := s.ClaimManagedPlatformOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.RecordManagedPlatformStep(ctx, op, "succeeded", "ready", "", map[string]any{"status": "ready"}); err != nil {
			t.Fatal(err)
		}
		return op
	}
	targetOwner := create(target, "native-cancellation-target")
	source := target
	source.ID, source.Spec.Name = NewID(), "native-cancellation-source"
	create(source, "native-cancellation-source")
	p.Project, p.Environment = target.Project, target.Environment
	destination, err := s.PutBackupDestination(ctx, p, backup.Destination{ID: NewID(), Name: "native-cancellation", Endpoint: "https://storage.example.test", Region: "test", Bucket: "recovery", Prefix: "neon", EncryptionRecipient: "age1fixture", EncryptedCredentials: []byte("sealed")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	manifest := neonRecoveryManifestFixture(source.ID, destination.ID)
	artifactID := NewID()
	if _, err = s.Pool.Exec(ctx, `INSERT INTO managed_platform_recovery_artifacts(id,source_platform_id,source_revision,destination_id,object_key,encrypted_bytes,encrypted_sha256,manifest,manifest_sha256,published_at) VALUES($1,$2,1,$3,$4,1,$5,$6,$7,now())`, artifactID, source.ID, destination.ID, "neon/native-cancellation", strings.Repeat("e", 64), JSON(manifest), manifest.ManifestSHA256); err != nil {
		t.Fatal(err)
	}
	intent := platformbackup.Intent{Kind: "restore", Project: target.Project, Environment: target.Environment, SourcePlatformID: source.ID, TargetPlatformID: target.ID, ArtifactID: artifactID, ExpectedSourceRevision: 1, ExpectedTargetRevision: 1}
	review, err := s.SavePlatformRecoveryReview(ctx, p, intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptPlatformRecovery(ctx, p, intent, review, "native-cancellation-restore"); err != nil {
		t.Fatal(err)
	}
	recovery, err := s.ClaimPlatformRecovery(ctx, "native-cancellation-lease")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := s.BindNeonRecoveryTarget(ctx, recovery, manifest, "fixture/recovery/"+recovery.ID+"/")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CancelPlatformRecovery(ctx, p, recovery.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.SetPlatformRecoveryCleanup(ctx, recovery, true); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	if _, err = s.Pool.Exec(ctx, `INSERT INTO platform_component_recovery_overrides(platform_id,platform_revision,component,resource_kind,recovery_operation_id,artifact_id,manifest_sha256,prior_resource_id,prior_generation,prior_owner_operation_id,replacement_external_key,phase,transition_token,completed_at) VALUES($1,1,'tenant','neon_tenant',$2,$3,$4,'prior-tenant',1,$5,'replacement-tenant','untouched_complete',$6,now())`, target.ID, recovery.ID, artifactID, binding.ManifestSHA256, targetOwner.ID, token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO managed_platform_recovery_deployments(operation_id,platform_id,workload_kind,deployment_name,deployment_uid,baseline_generation,current_generation,prior_replicas,target_replicas,transition_token) VALUES($1,$2,'deployment','neon-proxy','proxy-uid',1,2,1,0,$3)`, recovery.ID, target.ID, token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO platform_component_resources(platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id) VALUES($1,1,$2,'runtime_component','namespace-uid',1,$3)`, target.ID, "namespace.managed-platform-"+target.ID, targetOwner.ID); err != nil {
		t.Fatal(err)
	}
	cleanup := platformbackup.WithRecoveryCleanup(ctx)
	if err = s.SetPlatformRecoveryCleanup(cleanup, recovery, false); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkPlatformRecoveryTargetIsolated(cleanup, recovery, "isolated after cancelled native recovery"); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishPlatformRecovery(cleanup, recovery, "cancelled", "cancelled after cleanup"); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.NativeNeonCancellationReceipt(ctx, recovery.ID, target.Project, target.Environment)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.OperationID != recovery.ID || receipt.JournalEntries != 1 || receipt.JournalPhaseCounts["untouched_complete"] != 1 || len(receipt.Workloads) != 1 || receipt.Workloads[0].UID != "proxy-uid" || !receipt.OperationAuthorityRefused || receipt.CleanupPending {
		t.Fatalf("unexpected cancellation receipt: %#v", receipt)
	}
}
