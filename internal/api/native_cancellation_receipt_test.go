//go:build hakopod_native_acceptance && linux

package api

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/hakopod/hakopod/internal/store"
)

func TestNativeCancellationAdmissionRequiresExactIsolatedCancelledRestore(t *testing.T) {
	op := platformbackup.Operation{ID: strings.Repeat("a", 32), Kind: "restore", Status: "cancelled", CancelRequested: true, Project: "native-neon", Environment: "development", SourcePlatformID: strings.Repeat("b", 32), TargetPlatformID: strings.Repeat("c", 32), ExpectedSourceRevision: 3, ExpectedTargetRevision: 2}
	p := store.Principal{Project: op.Project, Environment: op.Environment, Permissions: []string{"deployments:write"}, IdentityPermissions: []string{"deployments:write"}}
	source := store.ManagedPlatform{ID: op.SourcePlatformID, Project: op.Project, Environment: op.Environment, Revision: op.ExpectedSourceRevision, Status: "ready", Spec: managedplatform.Spec{Kind: "neon"}}
	target := store.ManagedPlatform{ID: op.TargetPlatformID, Project: op.Project, Environment: op.Environment, Revision: op.ExpectedTargetRevision, Status: "failed", Spec: managedplatform.Spec{Kind: "neon"}, Observation: map[string]any{"phase": "recovery-isolated", "recovery_operation_id": op.ID}}
	if err := nativeCancellationAdmission(p, op, source, target); err != nil {
		t.Fatalf("exact cancelled restore was rejected: %v", err)
	}
	for _, change := range []func(*platformbackup.Operation){
		func(v *platformbackup.Operation) { v.Status = "running" },
		func(v *platformbackup.Operation) { v.Kind = "backup" },
		func(v *platformbackup.Operation) { v.CancelRequested = false },
		func(v *platformbackup.Operation) { v.CleanupRequired = true },
		func(v *platformbackup.Operation) { v.Lease = "active-lease" },
		func(v *platformbackup.Operation) { v.TargetPlatformID = v.SourcePlatformID },
		func(v *platformbackup.Operation) { v.ExpectedTargetRevision++ },
		func(v *platformbackup.Operation) { v.ExpectedSourceRevision++ },
	} {
		changed := op
		change(&changed)
		if err := nativeCancellationAdmission(p, changed, source, target); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("invalid cancellation returned %v", err)
		}
	}
	for _, change := range []func(*store.ManagedPlatform){
		func(v *store.ManagedPlatform) { v.Project = "other" },
		func(v *store.ManagedPlatform) { v.Environment = "production" },
		func(v *store.ManagedPlatform) { v.Status = "ready" },
		func(v *store.ManagedPlatform) { v.Spec.Kind = "supabase" },
		func(v *store.ManagedPlatform) { now := time.Now(); v.DeletedAt = &now },
		func(v *store.ManagedPlatform) {
			v.Observation = map[string]any{"phase": "recovery-isolated", "recovery_operation_id": "different-operation"}
		},
	} {
		changed := target
		change(&changed)
		if err := nativeCancellationAdmission(p, op, source, changed); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("changed cancellation target returned %v", err)
		}
	}
	p.IdentityPermissions = []string{"deployments:read"}
	if err := nativeCancellationAdmission(p, op, source, target); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("read-only identity returned %v", err)
	}
}
