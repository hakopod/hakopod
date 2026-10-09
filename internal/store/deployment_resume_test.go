package store

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/spec"
	"strings"
	"testing"
)

func TestResumeDeploymentSelectsFailedAndDependentsAtSameRevision(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	app := spec.Application{Name: "resume-app", Services: map[string]spec.Service{"migrate": {Image: "image:tag", Job: &spec.Job{TimeoutSeconds: 30}}, "healthy": {Image: "image:tag", DependsOn: []string{"migrate"}}, "failed": {Image: "image:tag", DependsOn: []string{"migrate"}}, "dependent": {Image: "image:tag", DependsOn: []string{"failed"}}}}
	d, err := s.Accept(ctx, p, "demo", "development", app, 0, "resume-initial")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	resolved := app
	for name, svc := range resolved.Services {
		svc.Image = "example.invalid/" + name + "@sha256:" + strings.Repeat("a", 64)
		resolved.Services[name] = svc
	}
	if _, err = claim.SetResolved(ctx, resolved); err != nil {
		t.Fatal(err)
	}
	result := map[string]any{"status": "partial", "attempt": map[string]any{"attempted": []string{"failed", "healthy", "migrate"}, "succeeded": []string{"healthy", "migrate"}, "failed": []string{"failed"}, "blocked": []string{"dependent"}}, "services": []map[string]any{{"name": "migrate", "status": "completed"}, {"name": "healthy", "status": "ready"}, {"name": "failed", "status": "failed"}, {"name": "dependent", "status": "missing"}}}
	if err = claim.Finish(ctx, "failed", "fixture", result); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	resumed, err := s.ResumeDeployment(ctx, p, d.ID, d.Revision, "resume-request-one")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != "queued" || resumed.ResumeGeneration != 1 || strings.Join(resumed.ResumeServices, ",") != "dependent,failed" {
		t.Fatal(resumed.Status, resumed.ResumeGeneration, resumed.ResumeServices)
	}
	replay, err := s.ResumeDeployment(ctx, p, d.ID, d.Revision, "resume-request-one")
	if err != nil || replay.ResumeGeneration != 1 {
		t.Fatal(err, replay.ResumeGeneration)
	}
	claim, err = s.Claim(ctx)
	if err != nil || claim == nil || claim.Deployment.ResumeGeneration != 1 || claim.Deployment.StartedAt == nil {
		t.Fatal(err, claim)
	}
	if err = claim.Finish(ctx, "failed", "second fixture", result); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	second, err := s.ResumeDeployment(ctx, p, d.ID, d.Revision, "resume-request-two")
	if err != nil || second.ResumeGeneration != 2 || second.StartedAt != nil {
		t.Fatal(err, second.ResumeGeneration, second.StartedAt)
	}
	var attempts, finished int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*),count(*) FILTER (WHERE finished_at IS NOT NULL) FROM deployment_resume_attempts WHERE deployment_id=$1", d.ID).Scan(&attempts, &finished); err != nil || attempts != 3 || finished != 2 {
		t.Fatal(err, attempts, finished)
	}
}

func TestResumeDeploymentRefusesFailureWithoutServiceAttemptEvidence(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	app := spec.Application{Name: "resume-preflight", Services: map[string]spec.Service{"web": {Image: "image:tag"}}}
	d, err := s.Accept(ctx, p, "demo", "development", app, 0, "resume-preflight-initial")
	if err != nil {
		t.Fatal(err)
	}
	claim, _ := s.Claim(ctx)
	resolved := app
	svc := resolved.Services["web"]
	svc.Image = "example.invalid/web@sha256:" + strings.Repeat("c", 64)
	resolved.Services["web"] = svc
	claim.SetResolved(ctx, resolved)
	claim.Finish(ctx, "failed", "preflight fixture", map[string]any{"status": "partial", "services": []map[string]any{{"name": "web", "status": "failed"}}})
	claim.Release()
	if _, err = s.ResumeDeployment(ctx, p, d.ID, d.Revision, "resume-preflight-request"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestResumeDeploymentRefusesFailedMigrationJob(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	app := spec.Application{Name: "resume-job", Services: map[string]spec.Service{"migrate": {Image: "image:tag", Job: &spec.Job{TimeoutSeconds: 30}}}}
	d, err := s.Accept(ctx, p, "demo", "development", app, 0, "resume-job-initial")
	if err != nil {
		t.Fatal(err)
	}
	claim, _ := s.Claim(ctx)
	resolved := app
	svc := resolved.Services["migrate"]
	svc.Image = "example.invalid/migrate@sha256:" + strings.Repeat("b", 64)
	resolved.Services["migrate"] = svc
	claim.SetResolved(ctx, resolved)
	claim.Finish(ctx, "failed", "fixture", map[string]any{"status": "pending", "attempt": map[string]any{"attempted": []string{"migrate"}, "failed": []string{"migrate"}}, "services": []map[string]any{{"name": "migrate", "status": "failed"}}})
	claim.Release()
	if _, err = s.ResumeDeployment(ctx, p, d.ID, d.Revision, "resume-job-request"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
