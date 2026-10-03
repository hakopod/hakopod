package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/jackc/pgx/v5"
)

func managedPlatformFixture(t *testing.T) (*Store, Principal, ManagedPlatform, managedplatform.Plan) {
	t.Helper()
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	spec := managedplatform.Spec{SchemaVersion: 1, Name: "neon-fixture", Kind: "neon", Version: managedplatform.NeonVersion, Resources: map[string]managedplatform.Resources{}, Storage: map[string]int64{}, Secrets: map[string]managedplatform.SecretReference{}, Placement: managedplatform.Placement{NodeNames: []string{"node-a", "node-b", "node-c"}}, Neon: &managedplatform.NeonConfig{PostgresVersion: "17", ComputeReplicas: 1, Pageservers: 2, Safekeepers: 3, BranchLimit: 1, ObjectStorageURL: "https://objects.example.test", ObjectStorageBucket: "neon-fixture", ObjectStorageRegion: "us-east-1", ObjectStoragePrefix: "fixture", ProxyControlPlanePatchSHA256: managedplatform.NeonProxyControlPlanePatchSHA256}}
	for _, name := range managedplatform.NeonComponents() {
		spec.Resources[name] = managedplatform.Resources{CPU: "500m", Memory: "2Gi"}
	}
	for _, name := range managedplatform.NeonStorageKeys() {
		spec.Storage[name] = 20
	}
	for _, name := range managedplatform.NeonSecretKeys() {
		spec.Secrets[name] = managedplatform.SecretReference{Name: "neon-" + name, Revision: 1}
	}
	images := map[string]string{}
	for _, name := range managedplatform.NeonComponents() {
		images[name] = "registry.example.test/neon/" + name + "@sha256:" + strings.Repeat("a", 64)
	}
	plan, err := managedplatform.PlanNeon(spec, images)
	if err != nil {
		t.Fatal(err)
	}
	plan.Capability.Available = true
	plan.Capability.ClusterQualified = true
	return s, p, ManagedPlatform{ID: NewID(), Project: "demo", Environment: "development", Spec: spec}, plan
}

func managedPlatformReview(t *testing.T, s *Store, p Principal, item ManagedPlatform, plan managedplatform.Plan, expected int64, kind string) ManagedPlatformReview {
	t.Helper()
	review, err := s.SaveManagedPlatformReview(context.Background(), p, item, plan, expected, kind)
	if err != nil {
		t.Fatal(err)
	}
	return review
}

func TestManagedPlatformReplayScopeAndStaleLease(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")
	op, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed-runtime-plan"), review, 0, "platform-create", "create")
	if err != nil {
		t.Fatal(err)
	}
	replayReview := review
	replayReview.ExpiresAt = time.Now().Add(-time.Second)
	replay, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("different-random-ciphertext"), replayReview, 0, "platform-create", "create")
	if err != nil || replay.ID != op.ID {
		t.Fatal("semantic replay after review expiry failed", err)
	}
	changed := item
	changed.Spec.Neon.ComputeReplicas = 2
	if _, err = s.AcceptManagedPlatform(ctx, p, changed, plan, []byte("sealed"), review, 0, "platform-create", "create"); !errors.Is(err, ErrConflict) {
		t.Fatal("changed replay accepted", err)
	}
	limited := p
	limited.Application = "app"
	if _, err = s.ManagedPlatform(ctx, limited, item.ID, false); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("application principal escaped scope", err)
	}
	claim, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stale := claim
	stale.Lease = NewID()
	if err = s.HeartbeatManagedPlatformOperation(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatal("stale lease renewed", err)
	}
	if err = s.RecordManagedPlatformStep(ctx, claim, "succeeded", "ready", "", map[string]any{"status": "ready"}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedPlatformResourceOwnershipIsGenerationFenced(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")
	_, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed-runtime-plan"), review, 0, "platform-resource", "create")
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claim := PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: "tenant", Kind: "neon_tenant", ResourceID: "11111111111111111111111111111111", ImmutableGeneration: 7, OwnerOperationID: op.ID}
	if err = s.ClaimPlatformResource(ctx, op, claim); err != nil {
		t.Fatal(err)
	}
	claims, err := s.PlatformResourceClaims(ctx, op, op.Revision)
	if err != nil || len(claims) != 1 || claims[0].ResourceID != claim.ResourceID {
		t.Fatal("bounded claim lookup failed", err)
	}
	wrong := claim
	wrong.ImmutableGeneration = 8
	if err = s.VerifyPlatformResourceClaim(ctx, op, wrong); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong generation verified", err)
	}
	other := claim
	other.ResourceID = "22222222222222222222222222222222"
	if err = s.ClaimPlatformResource(ctx, op, other); !errors.Is(err, ErrConflict) {
		t.Fatal("component ownership was overwritten", err)
	}
}

func TestManagedPlatformReviewAndEncryptedSnapshotStayPrivate(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")
	op, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed-runtime-plan"), review, 0, "platform-private", "create")
	if err != nil {
		t.Fatal(err)
	}
	if len(op.EncryptedSnapshot) != 0 || len(op.AuthorityFingerprint) != 0 {
		t.Fatal("accept response exposed private operation state")
	}
	replay, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("different-random-ciphertext"), review, 0, "platform-private", "create")
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.EncryptedSnapshot) != 0 {
		t.Fatal("replay response exposed encrypted reconciler snapshot")
	}
	visible, err := s.ManagedPlatformOperation(ctx, p, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible.EncryptedSnapshot) != 0 {
		t.Fatal("inspection exposed encrypted reconciler snapshot")
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_platforms SET status='deleted',deleted_at=now() WHERE id=$1", item.ID); err != nil {
		t.Fatal(err)
	}
	history, err := s.ManagedPlatformOperations(ctx, p, item.ID)
	if err != nil || len(history) != 1 {
		t.Fatal("deleted platform history unavailable", err)
	}
	if len(history[0].EncryptedSnapshot) != 0 {
		t.Fatal("history exposed encrypted reconciler snapshot")
	}
	forged := review
	forged.ID = NewID()
	if _, err = s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed"), forged, 0, "platform-forged", "create"); !errors.Is(err, ErrConflict) {
		t.Fatal("fabricated review accepted", err)
	}
}

func TestManagedPlatformRejectsUnqualifiedPlan(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	plan.Capability.Available = false
	if _, err := s.SaveManagedPlatformReview(context.Background(), p, item, plan, 0, "create"); !errors.Is(err, ErrForbidden) {
		t.Fatal("unqualified platform was reviewed", err)
	}
}

func TestManagedPlatformEncryptedSnapshotBoundPersistsAndReplays(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")
	oversized := bytes.Repeat([]byte{1}, managedplatform.MaxManagedPlatformEncryptedSnapshotBytes+1)
	if _, err := s.AcceptManagedPlatform(ctx, p, item, plan, oversized, review, 0, "platform-oversized", "create"); !errors.Is(err, ErrInput) {
		t.Fatalf("oversized encrypted snapshot was accepted: %v", err)
	}
	sealed := bytes.Repeat([]byte{2}, managedplatform.MaxManagedPlatformEncryptedSnapshotBytes)
	accepted, err := s.AcceptManagedPlatform(ctx, p, item, plan, sealed, review, 0, "platform-large-snapshot", "create")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.AcceptManagedPlatform(ctx, p, item, plan, bytes.Repeat([]byte{3}, len(sealed)), review, 0, "platform-large-snapshot", "create")
	if err != nil || replayed.ID != accepted.ID {
		t.Fatalf("large encrypted snapshot replay failed: %v", err)
	}
	claimed, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != accepted.ID || !bytes.Equal(claimed.EncryptedSnapshot, sealed) {
		t.Fatal("durable operation recovery did not preserve the encrypted snapshot")
	}
}
