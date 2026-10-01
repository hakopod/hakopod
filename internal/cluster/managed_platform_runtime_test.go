package cluster

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
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
}
