package worker

import (
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
)

func TestDeploymentDeadlineIncludesRecreateTerminationGrace(t *testing.T) {
	owner := rolloutBudgetRuntime{timeout: 2 * time.Minute}
	rolling := spec.Application{Services: map[string]spec.Service{"api": {TerminationGraceSeconds: 1800}}}
	if got, want := deploymentRuntimeTimeout(owner, rolling, time.Minute), 30*time.Second+2*time.Minute; got != want {
		t.Fatalf("rolling update budget=%s want=%s", got, want)
	}
	recreate := spec.Application{Services: map[string]spec.Service{"api": {UpdateStrategy: "recreate", TerminationGraceSeconds: 1800}}}
	if got, want := deploymentRuntimeTimeout(owner, recreate, time.Minute), 30*time.Second+2*time.Minute+30*time.Minute; got != want {
		t.Fatalf("recreate budget=%s want=%s", got, want)
	}
	if got, want := deploymentAttemptTimeout(recreate, time.Minute), 30*time.Second+30*time.Minute; got != want {
		t.Fatalf("recreate attempt timeout=%s want=%s", got, want)
	}
	if got := deploymentAttemptTimeout(rolling, time.Minute); got != time.Minute {
		t.Fatalf("rolling attempt timeout=%s", got)
	}
}
