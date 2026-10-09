package worker

import (
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
