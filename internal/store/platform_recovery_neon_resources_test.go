package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/jackc/pgx/v5"
)

func TestNeonRecoveryResourceInputIsBounded(t *testing.T) {
	token := strings.Repeat("a", 64)
	for _, component := range []string{"tenant", "timeline", "compute-primary", "compute-5"} {
		if !validNeonRecoveryResourceInput(component, "runtime_component", "restore/resource", token) {
			t.Fatalf("valid recovery component %q was rejected", component)
		}
	}
	for _, test := range []struct{ component, kind, key, token string }{
		{"", "runtime_component", "key", token},
		{"compute-0", "other", "key", token},
		{"compute-0", "runtime_component", "", token},
		{"compute-0", "runtime_component", strings.Repeat("x", 256), token},
		{"compute-0", "runtime_component", "key", strings.Repeat("a", 63)},
		{"compute-0", "runtime_component", "key", strings.Repeat("A", 64)},
	} {
		if validNeonRecoveryResourceInput(test.component, test.kind, test.key, test.token) {
			t.Fatalf("unsafe recovery input was accepted: %#v", test)
		}
	}
}

func TestNeonRecoveryResourceLifecycleUsesEffectiveOwnership(t *testing.T) {
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
	targetOwner := create(target, "neon-recovery-target")
	claim := PlatformResourceClaim{PlatformID: target.ID, PlatformRevision: 1, Component: "compute-primary", Kind: "runtime_component", ResourceID: "bootstrap-compute", ImmutableGeneration: 1, OwnerOperationID: targetOwner.ID}
	// The fixture plan intentionally has no compute-primary component. Insert a
	// fixture claim under the accepted snapshot to cover restored legacy names.
	if _, err := s.Pool.Exec(ctx, `INSERT INTO platform_component_resources(platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id) VALUES($1,1,$2,$3,$4,1,$5)`, target.ID, claim.Component, claim.Kind, claim.ResourceID, targetOwner.ID); err != nil {
		t.Fatal(err)
	}
	secondary := claim
	secondary.Component, secondary.ResourceID = "compute-secondary", "bootstrap-secondary"
	empty := claim
	empty.Component, empty.ResourceID = "compute-empty", "bootstrap-empty"
	untouched := claim
	untouched.Component, untouched.ResourceID = "compute-untouched", "bootstrap-untouched"
	for _, extra := range []PlatformResourceClaim{secondary, empty, untouched} {
		if _, err := s.Pool.Exec(ctx, `INSERT INTO platform_component_resources(platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, extra.PlatformID, extra.PlatformRevision, extra.Component, extra.Kind, extra.ResourceID, extra.ImmutableGeneration, extra.OwnerOperationID); err != nil {
			t.Fatal(err)
		}
	}
	proxy := neonProxyRecord(targetOwner, 1)
	if _, err := s.Pool.Exec(ctx, `INSERT INTO managed_platform_neon_proxy_endpoints(endpoint_id,platform_id,platform_revision,owner_operation_id,generation,enabled,address,server_name,project_id,branch_id,compute_id,encrypted_roles) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, proxy.EndpointID, proxy.PlatformID, proxy.PlatformRevision, proxy.OwnerOperationID, proxy.Generation, proxy.Enabled, proxy.Address, proxy.ServerName, proxy.ProjectID, proxy.BranchID, proxy.ComputeID, proxy.EncryptedRoles); err != nil {
		t.Fatal(err)
	}
	source := target
	source.ID = NewID()
	source.Spec.Name = "neon-recovery-source"
	create(source, "neon-recovery-source")
	p.Project, p.Environment = target.Project, target.Environment
	destination, err := s.PutBackupDestination(ctx, p, backup.Destination{ID: NewID(), Name: "neon-recovery", Endpoint: "https://storage.example.test", Region: "test", Bucket: "recovery", Prefix: "neon", EncryptionRecipient: "age1fixture", EncryptedCredentials: []byte("sealed")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	manifest := platformbackup.Manifest{Format: platformbackup.NeonFormat, Neon: &platformbackup.NeonIdentity{TenantID: strings.Repeat("1", 32), TimelineID: strings.Repeat("2", 32), TenantGeneration: 1, TimelineGeneration: 1}}
	manifest.ManifestSHA256 = manifest.Digest()
	artifactID := NewID()
	if _, err = s.Pool.Exec(ctx, `INSERT INTO managed_platform_recovery_artifacts(id,source_platform_id,source_revision,destination_id,object_key,encrypted_bytes,encrypted_sha256,manifest,manifest_sha256,published_at) VALUES($1,$2,1,$3,$4,1,$5,$6,$7,now())`, artifactID, source.ID, destination.ID, "neon/recovery", strings.Repeat("e", 64), JSON(map[string]any{"format": platformbackup.NeonFormat}), manifest.ManifestSHA256); err != nil {
		t.Fatal(err)
	}
	intent := platformbackup.Intent{Kind: "restore", Project: target.Project, Environment: target.Environment, SourcePlatformID: source.ID, TargetPlatformID: target.ID, ArtifactID: artifactID, ExpectedSourceRevision: 1, ExpectedTargetRevision: 1}
	review, err := s.SavePlatformRecoveryReview(ctx, p, intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptPlatformRecovery(ctx, p, intent, review, "neon-resource-recovery"); err != nil {
		t.Fatal(err)
	}
	recovery, err := s.ClaimPlatformRecovery(ctx, "neon-resource-lease")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetPlatformRecoveryCleanup(ctx, recovery, true); err != nil {
		t.Fatalf("pre-binding Neon cleanup flag was rejected: %v", err)
	}
	cleanup := platformbackup.WithRecoveryCleanup(ctx)
	if _, err = s.NeonRecoveryBindingForRecovery(ctx, recovery); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("fresh restore binding lookup did not report a safe miss: %v", err)
	}
	if _, err = s.NeonRecoveryBindingForRecovery(cleanup, recovery); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("pre-binding cleanup lookup did not report a safe miss: %v", err)
	}
	if err = s.SetPlatformRecoveryCleanup(ctx, recovery, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("pre-binding cleanup flag cleared without cleanup authority: %v", err)
	}
	if err = s.SetPlatformRecoveryCleanup(cleanup, recovery, false); err != nil {
		t.Fatalf("clean pre-binding recovery could not clear cleanup flag: %v", err)
	}
	if err = s.SetPlatformRecoveryCleanup(ctx, recovery, true); err != nil {
		t.Fatal(err)
	}
	binding, err := s.BindNeonRecoveryTarget(ctx, recovery, manifest, "staging/"+recovery.ID)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.BindNeonRecoveryTarget(ctx, recovery, manifest, "staging/"+recovery.ID)
	if err != nil || replayed != binding {
		t.Fatalf("same recovery operation did not replay its immutable binding: %#v %v", replayed, err)
	}
	pendingBinding, err := s.NeonRecoveryBindingForRecovery(ctx, recovery)
	if err != nil || pendingBinding != binding {
		t.Fatalf("running recovery could not resolve its exact pending binding: %#v %v", pendingBinding, err)
	}
	if _, err = s.NeonRecoveryBindingForTarget(ctx, target.ID, 1); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("running recovery binding became effective before success: %v", err)
	}
	token := strings.Repeat("a", 64)
	if _, err = s.PlanNeonRecoveryResource(ctx, recovery, binding, claim, "replacement-compute", token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkNeonRecoveryPriorReleased(ctx, recovery, claim.Component, claim.ResourceID, claim.ImmutableGeneration, token); err != nil {
		t.Fatal(err)
	}
	if claims, err := s.EffectiveNeonRecoveryClaims(ctx, recovery); err != nil || claims[claim.Component].ResourceID != "" {
		t.Fatalf("released prior claim remained effective: %#v %v", claims[claim.Component], err)
	}
	if _, err = s.ReserveNeonRecoveryReplacement(ctx, recovery, claim.Component, claim.Kind, "replacement-compute", token); err != nil {
		t.Fatal(err)
	}
	replacement := claim
	replacement.ResourceID = "restored-compute"
	replacement.ImmutableGeneration = 2
	if _, err = s.ConfirmNeonRecoveryReplacement(ctx, recovery, claim.Component, replacement.ResourceID, replacement.ImmutableGeneration, token); err != nil {
		t.Fatal(err)
	}
	if err = s.VerifyNeonRecoveryResource(ctx, recovery, replacement); err != nil {
		t.Fatal(err)
	}
	wrong := replacement
	wrong.ImmutableGeneration++
	if err = s.VerifyNeonRecoveryResource(ctx, recovery, wrong); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong recovered generation verified: %v", err)
	}
	if err = s.SetPlatformRecoveryCleanup(ctx, recovery, false); err != nil {
		t.Fatalf("successful restore could not clear cleanup with live confirmed resources: %v", err)
	}
	if err = s.SetPlatformRecoveryCleanup(ctx, recovery, true); err != nil {
		t.Fatal(err)
	}
	assertNeonTenantPlacementRestoreBoundary(t, s, targetOwner, recovery, binding)
	secondaryToken := strings.Repeat("b", 64)
	secondaryJournal, err := s.PlanNeonRecoveryResource(ctx, recovery, binding, secondary, "replacement-secondary", secondaryToken)
	if err != nil {
		t.Fatal(err)
	}
	if secondaryJournal, err = s.MarkNeonRecoveryPriorReleased(ctx, recovery, secondary.Component, secondary.ResourceID, secondary.ImmutableGeneration, secondaryToken); err != nil {
		t.Fatal(err)
	}
	if secondaryJournal, err = s.ReserveNeonRecoveryReplacement(ctx, recovery, secondary.Component, secondary.Kind, "replacement-secondary", secondaryToken); err != nil {
		t.Fatal(err)
	}
	emptyToken := strings.Repeat("c", 64)
	emptyJournal, err := s.PlanNeonRecoveryResource(ctx, recovery, binding, empty, "replacement-empty", emptyToken)
	if err != nil {
		t.Fatal(err)
	}
	if emptyJournal, err = s.MarkNeonRecoveryPriorReleased(ctx, recovery, empty.Component, empty.ResourceID, empty.ImmutableGeneration, emptyToken); err != nil {
		t.Fatal(err)
	}
	untouchedToken := strings.Repeat("d", 64)
	untouchedJournal, err := s.PlanNeonRecoveryResource(ctx, recovery, binding, untouched, "replacement-untouched", untouchedToken)
	if err != nil {
		t.Fatal(err)
	}
	proxy.BranchID = "old-timeline"
	if _, err = s.Pool.Exec(ctx, `UPDATE managed_platform_neon_proxy_endpoints SET branch_id=$2 WHERE endpoint_id=$1`, proxy.EndpointID, proxy.BranchID); err != nil {
		t.Fatal(err)
	}
	if err = s.RebindNeonRecoveryProxyEndpoint(ctx, recovery, binding, proxy, binding.TenantID, binding.TimelineID, proxy.ComputeID); err != nil {
		t.Fatal(err)
	}
	visible, err := s.NeonProxyEndpoint(ctx, proxy.EndpointID)
	if err != nil || visible.BranchID != binding.TimelineID {
		t.Fatalf("proxy endpoint was not rebound: %#v %v", visible, err)
	}
	stale := recovery
	stale.Lease = "stale"
	if _, err = s.NeonRecoveryResources(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale recovery lease read journal: %v", err)
	}
	if _, err = s.ConfirmNeonRecoveryReplacement(ctx, recovery, claim.Component, "wrong-provider", 3, token); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong provider replay crossed confirmation fence: %v", err)
	}
	if err = s.CancelPlatformRecovery(ctx, p, recovery.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NeonRecoveryResources(ctx, recovery); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelled recovery lease read journal: %v", err)
	}
	if _, err = s.BindNeonRecoveryTarget(ctx, recovery, manifest, "staging/"+recovery.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelled recovery replayed its immutable binding: %v", err)
	}
	if _, err = s.MarkNeonRecoveryReplacementReleased(ctx, recovery, replacement.Component, replacement.ResourceID, replacement.ImmutableGeneration, token); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelled recovery mutated without cleanup authority: %v", err)
	}
	if err = s.FencePlatformRecoveryCleanup(ctx, recovery); !errors.Is(err, ErrConflict) {
		t.Fatalf("restore cleanup fence accepted a plain context: %v", err)
	}
	if err = s.FencePlatformRecoveryCleanup(cleanup, recovery); err != nil {
		t.Fatalf("cancelled restore cleanup fence rejected exact binding: %v", err)
	}
	if _, err = s.ConfirmNeonRecoveryReplacement(ctx, recovery, secondary.Component, "restored-secondary", 2, secondaryToken); !errors.Is(err, ErrConflict) {
		t.Fatalf("ordinary confirmation crossed cancelled authority: %v", err)
	}
	wrongJournal := secondaryJournal
	wrongJournal.TransitionToken = strings.Repeat("d", 64)
	if _, err = s.ConfirmNeonRecoveryCleanupReservation(cleanup, recovery, wrongJournal, "restored-secondary", 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("cleanup confirmation accepted the wrong immutable journal: %v", err)
	}
	if _, err = s.ConfirmNeonRecoveryCleanupReservation(cleanup, recovery, secondaryJournal, replacement.ResourceID, replacement.ImmutableGeneration); !errors.Is(err, ErrConflict) {
		t.Fatalf("cleanup confirmation accepted a colliding provider identity: %v", err)
	}
	secondaryJournal, err = s.ConfirmNeonRecoveryCleanupReservation(cleanup, recovery, secondaryJournal, "restored-secondary", 2)
	if err != nil {
		t.Fatalf("cleanup could not confirm the exact reserved provider: %v", err)
	}
	if _, err = s.MarkNeonRecoveryReplacementReleased(cleanup, recovery, secondary.Component, secondaryJournal.ReplacementResourceID, secondaryJournal.ReplacementGeneration, secondaryToken); err != nil {
		t.Fatalf("cleanup could not release confirmed provider: %v", err)
	}
	if _, err = s.CompleteNeonRecoveryResource(cleanup, recovery, secondary.Component, secondaryToken); err != nil {
		t.Fatalf("cleanup could not complete confirmed provider journal: %v", err)
	}
	emptyJournal, err = s.CompleteNeonRecoveryEmptyReservation(cleanup, recovery, emptyJournal)
	if err != nil || emptyJournal.Phase != "empty_complete" {
		t.Fatalf("cleanup could not close empty reservation: %#v %v", emptyJournal, err)
	}
	if replayedEmpty, replayErr := s.CompleteNeonRecoveryEmptyReservation(cleanup, recovery, emptyJournal); replayErr != nil || replayedEmpty != emptyJournal {
		t.Fatalf("empty cleanup completion did not replay: %#v %v", replayedEmpty, replayErr)
	}
	if claims, claimsErr := s.EffectiveNeonRecoveryClaims(cleanup, recovery); claimsErr != nil || claims[empty.Component].ResourceID != "" {
		t.Fatalf("empty cleanup resurfaced the released prior claim: %#v %v", claims[empty.Component], claimsErr)
	}
	untouchedJournal, err = s.CompleteNeonRecoveryUntouchedResource(cleanup, recovery, untouchedJournal)
	if err != nil || untouchedJournal.Phase != "untouched_complete" {
		t.Fatalf("cleanup could not retain untouched prior resource: %#v %v", untouchedJournal, err)
	}
	if replayedUntouched, replayErr := s.CompleteNeonRecoveryUntouchedResource(cleanup, recovery, untouchedJournal); replayErr != nil || replayedUntouched != untouchedJournal {
		t.Fatalf("untouched cleanup completion did not replay: %#v %v", replayedUntouched, replayErr)
	}
	if claims, claimsErr := s.EffectiveNeonRecoveryClaims(cleanup, recovery); claimsErr != nil || claims[untouched.Component].ResourceID != untouched.ResourceID {
		t.Fatalf("untouched cleanup hid the retained prior claim: %#v %v", claims[untouched.Component], claimsErr)
	}
	if _, err = s.MarkNeonRecoveryReplacementReleased(cleanup, recovery, replacement.Component, replacement.ResourceID, replacement.ImmutableGeneration, token); err != nil {
		t.Fatalf("cancelled recovery could not journal exact replacement cleanup: %v", err)
	}
	if _, err = s.CompleteNeonRecoveryResource(cleanup, recovery, replacement.Component, token); err != nil {
		t.Fatalf("cancelled recovery could not complete cleanup journal: %v", err)
	}
	if err = s.FinishPlatformRecovery(ctx, recovery, "cancelled", "fixture cleanup complete"); err == nil {
		t.Fatal("cancelled restore finished without cleanup authority")
	}
	if err = s.SetPlatformRecoveryCleanup(cleanup, recovery, false); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishPlatformRecovery(cleanup, recovery, "cancelled", "fixture cleanup complete"); err != nil {
		t.Fatal(err)
	}
	secondReview, err := s.SavePlatformRecoveryReview(ctx, p, intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptPlatformRecovery(ctx, p, intent, secondReview, "neon-resource-recovery-second"); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "fresh target revision") {
		t.Fatalf("reusing a consumed target revision passed admission: %v", err)
	}
	var consumed bool
	if err = s.Pool.QueryRow(ctx, `SELECT consumed_at IS NOT NULL FROM managed_platform_recovery_reviews WHERE id=$1`, secondReview.ID).Scan(&consumed); err != nil || consumed {
		t.Fatalf("rejected recovery consumed its review: consumed=%v err=%v", consumed, err)
	}
	var operationCount int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM managed_platform_recovery_operations WHERE id<>$1 AND target_platform_id=$2 AND expected_target_revision=$3`, recovery.ID, recovery.TargetPlatformID, recovery.ExpectedTargetRevision).Scan(&operationCount); err != nil || operationCount != 0 {
		t.Fatalf("rejected recovery was inserted: count=%d err=%v", operationCount, err)
	}
	var storedBindingOperation string
	if err = s.Pool.QueryRow(ctx, `SELECT operation_id FROM managed_platform_neon_recovery_bindings WHERE target_platform_id=$1 AND target_revision=$2`, binding.TargetPlatformID, binding.TargetRevision).Scan(&storedBindingOperation); err != nil || storedBindingOperation != binding.OperationID {
		t.Fatalf("rejected recovery changed immutable binding: %q %v", storedBindingOperation, err)
	}
	if _, err = s.ClaimPlatformRecovery(ctx, "neon-resource-lease-second"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("rejected recovery was queued: %v", err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = propagateNeonRecoveryLineageTx(ctx, tx, target.ID, 1, 2, binding.OperationID); err != nil {
		t.Fatalf("recovered claim adoption did not propagate binding lineage: %v", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE managed_platforms SET revision=2 WHERE id=$1 AND revision=1`, target.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NeonRecoveryBindingForTarget(ctx, target.ID, 2); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cancelled recovery binding became effective: %v", err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE managed_platform_recovery_operations SET status='failed' WHERE id=$1 AND status='cancelled'`, recovery.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NeonRecoveryBindingForTarget(ctx, target.ID, 2); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("failed recovery binding became effective: %v", err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE managed_platform_recovery_operations SET status='succeeded' WHERE id=$1 AND status='failed'`, recovery.ID); err != nil {
		t.Fatal(err)
	}
	effectiveBinding, err := s.NeonRecoveryBindingForTarget(ctx, target.ID, 2)
	if err != nil || effectiveBinding != binding {
		t.Fatalf("successful updated target lost its origin recovery binding: %#v %v", effectiveBinding, err)
	}
	if _, err = s.NeonRecoveryBindingForTarget(ctx, source.ID, 1); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unrelated platform resolved recovery lineage: %v", err)
	}
	currentTarget, err := s.ManagedPlatform(ctx, p, target.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	updateReview := managedPlatformReview(t, s, p, currentTarget, plan, currentTarget.Revision, "update")
	if _, err = s.AcceptManagedPlatform(ctx, p, currentTarget, plan, []byte("sealed-lineage-update"), updateReview, currentTarget.Revision, "neon-recovery-lineage-update", "update"); err != nil {
		t.Fatal(err)
	}
	update, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleBinding, err := s.NeonRecoveryBindingForLifecycle(ctx, update)
	if err != nil || lifecycleBinding != binding {
		t.Fatalf("first update could not resolve restored identity before adoption: %#v %v", lifecycleBinding, err)
	}
	if _, err = s.NeonRecoveryBindingForTarget(ctx, target.ID, update.Revision); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("current-revision lookup resolved lineage before adoption: %v", err)
	}
	staleUpdate := update
	staleUpdate.Lease = "stale-lineage-lease"
	if _, err = s.NeonRecoveryBindingForLifecycle(ctx, staleUpdate); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update lease resolved recovery lineage: %v", err)
	}
	wrongPlatform := update
	wrongPlatform.PlatformID = source.ID
	if _, err = s.NeonRecoveryBindingForLifecycle(ctx, wrongPlatform); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong platform resolved recovery lineage: %v", err)
	}
}
