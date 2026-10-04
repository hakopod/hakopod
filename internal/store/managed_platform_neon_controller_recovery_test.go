package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/platformbackup"
)

func TestNeonRecoveryControllerStateRequiresLiveFenceAndReplacementTimeline(t *testing.T) {
	s, principal, target, plan := managedPlatformFixture(t)
	ctx := context.Background()
	create := func(item ManagedPlatform, key string) ManagedPlatformOperation {
		review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
		if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed"), review, 0, key, "create"); err != nil {
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
	targetOwner := create(target, "recovery-controller-target")
	prior := PlatformResourceClaim{PlatformID: target.ID, PlatformRevision: 1, Component: "timeline", Kind: "neon_timeline", ResourceID: "prior-timeline", ImmutableGeneration: 1, OwnerOperationID: targetOwner.ID}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO platform_component_resources(platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, prior.PlatformID, prior.PlatformRevision, prior.Component, prior.Kind, prior.ResourceID, prior.ImmutableGeneration, prior.OwnerOperationID); err != nil {
		t.Fatal(err)
	}
	source := target
	source.ID = NewID()
	source.Spec.Name = "recovery-controller-source"
	create(source, "recovery-controller-source")
	principal.Project, principal.Environment = target.Project, target.Environment
	destination, err := s.PutBackupDestination(ctx, principal, backup.Destination{ID: NewID(), Name: "recovery-controller", Endpoint: "https://storage.example.test", Region: "test", Bucket: "recovery", Prefix: "neon", EncryptionRecipient: "age1fixture", EncryptedCredentials: []byte("sealed")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	manifest := platformbackup.Manifest{Format: platformbackup.NeonFormat, Neon: &platformbackup.NeonIdentity{TenantID: strings.Repeat("1", 32), TimelineID: strings.Repeat("2", 32), TenantGeneration: 1, TimelineGeneration: 1}}
	manifest.ManifestSHA256 = manifest.Digest()
	artifactID := NewID()
	if _, err = s.Pool.Exec(ctx, `INSERT INTO managed_platform_recovery_artifacts(id,source_platform_id,source_revision,destination_id,object_key,encrypted_bytes,encrypted_sha256,manifest,manifest_sha256,published_at) VALUES($1,$2,1,$3,$4,1,$5,$6,$7,now())`, artifactID, source.ID, destination.ID, "neon/controller", strings.Repeat("e", 64), JSON(map[string]any{"format": platformbackup.NeonFormat}), manifest.ManifestSHA256); err != nil {
		t.Fatal(err)
	}
	intent := platformbackup.Intent{Kind: "restore", Project: target.Project, Environment: target.Environment, SourcePlatformID: source.ID, TargetPlatformID: target.ID, ArtifactID: artifactID, ExpectedSourceRevision: 1, ExpectedTargetRevision: 1}
	review, err := s.SavePlatformRecoveryReview(ctx, principal, intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptPlatformRecovery(ctx, principal, intent, review, "recovery-controller-restore"); err != nil {
		t.Fatal(err)
	}
	recovery, err := s.ClaimPlatformRecovery(ctx, "recovery-controller-lease")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := s.BindNeonRecoveryTarget(ctx, recovery, manifest, "staging/"+recovery.ID)
	if err != nil {
		t.Fatal(err)
	}
	transition := strings.Repeat("f", 64)
	if _, err = s.PlanNeonRecoveryResource(ctx, recovery, binding, prior, "replacement-timeline", transition); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkNeonRecoveryPriorReleased(ctx, recovery, prior.Component, prior.ResourceID, prior.ImmutableGeneration, transition); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReserveNeonRecoveryReplacement(ctx, recovery, prior.Component, prior.Kind, "replacement-timeline", transition); err != nil {
		t.Fatal(err)
	}

	called := false
	apply := func(input NeonControllerContext) (managedplatform.NeonControllerState, bool, error) {
		called = true
		if input.Binding != binding || len(input.Claims) != 1 || input.Claims[0].ResourceID != "restored-timeline" {
			t.Fatal("recovery controller context was not bound to the exact replacement")
		}
		return input.State, true, nil
	}
	if _, err = s.WithNeonRecoveryControllerState(ctx, recovery, apply); !errors.Is(err, ErrConflict) || called {
		t.Fatalf("unconfirmed replacement timeline reached controller apply: %v", err)
	}
	replacement := prior
	replacement.ResourceID, replacement.ImmutableGeneration = "restored-timeline", 2
	if _, err = s.ConfirmNeonRecoveryReplacement(ctx, recovery, prior.Component, replacement.ResourceID, replacement.ImmutableGeneration, transition); err != nil {
		t.Fatal(err)
	}
	staleLease := recovery
	staleLease.Lease = "stale"
	if _, err = s.WithNeonRecoveryControllerState(ctx, staleLease, apply); !errors.Is(err, ErrConflict) || called {
		t.Fatalf("stale restore lease reached controller apply: %v", err)
	}
	staleAuthority := recovery
	staleAuthority.AuthorityFingerprint = []byte("changed")
	if _, err = s.WithNeonRecoveryControllerState(ctx, staleAuthority, apply); !errors.Is(err, ErrConflict) || called {
		t.Fatalf("changed restore authority reached controller apply: %v", err)
	}
	wrongBinding := recovery
	wrongBinding.ArtifactID = NewID()
	if _, err = s.WithNeonRecoveryControllerState(ctx, wrongBinding, apply); !errors.Is(err, ErrConflict) || called {
		t.Fatalf("changed restore binding reached controller apply: %v", err)
	}
	if applied, applyErr := s.WithNeonRecoveryControllerState(ctx, recovery, apply); applyErr != nil || !applied || !called {
		t.Fatalf("exact live recovery controller state was rejected: %v", applyErr)
	}
}
