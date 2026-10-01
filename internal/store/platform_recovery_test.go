package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/jackc/pgx/v5"
)

func recoveryStoreFixture(t *testing.T) (*Store, Principal, ManagedPlatform, managedplatform.Plan, backup.Destination) {
	t.Helper()
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	spec := managedplatform.Spec{SchemaVersion: 1, Name: "supabase-recovery-fixture", Kind: "supabase", Version: managedplatform.SupabaseVersion, Resources: map[string]managedplatform.Resources{}, Storage: map[string]int64{}, Secrets: map[string]managedplatform.SecretReference{}, Placement: managedplatform.Placement{NodeNames: []string{"fixture-node"}}, Supabase: &managedplatform.SupabaseConfig{PublicURL: "https://supabase.example.test", SiteURL: "https://app.example.test", DatabaseName: "postgres", JWTExpirySeconds: 3600, RESTMaxRows: 1000, StorageFileLimitBytes: 10485760, PoolSize: 10, PoolMaxClients: 100}}
	for _, name := range managedplatform.SupabaseComponentNames() {
		spec.Resources[name] = managedplatform.Resources{CPU: "500m", Memory: "2Gi"}
	}
	for _, name := range managedplatform.SupabaseRequiredStorageKeys() {
		spec.Storage[name] = 20
	}
	for _, name := range managedplatform.SupabaseRequiredSecretKeys() {
		spec.Secrets[name] = managedplatform.SecretReference{Name: "supabase-" + name, Revision: 1}
	}
	images := map[string]string{}
	for _, name := range managedplatform.SupabaseComponentNames() {
		images[name] = "registry.example.test/supabase/" + name + "@sha256:" + strings.Repeat("d", 64)
	}
	plan, err := managedplatform.PlanSupabase(spec, images)
	if err != nil {
		t.Fatal(err)
	}
	plan.Capability.Available = true
	plan.Capability.ClusterQualified = true
	plan.StorageClass = "encrypted-block"
	item := ManagedPlatform{ID: NewID(), Project: "demo", Environment: "development", Spec: spec}
	ctx := context.Background()
	enableManagedPlatformCapacity(s, "fixture-node")
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")
	if _, err = s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed"), review, 0, "recovery-platform-create", "create"); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, claim, "succeeded", "ready", "", map[string]any{"status": "ready"}); err != nil {
		t.Fatal(err)
	}
	p.Project, p.Environment = item.Project, item.Environment
	destination, err := s.PutBackupDestination(ctx, p, backup.Destination{ID: NewID(), Name: "recovery-fixture", Endpoint: "https://storage.example.com", Region: "test", Bucket: "recovery", Prefix: "supabase", EncryptionRecipient: "age1fixture", EncryptedCredentials: []byte("sealed")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return s, p, item, plan, destination
}

func TestPlatformRecoveryRechecksAdmissionGrantAndCapacity(t *testing.T) {
	s, p, item, _, destination := recoveryStoreFixture(t)
	ctx := context.Background()
	intent := backupRecoveryIntent(item, destination)
	review, err := s.SavePlatformRecoveryReview(ctx, p, intent)
	if err != nil {
		t.Fatal(err)
	}
	s.RequireManagedPlatformAdmission = true
	admissions, validations := 0, 0
	s.AdmitManagedPlatform = func(_ context.Context, _ pgx.Tx, principal Principal, project, environment, idem string) error {
		admissions++
		if principal.ID != p.ID || project != item.Project || environment != item.Environment || idem != "recovery-capacity-recheck" {
			return ErrForbidden
		}
		return nil
	}
	s.ValidateManagedPlatformCapacity = func(_ context.Context, project, environment string, policy managedplatform.CapacityPolicy) error {
		validations++
		if project != item.Project || environment != item.Environment || len(policy.Nodes) != 1 || policy.Nodes[0].UID != "uid-fixture-node" {
			return ErrConflict
		}
		return nil
	}
	if _, err = s.AcceptPlatformRecovery(ctx, p, intent, review, "recovery-capacity-recheck"); err != nil {
		t.Fatal(err)
	}
	if admissions != 1 || validations != 1 {
		t.Fatalf("recovery skipped current admission or capacity validation: admissions=%d validations=%d", admissions, validations)
	}
}

func TestPlatformRestoreRechecksSourceAndTargetCapacity(t *testing.T) {
	s, p, source, sourcePlan, destination := recoveryStoreFixture(t)
	ctx := context.Background()
	targetSpec := source.Spec
	targetSpec.Name = "supabase-restore-target"
	images := make(map[string]string, len(sourcePlan.Components))
	for _, component := range sourcePlan.Components {
		images[component.Name] = component.Image
	}
	targetPlan, err := managedplatform.PlanSupabase(targetSpec, images)
	if err != nil {
		t.Fatal(err)
	}
	targetPlan.Capability.Available = true
	targetPlan.Capability.ClusterQualified = true
	targetPlan.StorageClass = sourcePlan.StorageClass
	target := ManagedPlatform{ID: NewID(), Project: source.Project, Environment: source.Environment, Spec: targetSpec}
	targetReview := managedPlatformReview(t, s, p, target, targetPlan, 0, "create")
	if _, err = s.AcceptManagedPlatform(ctx, p, target, targetPlan, []byte("sealed-target"), targetReview, 0, "recovery-target-create", "create"); err != nil {
		t.Fatal(err)
	}
	targetClaim, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, targetClaim, "succeeded", "ready", "", map[string]any{"status": "ready"}); err != nil {
		t.Fatal(err)
	}

	manifest := recoveryManifestFixture(source.ID, destination.ID)
	artifactID := NewID()
	if _, err = s.Pool.Exec(ctx, `INSERT INTO managed_platform_recovery_artifacts(id,source_platform_id,source_revision,destination_id,object_key,encrypted_bytes,encrypted_sha256,manifest,manifest_sha256,published_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,now())`, artifactID, source.ID, int64(1), destination.ID, "fixtures/restore-target", int64(123), strings.Repeat("e", 64), JSON(manifest), manifest.Digest()); err != nil {
		t.Fatal(err)
	}
	restore := platformbackup.Intent{Kind: "restore", Project: source.Project, Environment: source.Environment, SourcePlatformID: source.ID, TargetPlatformID: target.ID, ArtifactID: artifactID, ExpectedSourceRevision: 1, ExpectedTargetRevision: 1}
	review, err := s.SavePlatformRecoveryReview(ctx, p, restore)
	if err != nil {
		t.Fatal(err)
	}
	s.RequireManagedPlatformAdmission = true
	admissions, validations := 0, 0
	s.AdmitManagedPlatform = func(_ context.Context, _ pgx.Tx, principal Principal, project, environment, idem string) error {
		admissions++
		if principal.ID != p.ID || project != source.Project || environment != source.Environment || idem != "restore-capacity-recheck" {
			return ErrForbidden
		}
		return nil
	}
	s.ValidateManagedPlatformCapacity = func(_ context.Context, project, environment string, policy managedplatform.CapacityPolicy) error {
		validations++
		var reservedPlatforms int
		if err := s.Pool.QueryRow(ctx, `SELECT count(DISTINCT platform_id) FROM managed_platform_capacity_reservations`).Scan(&reservedPlatforms); err != nil {
			return err
		}
		if project != source.Project || environment != source.Environment || len(policy.Nodes) != 1 || reservedPlatforms != 2 {
			return ErrConflict
		}
		return nil
	}
	if _, err = s.AcceptPlatformRecovery(ctx, p, restore, review, "restore-capacity-recheck"); err != nil {
		t.Fatal(err)
	}
	if admissions != 1 || validations != 1 {
		t.Fatalf("restore skipped current admission or source/target capacity validation: admissions=%d validations=%d", admissions, validations)
	}
}

func backupRecoveryIntent(item ManagedPlatform, destination backup.Destination) platformbackup.Intent {
	return platformbackup.Intent{Kind: "backup", Project: item.Project, Environment: item.Environment, SourcePlatformID: item.ID, DestinationID: destination.ID, DestinationRevision: destination.Revision, ExpectedSourceRevision: 1}
}

func TestPlatformRecoveryReviewRevisionReplayLeaseArtifactAndCancellation(t *testing.T) {
	s, p, item, _, destination := recoveryStoreFixture(t)
	ctx := context.Background()
	intent := backupRecoveryIntent(item, destination)
	wrong := intent
	wrong.Project = "other"
	if _, err := s.SavePlatformRecoveryReview(ctx, p, wrong); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-scope review accepted: %v", err)
	}
	staleReview, err := s.SavePlatformRecoveryReview(ctx, p, intent)
	if err != nil {
		t.Fatal(err)
	}
	destination.Name = "rotated"
	destination, err = s.PutBackupDestination(ctx, p, destination, destination.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptPlatformRecovery(ctx, p, intent, staleReview, "recovery-stale-destination"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale destination revision accepted: %v", err)
	}
	intent.DestinationRevision = destination.Revision
	review, err := s.SavePlatformRecoveryReview(ctx, p, intent)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := s.AcceptPlatformRecovery(ctx, p, intent, review, "recovery-operation")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.AcceptPlatformRecovery(ctx, p, intent, review, "recovery-operation")
	if err != nil || replay.ID != accepted.ID {
		t.Fatalf("idempotent replay failed: %#v %v", replay, err)
	}
	claimed, err := s.ClaimPlatformRecovery(ctx, "recovery-lease")
	if err != nil {
		t.Fatal(err)
	}
	stale := claimed
	stale.Lease = "stale-lease"
	if _, err = s.HeartbeatPlatformRecovery(ctx, stale); err == nil {
		t.Fatal("stale lease renewed")
	}
	token := strings.Repeat("1", 64)
	if _, prior, err := s.PreparePlatformRecoveryDeployment(ctx, claimed, item.ID, "supabase-auth", "deployment-uid", 7, 2, 0, 7, token); err != nil || prior != 2 {
		t.Fatalf("deployment baseline was not journaled: %d %v", prior, err)
	}
	if err = s.ReconcilePlatformRecoveryDeployment(ctx, claimed, "supabase-auth", "deployment-uid", token, 8, 0); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcilePlatformRecoveryDeployment(ctx, claimed, "supabase-auth", "deployment-uid", token, 8, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("replayed reconciliation was not fenced: %v", err)
	}
	if prior, err := s.PlatformRecoveryPriorReplicas(ctx, claimed, item.ID, "supabase-auth", "deployment-uid"); err != nil || prior != 2 {
		t.Fatalf("deployment baseline could not be read: %d %v", prior, err)
	}
	if _, prior, err := s.PreparePlatformRecoveryDeployment(ctx, claimed, item.ID, "supabase-auth", "deployment-uid", 8, 0, 2, 7, strings.Repeat("2", 64)); err != nil || prior != 2 {
		t.Fatalf("deployment baseline was not preserved: %d %v", prior, err)
	}
	if _, _, err := s.PreparePlatformRecoveryDeployment(ctx, stale, item.ID, "supabase-auth", "deployment-uid", 8, 0, 2, 7, strings.Repeat("3", 64)); err == nil {
		t.Fatal("stale lease changed deployment journal")
	}
	if prior, err := s.SavePlatformRecoveryDatabaseState(ctx, claimed, item.ID, false); err != nil || prior {
		t.Fatalf("database baseline was not journaled: %v %v", prior, err)
	}
	if prior, err := s.SavePlatformRecoveryDatabaseState(ctx, claimed, item.ID, true); err != nil || prior {
		t.Fatalf("database baseline was overwritten: %v %v", prior, err)
	}
	if err = s.SetPlatformRecoveryCleanup(ctx, claimed, true); err != nil {
		t.Fatal(err)
	}
	if err = s.SetPlatformRecoveryCleanup(ctx, claimed, false); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelPlatformRecovery(ctx, p, claimed.ID); err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.HeartbeatPlatformRecovery(ctx, claimed)
	if err != nil || !cancelled {
		t.Fatalf("cancellation was not observed: %v %v", cancelled, err)
	}
	if err = s.FencePlatformRecoveryCleanup(ctx, claimed); err != nil {
		t.Fatal("live cleanup lease rejected", err)
	}
	manifest := recoveryManifestFixture(item.ID, destination.ID)
	objectKey := "supabase/" + claimed.ID + ".age"
	if err = s.ReservePlatformRecoveryUpload(ctx, claimed, objectKey); err != nil {
		t.Fatal(err)
	}
	artifact, err := s.StagePlatformRecoveryArtifact(ctx, claimed, manifest, objectKey, 123, strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PublishPlatformRecoveryArtifact(ctx, claimed, artifact); err != nil {
		t.Fatal(err)
	}
	stored, err := s.PlatformRecoveryArtifact(ctx, artifact.ID)
	if err != nil || stored.EncryptedBytes != 123 || stored.Manifest.Digest() != manifest.Digest() {
		t.Fatalf("artifact persistence changed: %#v %v", stored, err)
	}
}

func recoveryManifestFixture(platformID, destinationID string) platformbackup.Manifest {
	now := time.Now().UTC()
	parts := make([]platformbackup.Part, 0, len(platformbackup.RequiredParts))
	for _, name := range platformbackup.RequiredParts {
		parts = append(parts, platformbackup.Part{Name: name, Bytes: 1, SHA256: strings.Repeat("a", 64)})
	}
	verification := map[string]string{}
	for _, name := range []string{"database_roles", "database_security", "auth_metadata", "storage_metadata", "storage_bytes", "edge_functions", "studio_snippets"} {
		verification[name] = strings.Repeat("b", 64)
	}
	return platformbackup.Manifest{SchemaVersion: platformbackup.SchemaVersion, Format: platformbackup.Format, PlatformID: platformID, PlatformRevision: 1, PlatformSpec: json.RawMessage(`{"schema_version":1,"name":"fixture","kind":"supabase"}`), Release: "fixture", Images: map[string]string{"database": "postgres@sha256:" + strings.Repeat("c", 64)}, SourceNamespace: "fixture", SourceNamespaceUID: "namespace-uid", PVCs: []platformbackup.Claim{{Component: "database", Kind: "pvc", Name: "database", UID: "pvc-uid"}}, Parts: parts, Verification: verification, FrozenAt: now.Add(-2 * time.Second), CapturedAt: now.Add(-time.Second), ThawedAt: now, Consistency: "writes blocked and claims reobserved", DestinationID: destinationID, EncryptionRecipient: "age1fixture"}
}

func TestManagedPlatformMutationConflictsWithActiveRecovery(t *testing.T) {
	s, p, item, plan, destination := recoveryStoreFixture(t)
	ctx := context.Background()
	intent := backupRecoveryIntent(item, destination)
	review, err := s.SavePlatformRecoveryReview(ctx, p, intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptPlatformRecovery(ctx, p, intent, review, "recovery-lock"); err != nil {
		t.Fatal(err)
	}
	current, err := s.ManagedPlatform(ctx, p, item.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	deleteReview := managedPlatformReview(t, s, p, current, plan, current.Revision, "delete")
	if _, err = s.AcceptManagedPlatform(ctx, p, current, plan, []byte("sealed-delete"), deleteReview, current.Revision, "platform-during-recovery", "delete"); !errors.Is(err, ErrConflict) {
		t.Fatalf("platform mutation raced active recovery: %v", err)
	}
}
