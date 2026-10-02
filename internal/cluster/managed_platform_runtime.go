package cluster

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
)

const maxManagedPlatformSnapshotBytes = managedplatform.MaxManagedPlatformSnapshotBytes

type ManagedPlatformSnapshot struct {
	Version      int                     `json:"version"`
	ReviewedPlan managedplatform.Plan    `json:"reviewed_plan"`
	Supabase     *SupabaseRuntimeRequest `json:"supabase,omitempty"`
	Neon         *NeonRuntimeRequest     `json:"neon,omitempty"`
}

func SealManagedPlatformSnapshot(key []byte, platformID string, revision int64, kind string, snapshot ManagedPlatformSnapshot) ([]byte, error) {
	if len(key) != 32 || platformID == "" || revision < 1 {
		return nil, fmt.Errorf("managed platform snapshot encryption is unavailable")
	}
	snapshot.Version = 1
	plain, err := json.Marshal(snapshot)
	if err != nil || len(plain) > maxManagedPlatformSnapshotBytes {
		return nil, fmt.Errorf("managed platform snapshot is invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	aad := []byte(fmt.Sprintf("hakopod-managed-platform-v1:%s:%d:%s", platformID, revision, kind))
	return gcm.Seal(nonce, nonce, plain, aad), nil
}
func OpenManagedPlatformSnapshot(key []byte, op store.ManagedPlatformOperation) (ManagedPlatformSnapshot, error) {
	var snapshot ManagedPlatformSnapshot
	if len(key) != 32 {
		return snapshot, fmt.Errorf("managed platform snapshot encryption is unavailable")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return snapshot, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(op.EncryptedSnapshot) < gcm.NonceSize() || len(op.EncryptedSnapshot) > managedplatform.MaxManagedPlatformEncryptedSnapshotBytes {
		return snapshot, fmt.Errorf("managed platform snapshot is invalid")
	}
	nonce, data := op.EncryptedSnapshot[:gcm.NonceSize()], op.EncryptedSnapshot[gcm.NonceSize():]
	aad := []byte(fmt.Sprintf("hakopod-managed-platform-v1:%s:%d:%s", op.PlatformID, op.Revision, op.Kind))
	plain, err := gcm.Open(nil, nonce, data, aad)
	if err != nil {
		return snapshot, fmt.Errorf("managed platform snapshot could not be authenticated")
	}
	if err = json.Unmarshal(plain, &snapshot); err != nil || snapshot.Version != 1 {
		return ManagedPlatformSnapshot{}, fmt.Errorf("managed platform snapshot is invalid")
	}
	return snapshot, nil
}

type ManagedPlatformRuntime struct {
	Cluster                         *Client
	EncryptionKey                   []byte
	validateManagedPlatformCapacity func(context.Context, *store.Store, string) error
}

func (r *ManagedPlatformRuntime) ReconcileManagedPlatform(ctx context.Context, state *store.Store, op store.ManagedPlatformOperation) error {
	if r == nil {
		return managedPlatformRuntimeError("runtime_unavailable", fmt.Errorf("managed platform cluster runtime is unavailable"))
	}
	if state != nil && state.ManagedCloud && op.Kind != "delete" {
		if state.ManagedPlatformCapacityBudget == nil || state.ValidateManagedPlatformCapacity == nil {
			return managedPlatformRuntimeError("capacity_admission", fmt.Errorf("managed platform reconciliation requires durable capacity admission"))
		}
		validate := r.validateManagedPlatformCapacity
		if validate == nil {
			validate = func(ctx context.Context, state *store.Store, platformID string) error {
				return state.CheckManagedPlatformOperationCapacity(ctx, platformID)
			}
		}
		if err := validate(ctx, state, op.PlatformID); err != nil {
			return managedPlatformRuntimeError("capacity_admission", err)
		}
	}
	if r.Cluster == nil {
		return managedPlatformRuntimeError("runtime_unavailable", fmt.Errorf("managed platform cluster runtime is unavailable"))
	}
	snapshot, err := OpenManagedPlatformSnapshot(r.EncryptionKey, op)
	if err != nil {
		return managedPlatformRuntimeError("snapshot_invalid", err)
	}
	switch op.Spec.Kind {
	case "supabase":
		if snapshot.Supabase == nil {
			return managedPlatformRuntimeError("supabase_snapshot_missing", fmt.Errorf("Supabase runtime snapshot is unavailable"))
		}
		request := *snapshot.Supabase
		request.Operation = op
		request.Render.Assets, err = managedplatform.PinnedSupabaseAssets()
		if err != nil {
			return managedPlatformRuntimeError("supabase_assets_invalid", err)
		}
		if request.Render.PlatformID != op.PlatformID || request.Render.Revision != op.Revision || !bytes.Equal(store.JSON(request.Render.Spec), store.JSON(op.Spec)) || !bytes.Equal(store.JSON(snapshot.ReviewedPlan), store.JSON(op.Plan)) || !sameSupabaseTopology(mustSupabasePlan(request.Render), op.Plan) {
			return managedPlatformRuntimeError("supabase_snapshot_mismatch", fmt.Errorf("Supabase runtime snapshot does not match the durable operation"))
		}
		return r.Cluster.ReconcileSupabaseOperation(ctx, state, request)
	case "neon":
		if snapshot.Neon == nil {
			return managedPlatformRuntimeError("neon_snapshot_missing", fmt.Errorf("Neon runtime snapshot is unavailable"))
		}
		request := *snapshot.Neon
		request.Operation = op
		if request.Render.PlatformID != op.PlatformID || request.Render.Revision != op.Revision || !bytes.Equal(store.JSON(request.Render.Spec), store.JSON(op.Spec)) || !bytes.Equal(store.JSON(snapshot.ReviewedPlan), store.JSON(op.Plan)) || !sameNeonTopology(mustNeonPlan(request.Render), op.Plan) {
			return managedPlatformRuntimeError("neon_snapshot_mismatch", fmt.Errorf("Neon runtime snapshot does not match the durable operation"))
		}
		return r.Cluster.ReconcileNeonOperation(ctx, state, request, r.EncryptionKey)
	default:
		return managedPlatformRuntimeError("unsupported_kind", fmt.Errorf("managed platform kind is not supported by this runtime"))
	}
}
func mustNeonPlan(input managedplatform.NeonRenderInput) managedplatform.Plan {
	plan, _ := managedplatform.PlanNeon(input.Spec, input.Images)
	plan.Namespace = "managed-platform-" + input.PlatformID
	return plan
}

func sameNeonTopology(a, b managedplatform.Plan) bool {
	return a.Namespace == b.Namespace && a.PublicService == b.PublicService && bytes.Equal(store.JSON(a.Components), store.JSON(b.Components))
}

func mustSupabasePlan(input managedplatform.SupabaseRenderInput) managedplatform.Plan {
	plan, _ := managedplatform.PlanSupabase(input.Spec, input.Images)
	plan.Namespace = "managed-platform-" + input.PlatformID
	return plan
}

func sameSupabaseTopology(a, b managedplatform.Plan) bool {
	return a.Namespace == b.Namespace && a.PublicService == b.PublicService && bytes.Equal(store.JSON(a.Components), store.JSON(b.Components))
}
