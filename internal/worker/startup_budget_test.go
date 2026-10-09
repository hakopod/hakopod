package worker

import (
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
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
