//go:build hakopod_native_acceptance && linux

package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/hakopod/hakopod/internal/store"
)

func nativeReceiptFixture() (platformbackup.Operation, platformbackup.StoredArtifact) {
	id := strings.Repeat("a", 32)
	m := platformbackup.Manifest{SchemaVersion: 1, Format: platformbackup.NeonFormat, PlatformID: id, PlatformRevision: 3, PlatformSpec: json.RawMessage(`{"kind":"neon","private_configuration":"must-stay-private"}`), Release: "neon-test", Images: map[string]string{"storage": "registry.example/neon@sha256:" + strings.Repeat("b", 64)}, SourceNamespace: "managed-platform-" + id, SourceNamespaceUID: "source-namespace", Verification: map[string]string{}, CapturedAt: time.Unix(30, 0), FrozenAt: time.Unix(20, 0), ThawedAt: time.Unix(40, 0), Consistency: "fenced capture", DestinationID: strings.Repeat("c", 32), EncryptionRecipient: "private-encryption-reference", Neon: &platformbackup.NeonIdentity{TenantID: strings.Repeat("1", 32), TimelineID: strings.Repeat("2", 32), TenantGeneration: 4, TimelineGeneration: 5, CommitLSN: "1/20", PageserverRemoteConsistentLSNs: map[string]string{"0": "1/20", "1": "1/21"}, SourceObjectPrefix: "source-prefix/", ObjectInventorySHA256: strings.Repeat("d", 64), ObjectCount: 2, ObjectBytes: 4}}
	for _, key := range []string{"tenant_identity", "tenant_generation", "timeline_identity", "timeline_generation", "remote_storage"} {
		m.Verification[key] = strings.Repeat("e", 64)
	}
	for _, name := range platformbackup.NeonRequiredParts {
		m.Parts = append(m.Parts, platformbackup.Part{Name: name, SHA256: strings.Repeat("f", 64), Bytes: 1})
	}
	m.ManifestSHA256 = m.Digest()
	artifact := platformbackup.StoredArtifact{ID: strings.Repeat("6", 32), ObjectKey: "private-object-key", Manifest: m}
	op := platformbackup.Operation{ID: strings.Repeat("7", 32), Kind: "backup", Status: "succeeded", SourcePlatformID: id, ExpectedSourceRevision: 3, ResultArtifactID: artifact.ID}
	return op, artifact
}

func TestNativeRecoveryReceiptRequiresCurrentExactSourceAndTarget(t *testing.T) {
	op, _ := nativeReceiptFixture()
	op.Kind, op.Project, op.Environment = "restore", "native-neon", "development"
	op.TargetPlatformID, op.ExpectedTargetRevision = strings.Repeat("8", 32), 2
	p := store.Principal{Project: op.Project, Environment: op.Environment, Permissions: []string{"deployments:write"}, IdentityPermissions: []string{"deployments:write"}}
	for _, target := range []bool{false, true} {
		item := store.ManagedPlatform{ID: op.SourcePlatformID, Project: op.Project, Environment: op.Environment, Revision: op.ExpectedSourceRevision, Status: "ready", Spec: managedplatform.Spec{Kind: "neon"}}
		if target {
			item.ID, item.Revision = op.TargetPlatformID, op.ExpectedTargetRevision
		}
		if err := nativeRecoveryPlatformAdmission(p, op, item, target); err != nil {
			t.Fatalf("current exact platform was rejected: %v", err)
		}
		for _, change := range []func(*store.ManagedPlatform){
			func(value *store.ManagedPlatform) { value.ID = strings.Repeat("9", 32) },
			func(value *store.ManagedPlatform) { value.Project = "other" },
			func(value *store.ManagedPlatform) { value.Environment = "production" },
			func(value *store.ManagedPlatform) { value.Revision++ },
			func(value *store.ManagedPlatform) { value.Spec.Kind = "supabase" },
			func(value *store.ManagedPlatform) { value.Status = "failed" },
			func(value *store.ManagedPlatform) { now := time.Now(); value.DeletedAt = &now },
		} {
			changed := item
			change(&changed)
			if err := nativeRecoveryPlatformAdmission(p, op, changed, target); err == nil {
				t.Fatal("receipt admitted a changed or inaccessible source/target")
			}
		}
		readOnly := p
		readOnly.Permissions = []string{"deployments:read"}
		if err := nativeRecoveryPlatformAdmission(readOnly, op, item, target); err == nil {
			t.Fatal("receipt admitted a read-only credential")
		}
	}
	op.TargetPlatformID = op.SourcePlatformID
	item := store.ManagedPlatform{ID: op.SourcePlatformID, Project: op.Project, Environment: op.Environment, Revision: op.ExpectedTargetRevision, Status: "ready", Spec: managedplatform.Spec{Kind: "neon"}}
	if err := nativeRecoveryPlatformAdmission(p, op, item, true); err == nil {
		t.Fatal("receipt admitted a restore into the source")
	}
}

func TestNativeRecoveryReceiptSelectsPublishedArtifactFacts(t *testing.T) {
	op, artifact := nativeReceiptFixture()
	receipt, err := nativeRecoveryArtifactReceipt(op, artifact)
	if err != nil || receipt.Neon.CommitLSN != "1/20" || receipt.ManifestSHA256 != artifact.Manifest.Digest() || len(receipt.Parts) != 3 {
		t.Fatalf("valid native artifact receipt failed: %v", err)
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-object-key", "private-encryption-reference", "must-stay-private", "restored_data_sha256", "cleanup_replayed", "isolated_target"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("native receipt exposed private or unmeasured field %s", forbidden)
		}
	}
}

func TestNativeRecoveryReceiptRejectsMismatchedAndUnfinishedArtifacts(t *testing.T) {
	for _, change := range []func(*platformbackup.Operation, *platformbackup.StoredArtifact){
		func(op *platformbackup.Operation, _ *platformbackup.StoredArtifact) { op.Status = "running" },
		func(op *platformbackup.Operation, _ *platformbackup.StoredArtifact) {
			op.SourcePlatformID = strings.Repeat("8", 32)
		},
		func(op *platformbackup.Operation, _ *platformbackup.StoredArtifact) { op.ExpectedSourceRevision++ },
		func(_ *platformbackup.Operation, artifact *platformbackup.StoredArtifact) {
			artifact.ID = strings.Repeat("9", 32)
		},
		func(_ *platformbackup.Operation, artifact *platformbackup.StoredArtifact) {
			artifact.Manifest.ManifestSHA256 = strings.Repeat("0", 64)
		},
		func(_ *platformbackup.Operation, artifact *platformbackup.StoredArtifact) {
			artifact.Manifest.Neon.CommitLSN = "0/0"
		},
	} {
		op, artifact := nativeReceiptFixture()
		change(&op, &artifact)
		if _, err := nativeRecoveryArtifactReceipt(op, artifact); err == nil {
			t.Fatal("native receipt accepted a mismatched or unfinished artifact")
		}
	}
}
