package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestDeploymentAttemptTimeoutIncludesReviewedSequentialWork(t *testing.T) {
	app := spec.Application{Services: map[string]spec.Service{
		"database": {Image: "database", StartupTimeoutSeconds: 300},
		"migrate":  {Image: "application", Job: &spec.Job{TimeoutSeconds: 240}},
		"web":      {Image: "application", StartupTimeoutSeconds: 600},
		"schedule": {Image: "application", Job: &spec.Job{TimeoutSeconds: 900, Schedule: &spec.JobSchedule{Cron: "0 0 * * *"}}},
	}}
	if got, want := deploymentAttemptTimeout(app, time.Minute), 20*time.Minute; got != want {
		t.Fatalf("deployment timeout = %s, want %s", got, want)
	}
	if got := deploymentAttemptTimeout(spec.Application{}, 4*time.Minute); got != 4*time.Minute {
		t.Fatalf("default deployment timeout changed: %s", got)
	}
	if got := deploymentAttemptTimeout(spec.Application{}, 10*time.Second); got != 10*time.Second {
		t.Fatalf("short default deployment timeout changed: %s", got)
	}
	if got := deploymentOperationTimeout(10*time.Minute, 10*time.Minute*2/3, 5*time.Minute); got != 10*time.Minute {
		t.Fatalf("default operation timeout changed: %s", got)
	}
	if got, want := deploymentOperationTimeout(10*time.Minute, 11*time.Minute, 5*time.Minute), 16*time.Minute+30*time.Second; got != want {
		t.Fatalf("expanded operation timeout = %s, want %s", got, want)
	}
}

func TestDeploymentRecoveryBudgetUsesTheWorkloadThatRollbackWillApply(t *testing.T) {
	desired := spec.Application{Services: map[string]spec.Service{"web": {StartupTimeoutSeconds: 30}}}
	previous := spec.Application{Services: map[string]spec.Service{"web": {StartupTimeoutSeconds: 600}}}
	deployment := store.Deployment{Spec: desired}
	priorRelease := &store.Deployment{ResolvedSpec: &previous}
	if got := deploymentRecoveryBudgetSpec(deployment, priorRelease); got.Services["web"].StartupTimeoutSeconds != 600 {
		t.Fatalf("fresh rollback budget used the attempted release: %+v", got.Services["web"])
	}
	resumed := spec.Application{Services: map[string]spec.Service{"web": {StartupTimeoutSeconds: 900}}}
	deployment.RecoveryState, deployment.RecoverySpec = "running", &resumed
	if got := deploymentRecoveryBudgetSpec(deployment, priorRelease); got.Services["web"].StartupTimeoutSeconds != 900 {
		t.Fatalf("resumed rollback budget ignored its durable recovery spec: %+v", got.Services["web"])
	}
}

func TestDeploymentRuntimeBudgetIncludesSlowSetupAndLateBeat(t *testing.T) {
	owner := rolloutBudgetRuntime{timeout: 120 * time.Second}
	app := spec.Application{Services: map[string]spec.Service{
		"setup": {Job: &spec.Job{TimeoutSeconds: 240}},
		"api":   {}, "worker": {}, "beat": {},
	}}
	got := deploymentRuntimeTimeout(owner, app, 200*time.Second)
	if want := 11 * time.Minute; got != want {
		t.Fatalf("budget=%s want=%s", got, want)
	}
	// Setup plus API/worker used the old shared200s window; Beat still owns
	// its full120s bounded readiness window in the new complete budget.
	if remaining := got - 201*time.Second; remaining < owner.timeout {
		t.Fatalf("late beat lost readiness window: %s", remaining)
	}
	app.Services["api"] = spec.Service{StartupTimeoutSeconds: 300}
	if want := 14 * time.Minute; deploymentRuntimeTimeout(owner, app, time.Minute) != want {
		t.Fatal("mixed explicit/default waits not counted")
	}
}

type rolloutBudgetRuntime struct {
	Runtime
	timeout time.Duration
}

func (r rolloutBudgetRuntime) ServiceRolloutTimeout() time.Duration { return r.timeout }

func TestSlowSetupLeavesLateHealthyBeatItsOwnWindow(t *testing.T) {
	owner := rolloutBudgetRuntime{timeout: 40 * time.Millisecond}
	app := spec.Application{Services: map[string]spec.Service{"setup": {}, "api": {}, "worker": {}, "beat": {}}}
	budget := deploymentRuntimeTimeout(owner, app, 100*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	for _, delay := range []time.Duration{70 * time.Millisecond, 20 * time.Millisecond, 20 * time.Millisecond, 20 * time.Millisecond} {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			t.Fatal("healthy late beat inherited exhausted shared deadline")
		}
	}
	// More aggregate time does not waive the individual unhealthy service bound.
	child, stop := context.WithTimeout(ctx, owner.timeout)
	defer stop()
	select {
	case <-time.After(2 * owner.timeout):
		t.Fatal("unhealthy service escaped its deadline")
	case <-child.Done():
	}
	if !errors.Is(child.Err(), context.DeadlineExceeded) {
		t.Fatal(child.Err())
	}
}
