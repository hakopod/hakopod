package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestManagedPlatformSnapshotIsAuthenticatedAndDoesNotExposeSecrets(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	op := store.ManagedPlatformOperation{PlatformID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Revision: 1, Kind: "create"}
	secret := []byte("hakopod-test-private-value")
	snapshot := ManagedPlatformSnapshot{ReviewedPlan: managedplatform.Plan{Namespace: "managed-platform-" + op.PlatformID}, Supabase: &SupabaseRuntimeRequest{Render: managedplatform.SupabaseRenderInput{PlatformID: op.PlatformID, Revision: 1}, SecretSnapshots: map[string]map[string][]byte{"snapshot-r1": {"value": secret}}}}
	sealed, err := SealManagedPlatformSnapshot(key, op.PlatformID, op.Revision, op.Kind, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, secret) {
		t.Fatal("sealed snapshot exposed a secret body")
	}
	op.EncryptedSnapshot = sealed
	opened, err := OpenManagedPlatformSnapshot(key, op)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened.Supabase.SecretSnapshots["snapshot-r1"]["value"], secret) {
		t.Fatal("snapshot did not round trip")
	}
	op.Revision++
	if _, err = OpenManagedPlatformSnapshot(key, op); err == nil {
		t.Fatal("snapshot authenticated under a different revision")
	}
}

func TestManagedCloudCapacityGuardFencesPlatformReconciliation(t *testing.T) {
	runtime := &ManagedPlatformRuntime{}
	state := &store.Store{ManagedCloud: true}
	if err := runtime.ReconcileManagedPlatform(context.Background(), state, store.ManagedPlatformOperation{Kind: "create"}); err == nil || !strings.Contains(err.Error(), "capacity admission") {
		t.Fatalf("create reconciliation was not capacity fenced: %v", err)
	}
	if err := runtime.ReconcileManagedPlatform(context.Background(), state, store.ManagedPlatformOperation{Kind: "update"}); err == nil || !strings.Contains(err.Error(), "capacity admission") {
		t.Fatalf("update reconciliation was not capacity fenced: %v", err)
	}
	if err := runtime.ReconcileManagedPlatform(context.Background(), state, store.ManagedPlatformOperation{Kind: "delete"}); err == nil || strings.Contains(err.Error(), "capacity admission") {
		t.Fatalf("delete reconciliation was capacity fenced: %v", err)
	}
	state.ManagedPlatformCapacityBudget = func(context.Context, pgx.Tx, string, string) (managedplatform.CapacityPolicy, error) {
		return managedplatform.CapacityPolicy{}, nil
	}
	state.ValidateManagedPlatformCapacity = func(context.Context, string, string, managedplatform.CapacityPolicy) error { return nil }
	checks := 0
	runtime.validateManagedPlatformCapacity = func(_ context.Context, _ *store.Store, platformID string) error {
		checks++
		if platformID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
			t.Fatal("reconciliation capacity check used the wrong platform")
		}
		return store.ErrConflict
	}
	err := runtime.ReconcileManagedPlatform(context.Background(), state, store.ManagedPlatformOperation{Kind: "update", PlatformID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if !errors.Is(err, store.ErrConflict) || checks != 1 {
		t.Fatalf("reconciliation did not recheck the current grant and physical node capacity: checks=%d err=%v", checks, err)
	}
}

func TestManagedPlatformSnapshotBoundSupportsNativeRuntimeAndFailsClosed(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	op := store.ManagedPlatformOperation{PlatformID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Revision: 1, Kind: "create"}
	names := managedplatform.SupabaseRequiredSecretKeys()
	snapshots := make(map[string]map[string][]byte, len(names))
	snapshotNames := make([]string, 0, len(names))
	for i, name := range names {
		snapshotName := fmt.Sprintf("native-runtime-%02d-r1", i)
		snapshotNames = append(snapshotNames, snapshotName)
		snapshots[snapshotName] = map[string][]byte{"value": bytes.Repeat([]byte{name[0]}, (64<<10)-len("value"))}
	}
	if err := validateSupabaseSecretSnapshot(snapshots, snapshotNames); err != nil {
		t.Fatalf("test fixture violates the accepted Supabase secret contract: %v", err)
	}
	snapshot := ManagedPlatformSnapshot{ReviewedPlan: managedplatform.Plan{Namespace: "managed-platform-" + op.PlatformID}, Supabase: &SupabaseRuntimeRequest{Render: managedplatform.SupabaseRenderInput{PlatformID: op.PlatformID, Revision: 1}, SecretSnapshots: snapshots}}
	plain, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) <= 60<<10 || len(plain) >= maxManagedPlatformSnapshotBytes {
		t.Fatalf("valid contract snapshot size %d is outside the expanded bounded range", len(plain))
	}
	sealed, err := SealManagedPlatformSnapshot(key, op.PlatformID, op.Revision, op.Kind, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	op.EncryptedSnapshot = sealed
	opened, err := OpenManagedPlatformSnapshot(key, op)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Supabase.SecretSnapshots) != len(snapshots) {
		t.Fatal("large bounded snapshot did not round trip")
	}
	wrongAuthority := op
	wrongAuthority.PlatformID = "cccccccccccccccccccccccccccccccc"
	if _, err = OpenManagedPlatformSnapshot(key, wrongAuthority); err == nil {
		t.Fatal("snapshot authenticated for the wrong platform authority")
	}
	tampered := op
	tampered.EncryptedSnapshot = append([]byte(nil), sealed...)
	tampered.EncryptedSnapshot[len(tampered.EncryptedSnapshot)-1] ^= 1
	if _, err = OpenManagedPlatformSnapshot(key, tampered); err == nil {
		t.Fatal("tampered snapshot authenticated")
	}
	oversized := snapshot
	oversized.Supabase = &SupabaseRuntimeRequest{Render: snapshot.Supabase.Render, SecretSnapshots: map[string]map[string][]byte{"oversized-r1": {"value": bytes.Repeat([]byte("x"), maxManagedPlatformSnapshotBytes)}}}
	if _, err = SealManagedPlatformSnapshot(key, op.PlatformID, op.Revision, op.Kind, oversized); err == nil {
		t.Fatal("snapshot beyond the plaintext bound was sealed")
	}
	oversizedCiphertext := op
	oversizedCiphertext.EncryptedSnapshot = make([]byte, maxManagedPlatformSnapshotBytes+12+16+1)
	if _, err = OpenManagedPlatformSnapshot(key, oversizedCiphertext); err == nil {
		t.Fatal("ciphertext beyond the encrypted snapshot bound was opened")
	}
}
