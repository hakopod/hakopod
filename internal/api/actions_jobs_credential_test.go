package api

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type jobsCredentialRuntime struct {
	*actionsFake
	refs        []string
	targets     []cluster.Target
	unavailable string
}

func (f *jobsCredentialRuntime) ActionsCredential(_ context.Context, target cluster.Target, ref string) (string, error) {
	f.refs = append(f.refs, ref)
	f.targets = append(f.targets, target)
	if ref == f.unavailable {
		return "", errors.New("application secret is unavailable")
	}
	return "synthetic-credential-for-unit-tests", nil
}

func TestRunnerManagementAndJobAccessUseSeparateCredentials(t *testing.T) {
	for _, selected := range []string{"", "job-read"} {
		t.Run(selected, func(t *testing.T) {
			runtime := &jobsCredentialRuntime{actionsFake: &actionsFake{}}
			server := &Server{actionsTestRuntime: runtime}
			pool := store.ActionsPool{
				ApplicationID: "application", Project: "project", Environment: "development", ApplicationName: "pool",
				Config: spec.Service{Actions: &spec.Actions{Repository: "team/repo", Credential: "runner-management", JobsCredential: selected}},
			}
			if _, err := server.runnerClient(context.Background(), actionsTarget(pool), pool.Config); err != nil {
				t.Fatal(err)
			}
			if _, err := server.workflowClient(context.Background(), pool); err != nil {
				t.Fatal(err)
			}
			jobs := selected
			if jobs == "" {
				jobs = "runner-management"
			}
			if want := []string{"runner-management", jobs}; !reflect.DeepEqual(runtime.refs, want) {
				t.Fatalf("credential references = %v, want %v", runtime.refs, want)
			}
			for _, target := range runtime.targets {
				if target.ApplicationID != pool.ApplicationID || target.Project != pool.Project || target.Environment != pool.Environment || target.Spec.Name != pool.ApplicationName {
					t.Fatal("credential lookup escaped the owning application's scope")
				}
			}
		})
	}
}

func TestUnavailableJobCredentialDoesNotFallBackToManagementAccess(t *testing.T) {
	runtime := &jobsCredentialRuntime{actionsFake: &actionsFake{}, unavailable: "job-read"}
	server := &Server{actionsTestRuntime: runtime}
	pool := store.ActionsPool{Config: spec.Service{Actions: &spec.Actions{Repository: "team/repo", Credential: "runner-management", JobsCredential: "job-read"}}}
	if _, err := server.workflowClient(context.Background(), pool); err == nil {
		t.Fatal("missing selected job credential was accepted")
	}
	if !reflect.DeepEqual(runtime.refs, []string{"job-read"}) {
		t.Fatalf("selected job access fell back to management access: %v", runtime.refs)
	}
}

func TestHistoricalJobUsesOriginalProviderAndCredential(t *testing.T) {
	runtime := &jobsCredentialRuntime{actionsFake: &actionsFake{}}
	server := &Server{actionsTestRuntime: runtime}
	pool := store.ActionsPool{
		ApplicationID: "application", Project: "project", Environment: "development", ApplicationName: "pool",
		Config: spec.Service{Actions: &spec.Actions{Repository: "new/repo", Credential: "new-management", JobsCredential: "new-jobs"}},
	}
	job := store.ActionsJob{
		ProviderConfig: &spec.Actions{Repository: "old/repo", Credential: "old-management", JobsCredential: "old-jobs"},
		Observation:    actions.Observation{Repository: "old/repo", RunID: 12, Attempt: 1, JobKey: "build"},
	}
	original, ok := workflowJobPool(pool, job)
	if !ok || original.Config.Actions.Target().Repository != "old/repo" {
		t.Fatal("historical provider target was replaced by current settings")
	}
	if _, err := server.workflowClient(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runtime.refs, []string{"old-jobs"}) {
		t.Fatalf("historical request used current credentials: %v", runtime.refs)
	}
	if pool.Config.Actions.Repository != "new/repo" || runtime.targets[0].ApplicationID != "application" {
		t.Fatal("historical access changed the pool or application scope")
	}
	for _, config := range []*spec.Actions{
		nil,
		{Repository: "wrong/repo", Credential: "old-jobs"},
		{Repository: "old/repo"},
		{Provider: actions.ProviderGitLab, Repository: "old/repo", Credential: "old-jobs"},
	} {
		job.ProviderConfig = config
		if _, ok := workflowJobPool(pool, job); ok {
			t.Fatal("unbound historical job inherited current provider access")
		}
	}
}

func TestWorkflowLogMaskSeedsUseOriginalScopedCredentials(t *testing.T) {
	for _, missing := range []string{"", "old-read", "old-management", "old-cache"} {
		runtime := &jobsCredentialRuntime{actionsFake: &actionsFake{}, unavailable: missing}
		server := &Server{actionsTestRuntime: runtime}
		pool := store.ActionsPool{ApplicationID: "application", Project: "project", Environment: "development", ApplicationName: "pool", Service: "runner", Config: spec.Service{Actions: &spec.Actions{Credential: "new-management"}}}
		original := &spec.Actions{Credential: "old-management", JobsCredential: "old-read", Cache: &spec.ActionsCache{Credential: "old-cache"}}
		seeds, err := server.actionsLogCredentials(context.Background(), pool, original)
		if missing == "old-read" {
			if err == nil || len(seeds) != 0 || !reflect.DeepEqual(runtime.refs, []string{"old-read"}) {
				t.Fatal("missing read credential did not fail closed")
			}
			continue
		}
		want := 3
		if missing != "" {
			want--
		}
		if err != nil || len(seeds) != want || !reflect.DeepEqual(runtime.refs, []string{"old-read", "old-management", "old-cache"}) {
			t.Fatal("scoped available credentials were not collected", err)
		}
		for _, target := range runtime.targets {
			if target.ApplicationID != pool.ApplicationID || target.Project != pool.Project || target.Environment != pool.Environment || target.Spec.Name != pool.ApplicationName || target.Spec.Services[pool.Service].Actions != original {
				t.Fatal("log redaction used a different scope or revision")
			}
		}
	}
}
