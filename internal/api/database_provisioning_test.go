package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

type provisioningRuntimeFixture struct {
	apply     func(context.Context) error
	observe   func(context.Context) (database.Observation, error)
	authority func(context.Context) error
}

func (f provisioningRuntimeFixture) ReconcileVitessBackupAuthority(ctx context.Context, _ database.Resource, before func() error) error {
	if err := before(); err != nil {
		return err
	}
	if f.authority != nil {
		return f.authority(ctx)
	}
	return nil
}

func (f provisioningRuntimeFixture) ApplyDatabase(ctx context.Context, _ database.Resource, _ []byte, before func() error) error {
	if err := before(); err != nil {
		return err
	}
	return f.apply(ctx)
}

func (f provisioningRuntimeFixture) ObserveDatabase(ctx context.Context, _ database.Resource) (database.Observation, error) {
	return f.observe(ctx)
}

func TestSplitDatabaseProvisioningGivesHealthAFreshStep(t *testing.T) {
	for _, engine := range []string{"vitess", "oracle"} {
		t.Run(engine, func(t *testing.T) {
			d := database.Resource{Revision: 1, Spec: database.Spec{Engine: engine}}
			applyContext, cancelApply := context.WithCancel(context.Background())
			defer cancelApply()
			applied, observed := 0, 0
			runtime := provisioningRuntimeFixture{
				apply: func(context.Context) error {
					applied++
					// Model a successful apply that consumed the current step's budget.
					cancelApply()
					return nil
				},
				observe: func(ctx context.Context) (database.Observation, error) {
					observed++
					deadline, ok := ctx.Deadline()
					if ctx.Err() != nil || !ok || time.Until(deadline) < 20*time.Second {
						t.Fatal("health inherited the exhausted apply deadline")
					}
					return database.Observation{Status: "ready", Revision: 1}, nil
				},
			}
			status, phase := "", ""
			finish := func(s, p, _ string, _ database.Observation) { status, phase = s, p }
			fence := func() error { return nil }
			reconcileDatabaseProvisioning(applyContext, runtime, d, "provisioning", nil, fence, finish)
			if status != "queued" || phase != "observing" || applied != 1 || observed != 0 {
				t.Fatal("apply did not persist a separate observation step", status, phase, applied, observed)
			}
			observationContext, cancelObservation := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancelObservation()
			// A later claim, including after a restart, resumes from the recorded phase.
			reconcileDatabaseProvisioning(observationContext, runtime, d, phase, nil, fence, finish)
			if status != "succeeded" || phase != "ready" || applied != 1 || observed != 1 {
				t.Fatal("resumed observation reapplied resources or failed to finish", status, phase, applied, observed)
			}
		})
	}
}

func TestSplitDatabasePendingHealthReturnsToReconciliation(t *testing.T) {
	for _, engine := range []string{"vitess", "oracle"} {
		for _, observationError := range []error{nil, errors.New("synthetic unavailable native probe")} {
			name := engine + "/" + map[bool]string{true: "unavailable", false: "pending"}[observationError != nil]
			t.Run(name, func(t *testing.T) {
				d := database.Resource{Spec: database.Spec{Engine: engine}}
				applied, observed := 0, 0
				runtime := provisioningRuntimeFixture{
					apply: func(context.Context) error { applied++; return nil },
					observe: func(context.Context) (database.Observation, error) {
						observed++
						return database.Observation{Status: "pending"}, observationError
					},
				}
				status, phase := "", ""
				finish := func(s, p, _ string, o database.Observation) {
					status, phase = s, p
					if o.Status == "ready" {
						t.Fatal("unverified health became ready")
					}
				}
				fence := func() error { return nil }
				reconcileDatabaseProvisioning(context.Background(), runtime, d, "observing", nil, fence, finish)
				if status != "queued" || phase != "provisioning" || applied != 0 || observed != 1 {
					t.Fatal("pending health did not return to reconciliation", status, phase)
				}
				reconcileDatabaseProvisioning(context.Background(), runtime, d, phase, nil, fence, finish)
				if phase != "observing" || applied != 1 || observed != 1 {
					t.Fatal("retry did not reconcile resources before the next health check")
				}
			})
		}
	}
}

func TestDatabaseProvisioningPreservesApplyFailureAndOtherEngines(t *testing.T) {
	for _, engine := range []string{"vitess", "oracle", "postgresql"} {
		t.Run(engine, func(t *testing.T) {
			d := database.Resource{Spec: database.Spec{Engine: engine}}
			applied, observed := 0, 0
			runtime := provisioningRuntimeFixture{
				apply: func(context.Context) error { applied++; return nil },
				observe: func(context.Context) (database.Observation, error) {
					observed++
					return database.Observation{Status: "ready"}, nil
				},
			}
			status, phase := "", ""
			finish := func(s, p, _ string, _ database.Observation) { status, phase = s, p }
			reconcileDatabaseProvisioning(context.Background(), runtime, d, "provisioning", nil, func() error { return errors.New("synthetic expired authority") }, finish)
			if status != "failed" || phase != "reconcile" || applied != 0 || observed != 0 {
				t.Fatal("failed authority reached apply or observation", status, phase)
			}
			if engine == "postgresql" {
				reconcileDatabaseProvisioning(context.Background(), runtime, d, "observing", nil, func() error { return nil }, finish)
				if status != "succeeded" || phase != "ready" || applied != 1 || observed != 1 {
					t.Fatal("ordinary database single-step reconciliation changed", status, phase)
				}
			}
		})
	}
}

func TestVitessObservationRechecksNativeBackupAuthority(t *testing.T) {
	d := database.Resource{Spec: database.Spec{Engine: "vitess"}}
	checked := false
	runtime := provisioningRuntimeFixture{
		apply: func(context.Context) error { t.Fatal("observation reran apply"); return nil },
		observe: func(context.Context) (database.Observation, error) {
			t.Fatal("revoked native backup authority reached health observation")
			return database.Observation{}, nil
		},
		authority: func(context.Context) error {
			checked = true
			return errors.New("synthetic revoked native backup approval")
		},
	}
	status, phase := "", ""
	reconcileDatabaseProvisioning(context.Background(), runtime, d, "observing", nil, func() error { return nil }, func(s, p, _ string, _ database.Observation) { status, phase = s, p })
	if !checked || status != "failed" || phase != "reconcile" {
		t.Fatal("resumed provisioning ignored current backup authority", checked, status, phase)
	}
}

func TestOracleObservationDoesNotReconcileVitessBackupAuthority(t *testing.T) {
	d := database.Resource{Spec: database.Spec{Engine: "oracle"}}
	runtime := provisioningRuntimeFixture{
		apply: func(context.Context) error { t.Fatal("observation reran apply"); return nil },
		observe: func(context.Context) (database.Observation, error) {
			return database.Observation{Status: "ready"}, nil
		},
		authority: func(context.Context) error {
			t.Fatal("Oracle observation reconciled Vitess backup authority")
			return nil
		},
	}
	status, phase := "", ""
	reconcileDatabaseProvisioning(context.Background(), runtime, d, "observing", nil, func() error { return nil }, func(s, p, _ string, _ database.Observation) { status, phase = s, p })
	if status != "succeeded" || phase != "ready" {
		t.Fatal("Oracle observation did not finish", status, phase)
	}
}
