package cluster

import (
	"context"
	"fmt"
	"io"

	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/hakopod/hakopod/internal/store"
)

type PlatformRecoveryRuntime struct {
	Store                 *store.Store
	Supabase              platformbackup.Runtime
	Neon                  platformbackup.Runtime
	ValidateQualification func(context.Context, platformbackup.Operation, string) error
}

func (r *PlatformRecoveryRuntime) runtime(ctx context.Context, op platformbackup.Operation) (platformbackup.Runtime, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("managed platform recovery runtime is unavailable")
	}
	item, _, err := r.Store.ManagedPlatformRecoveryContract(ctx, op.SourcePlatformID, op.ExpectedSourceRevision)
	if err != nil {
		return nil, err
	}
	if !platformbackup.RecoveryCleanupFromContext(ctx) {
		if r.ValidateQualification == nil {
			return nil, fmt.Errorf("managed platform recovery qualification is unavailable")
		}
		if err := r.ValidateQualification(ctx, op, item.Spec.Kind); err != nil {
			return nil, err
		}
	}
	switch item.Spec.Kind {
	case "supabase":
		if r.Supabase != nil {
			return r.Supabase, nil
		}
	case "neon":
		if r.Neon != nil {
			return r.Neon, nil
		}
	}
	return nil, fmt.Errorf("%s recovery runtime is unavailable", item.Spec.Kind)
}
func (r *PlatformRecoveryRuntime) ResolveSource(ctx context.Context, op platformbackup.Operation) (platformbackup.Manifest, error) {
	v, e := r.runtime(ctx, op)
	if e != nil {
		return platformbackup.Manifest{}, e
	}
	return v.ResolveSource(ctx, op)
}
func (r *PlatformRecoveryRuntime) ResolveEmptyTarget(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) error {
	v, e := r.runtime(ctx, op)
	if e != nil {
		return e
	}
	return v.ResolveEmptyTarget(ctx, op, m)
}
func (r *PlatformRecoveryRuntime) PreflightRestoreTarget(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) error {
	v, err := r.runtime(ctx, op)
	if err != nil {
		return err
	}
	if preflight, ok := v.(interface {
		PreflightRestoreTarget(context.Context, platformbackup.Operation, platformbackup.Manifest) error
	}); ok {
		return preflight.PreflightRestoreTarget(ctx, op, m)
	}
	return nil
}
func (r *PlatformRecoveryRuntime) PauseWrites(ctx context.Context, op platformbackup.Operation) error {
	v, e := r.runtime(ctx, op)
	if e != nil {
		return e
	}
	return v.PauseWrites(ctx, op)
}
func (r *PlatformRecoveryRuntime) DrainWrites(ctx context.Context, op platformbackup.Operation) error {
	v, e := r.runtime(ctx, op)
	if e != nil {
		return e
	}
	return v.DrainWrites(ctx, op)
}
func (r *PlatformRecoveryRuntime) Capture(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) ([]platformbackup.CapturedPart, map[string]string, error) {
	v, e := r.runtime(ctx, op)
	if e != nil {
		return nil, nil, e
	}
	return v.Capture(ctx, op, m)
}

func (r *PlatformRecoveryRuntime) FinalizeCapture(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) (platformbackup.Manifest, error) {
	v, err := r.runtime(ctx, op)
	if err != nil {
		return m, err
	}
	if finalizer, ok := v.(interface {
		FinalizeCapture(context.Context, platformbackup.Operation, platformbackup.Manifest) (platformbackup.Manifest, error)
	}); ok {
		return finalizer.FinalizeCapture(ctx, op, m)
	}
	return m, nil
}
func (r *PlatformRecoveryRuntime) CleanupRestore(ctx context.Context, op platformbackup.Operation) error {
	v, err := r.runtime(ctx, op)
	if err != nil {
		return err
	}
	cleaner, ok := v.(interface {
		CleanupRestore(context.Context, platformbackup.Operation) error
	})
	if !ok {
		return fmt.Errorf("managed platform restore cleanup is unavailable")
	}
	return cleaner.CleanupRestore(ctx, op)
}
func (r *PlatformRecoveryRuntime) ReobserveSourceClaims(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) error {
	v, e := r.runtime(ctx, op)
	if e != nil {
		return e
	}
	return v.ReobserveSourceClaims(ctx, op, m)
}
func (r *PlatformRecoveryRuntime) ResumeSource(ctx context.Context, op platformbackup.Operation) error {
	v, e := r.runtime(ctx, op)
	if e != nil {
		return e
	}
	return v.ResumeSource(ctx, op)
}
func (r *PlatformRecoveryRuntime) RestorePart(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest, p platformbackup.Part, in io.Reader) error {
	v, e := r.runtime(ctx, op)
	if e != nil {
		return e
	}
	return v.RestorePart(ctx, op, m, p, in)
}
func (r *PlatformRecoveryRuntime) VerifyRestoredContent(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) error {
	v, e := r.runtime(ctx, op)
	if e != nil {
		return e
	}
	return v.VerifyRestoredContent(ctx, op, m)
}
func (r *PlatformRecoveryRuntime) VerifyRestoredRuntime(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) error {
	v, e := r.runtime(ctx, op)
	if e != nil {
		return e
	}
	return v.VerifyRestoredRuntime(ctx, op, m)
}
