//go:build hakopod_native_acceptance && linux

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

func TestNativeNeonMigrationFenceAllowsCallbackAndBlocksRevision(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed"), review, 0, "native-migration-create", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, op, "succeeded", "ready", "", nil); err != nil {
		t.Fatal(err)
	}
	op, err = s.ManagedPlatformRecoverySnapshot(ctx, item.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	err = s.WithNativeNeonRevision(ctx, op, func() error {
		if _, err := s.WithNeonControllerState(ctx, item.ID, 1, func(input NeonControllerContext) (managedplatform.NeonControllerState, bool, error) {
			return input.State, true, nil
		}); err != nil {
			return err
		}
		bounded, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		if _, err := s.Pool.Exec(bounded, `UPDATE managed_platforms SET revision=revision+1 WHERE id=$1`, item.ID); err == nil {
			t.Fatal("revision changed during native migration")
		}
		return nil
	})
	if err != nil {
		t.Fatal("migration prevented its own callback", err)
	}
	op.Revision = 2
	if err = s.WithNativeNeonRevision(ctx, op, func() error { t.Fatal("stale native mutation entered"); return nil }); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
