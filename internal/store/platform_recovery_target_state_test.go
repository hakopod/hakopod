package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/platformbackup"
)

func TestPlatformRecoveryCleanupMarksOnlyExactTargetFailed(t *testing.T) {
	s, p, target, plan := managedPlatformFixture(t)
	ctx := context.Background()
	create := func(item ManagedPlatform, idem string) {
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
	}
	create(target, "target-state-target")
	source := target
	source.ID = NewID()
	source.Spec.Name = "target-state-source"
	create(source, "target-state-source")
	p.Project, p.Environment = target.Project, target.Environment
	destination, err := s.PutBackupDestination(ctx, p, backup.Destination{ID: NewID(), Name: "target-state", Endpoint: "https://storage.example.test", Region: "test", Bucket: "recovery", Prefix: "neon", EncryptionRecipient: "age1fixture", EncryptedCredentials: []byte("sealed")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	manifest := neonRecoveryManifestFixture(source.ID, destination.ID)
	artifactID := NewID()
	if _, err = s.Pool.Exec(ctx, `INSERT INTO managed_platform_recovery_artifacts(id,source_platform_id,source_revision,destination_id,object_key,encrypted_bytes,encrypted_sha256,manifest,manifest_sha256,published_at) VALUES($1,$2,1,$3,$4,1,$5,$6,$7,now())`, artifactID, source.ID, destination.ID, "neon/target-state", strings.Repeat("e", 64), JSON(manifest), manifest.ManifestSHA256); err != nil {
		t.Fatal(err)
	}
	intent := platformbackup.Intent{Kind: "restore", Project: target.Project, Environment: target.Environment, SourcePlatformID: source.ID, TargetPlatformID: target.ID, ArtifactID: artifactID, ExpectedSourceRevision: 1, ExpectedTargetRevision: 1}
	review, err := s.SavePlatformRecoveryReview(ctx, p, intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptPlatformRecovery(ctx, p, intent, review, "target-state-restore"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimPlatformRecovery(ctx, "target-state-lease")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BindNeonRecoveryTarget(ctx, op, manifest, "staging/"+op.ID); err != nil {
		t.Fatal(err)
	}
	cleanup := platformbackup.WithRecoveryCleanup(ctx)
	stale := op
	stale.Lease = "stale"
	if err = s.MarkPlatformRecoveryTargetIsolated(cleanup, stale, "isolated"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale cleanup lease changed target state: %v", err)
	}
	if err = s.MarkPlatformRecoveryTargetIsolated(cleanup, op, "isolated after restore cleanup"); err != nil {
		t.Fatal(err)
	}
	loadedTarget, err := s.ManagedPlatform(ctx, p, target.ID, false)
	if err != nil || loadedTarget.Status != "failed" || loadedTarget.Observation["phase"] != "recovery-isolated" {
		t.Fatalf("target was not visibly isolated: %#v %v", loadedTarget, err)
	}
	loadedSource, err := s.ManagedPlatform(ctx, p, source.ID, false)
	if err != nil || loadedSource.Status != "ready" {
		t.Fatalf("source state changed during target isolation: %#v %v", loadedSource, err)
	}
}
