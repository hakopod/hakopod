package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/hakopod/hakopod/internal/store"
)

type neonRecoveryTimelineStateFixture struct {
	recovery platformbackup.Operation
	input    store.NeonControllerContext
	called   bool
	err      error
}

func (f *neonRecoveryTimelineStateFixture) WithNeonRecoveryControllerState(_ context.Context, recovery platformbackup.Operation, apply func(store.NeonControllerContext) (managedplatform.NeonControllerState, bool, error)) (bool, error) {
	f.called = true
	if recovery.ID != f.recovery.ID || recovery.Lease != f.recovery.Lease {
		return false, store.ErrConflict
	}
	if f.err != nil {
		return false, f.err
	}
	next, applied, err := apply(f.input)
	if err == nil {
		f.input.State = next
	}
	return applied, err
}

func TestNeonRecoveryInitialRoutingUsesRecoveryFence(t *testing.T) {
	platform := strings.Repeat("a", 32)
	operation := store.ManagedPlatformOperation{ID: strings.Repeat("b", 32), PlatformID: platform, Revision: 1, Spec: managedplatform.Spec{Neon: &managedplatform.NeonConfig{Pageservers: 2}}}
	recovery := platformbackup.Operation{ID: strings.Repeat("c", 32), Kind: "restore", TargetPlatformID: platform, ExpectedTargetRevision: 1, Lease: strings.Repeat("d", 32)}
	fixture := &neonRecoveryTimelineStateFixture{recovery: recovery, err: store.ErrForbidden}
	err := neonRecoveryTimelineObserver(fixture, recovery, operation)(context.Background(), managedplatform.NeonTimelineRoutingObservation{})
	if !fixture.called || !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("recovery observer bypassed its durable restore fence: %v", err)
	}
}
