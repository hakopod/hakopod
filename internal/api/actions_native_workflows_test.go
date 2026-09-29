package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func (f *gitlabLifecycleFake) DiscoverJobCandidates(_ context.Context, target actions.ProviderTarget, runnerID, runnerName string) ([]actions.ProviderJobIdentity, error) {
	f.providerOps++
	if !reflect.DeepEqual(target, f.targets[runnerID]) {
		return nil, &actions.GitLabError{Kind: "scope"}
	}
	job, ok := f.jobs[runnerID]
	if !ok {
		return nil, nil
	}
	if job.Identity.RunnerName != runnerName {
		return nil, &actions.GitLabError{Kind: "identity"}
	}
	if f.reuseJobs {
		other := job
		other.Identity.JobID += "-second"
		return []actions.ProviderJobIdentity{job.Identity, other.Identity}, nil
	}
	return []actions.ProviderJobIdentity{job.Identity}, nil
}

func (f *gitlabLifecycleFake) AssignedJob(ctx context.Context, target actions.ProviderTarget, identity actions.ProviderJobIdentity) (*actions.ProviderJob, error) {
	if _, ok := f.runners[identity.RunnerID]; !ok {
		return nil, &actions.GitLabError{Kind: "identity"}
	}
	return f.HistoricalJob(ctx, target, identity)
}

func (f *gitlabLifecycleFake) HistoricalJob(_ context.Context, target actions.ProviderTarget, identity actions.ProviderJobIdentity) (*actions.ProviderJob, error) {
	f.providerOps++
	job, ok := f.jobs[identity.RunnerID]
	if f.reuseJobs && identity.JobID == job.Identity.JobID+"-second" {
		job.Identity.JobID = identity.JobID
	}
	if !ok || !reflect.DeepEqual(target, f.targets[identity.RunnerID]) || job.Identity != identity {
		return nil, &actions.GitLabError{Kind: "identity"}
	}
	return &job, nil
}

func (f *gitlabLifecycleFake) JobLogs(ctx context.Context, target actions.ProviderTarget, identity actions.ProviderJobIdentity) ([]actions.LogLine, bool, error) {
	if _, err := f.AssignedJob(ctx, target, identity); err != nil {
		return nil, false, err
	}
	return f.nativeTrace(), false, nil
}

func (f *gitlabLifecycleFake) HistoricalJobLogs(ctx context.Context, target actions.ProviderTarget, identity actions.ProviderJobIdentity) ([]actions.LogLine, bool, error) {
	if _, err := f.HistoricalJob(ctx, target, identity); err != nil {
		return nil, false, err
	}
	return f.nativeTrace(), false, nil
}

func (f *gitlabLifecycleFake) nativeTrace() []actions.LogLine {
	f.traceCalls++
	if f.afterTrace != nil {
		f.afterTrace()
	}
	return []actions.LogLine{{Number: 1, Text: "development provider job trace"}}
}

func (f *gitlabLifecycleFake) CancelAuthorized(ctx context.Context, target actions.ProviderTarget, identity actions.ProviderJobIdentity, beforeWrite func(context.Context) error) (actions.CancellationScope, error) {
	if _, err := f.AssignedJob(ctx, target, identity); err != nil {
		return actions.CancellationNone, err
	}
	if f.beforeCancel != nil {
		f.beforeCancel()
	}
	if err := beforeWrite(ctx); err != nil {
		return actions.CancellationNone, err
	}
	f.cancelCalls++
	return actions.CancellationJob, nil
}

func (f *gitlabLifecycleFake) ActionsWorkflowOutput(context.Context, cluster.Target, string, string) (actions.Output, error) {
	f.managerLogReads++
	return actions.Output{Available: true, Lines: []actions.LogLine{{Number: 1, Text: gitlabLifecycleToken}}}, nil
}

func nativeWorkflowFixture(t *testing.T) (*Server, *gitlabLifecycleFake, store.ActionsPool, store.ActionsSlot, store.Principal, string) {
	t.Helper()
	s, f, pool := gitlabLifecycleHarness(t, actionsDatabase(t))
	ctx := context.Background()
	pool.Config.Actions.JobsCredential = "original-jobs"
	app := store.Application{ID: pool.ApplicationID, Project: pool.Project, Environment: pool.Environment, Name: pool.ApplicationName}
	if err := s.Store.SyncActions(ctx, app, spec.Application{Name: app.Name, Services: map[string]spec.Service{pool.Service: pool.Config}}, pool.Revision); err != nil {
		t.Fatal(err)
	}
	key, err := s.Store.Bootstrap(ctx, "native-workflow-fixture")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.Store.Authenticate(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	gitlabReconcile(t, s, pool)
	return s, f, pool, f.slot(f.starts[0]), owner, key
}

func nativeRequest(handler http.Handler, method, path, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func nativeHistory(t *testing.T, s *Server, pool store.ActionsPool, slot store.ActionsSlot) store.ActionsNativeHistory {
	t.Helper()
	h, err := s.Store.ActionsNativeHistory(context.Background(), pool.ApplicationID, pool.Service, slot.ID)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func nativeRefresh(t *testing.T, s *Server, pool store.ActionsPool, slot store.ActionsSlot) store.ActionsJob {
	t.Helper()
	h := nativeHistory(t, s, pool, slot)
	job, err := s.refreshNativeActionsJob(context.Background(), pool, h.Job)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

type nativeScopedCredentialRuntime struct {
	*gitlabLifecycleFake
	pool     store.ActionsPool
	original *spec.Actions
}

func (f *nativeScopedCredentialRuntime) ActionsCredential(ctx context.Context, target cluster.Target, reference string) (string, error) {
	f.t.Helper()
	config, ok := target.Spec.Services[f.pool.Service]
	if target.ApplicationID != f.pool.ApplicationID || target.Project != f.pool.Project || target.Environment != f.pool.Environment || target.Spec.Name != f.pool.ApplicationName || len(target.Spec.Services) != 1 || !ok {
		f.t.Fatal("historical credential lookup escaped the owning application and service")
	}
	if !reflect.DeepEqual(config.Actions, f.original) {
		f.t.Fatal("historical credential lookup substituted the current pool for the saved slot configuration")
	}
	return f.gitlabLifecycleFake.ActionsCredential(ctx, target, reference)
}

func TestNativeWorkflowDiscoveryHistoryAndOriginalCredentialBinding(t *testing.T) {
	s, f, pool, slot, _, key := nativeWorkflowFixture(t)
	s.actionsTestRuntime = &nativeScopedCredentialRuntime{gitlabLifecycleFake: f, pool: pool, original: slot.Config.Actions}
	if h := nativeHistory(t, s, pool, slot); h.Job.NativeJob != nil || h.Job.DiscoveryState != "pending" {
		t.Fatal("launch placeholder invented an assigned job")
	}
	f.credentials = nil
	// Provider target and credentials are changed after launch. History must
	// continue reading the original target, including after runner deletion.
	changed := *pool.Config.Actions
	changed.Credential, changed.JobsCredential = "new-management", "new-jobs"
	changed.GitLab = &actions.GitLabTarget{URL: "https://gitlab.com", ProjectID: 99}
	pool.Config.Actions = &changed
	if err := s.Store.SyncActions(context.Background(), store.Application{ID: pool.ApplicationID, Project: pool.Project, Environment: pool.Environment, Name: pool.ApplicationName}, spec.Application{Name: pool.ApplicationName, Services: map[string]spec.Service{pool.Service: pool.Config}}, pool.Revision+1); err != nil {
		t.Fatal(err)
	}
	job := nativeRefresh(t, s, pool, slot)
	if job.NativeJob == nil || job.NativeJob.Identity != f.jobs[slot.ProviderRunnerID].Identity || !reflect.DeepEqual(f.credentials, []string{"original-jobs", "original-management"}) {
		t.Fatal("history did not discover the provider job using its original read credential")
	}
	gitlabCleaned(t, s, f, pool)
	if h := nativeHistory(t, s, pool, slot); h.Job.NativeJob == nil || !h.Job.ProviderRemoved || h.Job.CanCancel {
		t.Fatal("runner cleanup erased history")
	}
	f.credentials = nil
	handler := s.Handler()
	base := "/api/v1/applications/" + pool.ApplicationID + "/actions/runner/jobs"
	response := nativeRequest(handler, "GET", base, key)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"provider":"gitlab"`) || !strings.Contains(response.Body.String(), `"provider_runner_id":"1"`) || strings.Contains(response.Body.String(), "original-jobs") {
		t.Fatalf("history response = %d %s", response.Code, response.Body.String())
	}
	response = nativeRequest(handler, "GET", base+"/"+slot.ID+"/logs", key)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "development provider job trace") || !strings.Contains(response.Body.String(), `"source":"gitlab"`) || f.managerLogReads != 0 || f.traceCalls != 1 {
		t.Fatalf("native logs did not preserve provider-only source and original scope: %d %s", response.Code, response.Body.String())
	}
	// Redaction resolves the original read and optional management credentials;
	// the provider client separately resolves only the original read credential.
	if want := []string{"original-jobs", "original-management", "original-jobs"}; !reflect.DeepEqual(f.credentials, want) {
		t.Fatalf("historical log credential lookups = %v, want %v", f.credentials, want)
	}
}

func TestNativeWorkflowUnavailableReadCredentialNeverFallsBackOrBlocksCleanup(t *testing.T) {
	s, f, pool, slot, _, key := nativeWorkflowFixture(t)
	f.readFailure = true
	f.credentials = nil
	h := nativeHistory(t, s, pool, slot)
	if _, err := s.refreshNativeActionsJob(context.Background(), pool, h.Job); err == nil || !reflect.DeepEqual(f.credentials, []string{"original-jobs"}) {
		t.Fatal("read failure fell back to management access")
	}
	gitlabCleaned(t, s, f, pool)
	h = nativeHistory(t, s, pool, slot)
	if !h.Finished || h.Attempts != 3 || h.Job.DiscoveryState != "unavailable" || h.Job.NativeJob != nil {
		t.Fatalf("failed discovery did not finish with unavailable metadata: %+v", h)
	}
	response := nativeRequest(s.Handler(), "GET", "/api/v1/applications/"+pool.ApplicationID+"/actions/runner/jobs/"+slot.ID+"/logs", key)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"state":"unavailable"`) || f.managerLogReads != 0 || f.traceCalls != 0 {
		t.Fatal("unavailable job exposed manager diagnostics", response.Code)
	}
}

func TestNativeHistoricalJobRequiresDurableCleanupAndRefreshesCompletion(t *testing.T) {
	s, f, pool, slot, _, key := nativeWorkflowFixture(t)
	job := f.jobs[slot.ProviderRunnerID]
	job.Status, job.Conclusion = "in_progress", ""
	f.jobs[slot.ProviderRunnerID] = job
	observed := nativeRefresh(t, s, pool, slot)
	if !observed.CanCancel {
		t.Fatal("verified running job has no cancellation eligibility")
	}
	// Model GitLab's deleted runner association. Its known job stays queryable,
	// but this alone must not authorize the relaxed historical read path.
	delete(f.runners, slot.ProviderRunnerID)
	handler := s.Handler()
	base := "/api/v1/applications/" + pool.ApplicationID + "/actions/runner/jobs/" + slot.ID
	response := nativeRequest(handler, "GET", base+"/logs", key)
	if response.Code != 200 || strings.Contains(response.Body.String(), "development provider job trace") || f.traceCalls != 0 {
		t.Fatal("missing runner without cleanup proof exposed historical logs")
	}
	// Handler switches deployment mode; this fixture retains its managed access
	// callback and explicitly restores the fixture flag for reconciliation.
	s.Store.ManagedCloud = true
	gitlabCleaned(t, s, f, pool)
	h := nativeHistory(t, s, pool, slot)
	if !h.Job.ProviderRemoved || h.Job.CanCancel || h.Job.NativeJob.Status != "in_progress" {
		h.Job.ProviderConfig = nil
		t.Fatalf("cleanup proof did not preserve the last observed status: %+v", h.Job)
	}
	job.Status, job.Conclusion = "completed", "success"
	f.jobs[slot.ProviderRunnerID] = job
	refreshed := nativeRefresh(t, s, pool, slot)
	if refreshed.NativeJob.Status != "completed" || refreshed.CanCancel {
		t.Fatal("historical completion could not recover after runner removal")
	}
	response = nativeRequest(handler, "GET", base+"/logs", key)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "development provider job trace") || f.traceCalls != 1 {
		t.Fatalf("proven historical logs = %d %s", response.Code, response.Body.String())
	}
	response = nativeRequest(handler, "POST", base+"/cancel", key)
	if response.Code != 409 || f.cancelCalls != 0 {
		t.Fatal("historical binding relaxed cancellation")
	}
}

func TestNativeWorkflowLogAndCancellationAuthorization(t *testing.T) {
	s, f, pool, slot, owner, _ := nativeWorkflowFixture(t)
	s.actionsTestRuntime = &nativeScopedCredentialRuntime{gitlabLifecycleFake: f, pool: pool, original: slot.Config.Actions}
	nativeRefresh(t, s, pool, slot)
	ctx := context.Background()
	changed := *pool.Config.Actions
	changed.Credential, changed.JobsCredential = "replacement-management", "replacement-jobs"
	changed.GitLab = &actions.GitLabTarget{URL: "https://gitlab.com", ProjectID: 99}
	pool.Config.Actions = &changed
	if err := s.Store.SyncActions(ctx, store.Application{ID: pool.ApplicationID, Project: pool.Project, Environment: pool.Environment, Name: pool.ApplicationName}, spec.Application{Name: pool.ApplicationName, Services: map[string]spec.Service{pool.Service: pool.Config}}, pool.Revision+1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Pool.Exec(ctx, `INSERT INTO environments(project,name) VALUES($1,'production')`, pool.Project); err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	base := "/api/v1/applications/" + pool.ApplicationID + "/actions/runner/jobs/" + slot.ID
	for _, test := range []struct {
		name, environment, application, permission string
		logs, cancel                               int
	}{
		{"metadata", pool.Environment, pool.ApplicationName, "deployments:read", 403, 403},
		{"logs", pool.Environment, pool.ApplicationName, "logs:read", 200, 403},
		{"cancel", pool.Environment, pool.ApplicationName, "deployments:write", 403, 202},
		{"wrong-env", "production", pool.ApplicationName, "logs:read", 403, 403},
		{"wrong-app", pool.Environment, "another-application", "logs:read", 403, 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, key, err := s.Store.CreateKey(ctx, owner, store.KeyInput{Name: test.name, Project: pool.Project, Environment: test.environment, Application: test.application, Permissions: []string{test.permission}, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			f.credentials = nil
			response := nativeRequest(handler, "GET", base+"/logs", key)
			if response.Code != test.logs || strings.Contains(response.Body.String(), gitlabLifecycleToken) {
				t.Fatalf("logs authorization = %d %s", response.Code, response.Body.String())
			}
			if test.logs == 200 {
				if want := []string{"original-jobs", "original-management", "original-jobs"}; !reflect.DeepEqual(f.credentials, want) {
					t.Fatalf("authorized log credential lookups = %v, want %v", f.credentials, want)
				}
			} else if len(f.credentials) != 0 {
				t.Fatal("denied log request resolved a provider credential", f.credentials)
			}
			f.credentials = nil
			f.readFailure = true // Cancellation must use management access even if the read credential is revoked.
			response = nativeRequest(handler, "POST", base+"/cancel", key)
			f.readFailure = false
			if response.Code != test.cancel {
				t.Fatalf("cancellation authorization = %d %s", response.Code, response.Body.String())
			}
			if test.cancel == 202 && !reflect.DeepEqual(f.credentials, []string{"original-management"}) {
				t.Fatal("cancellation used the optional read credential", f.credentials)
			}
			if test.cancel != 202 && len(f.credentials) != 0 {
				t.Fatal("denied cancellation resolved a provider credential", f.credentials)
			}
		})
	}
	if f.managerLogReads != 0 || f.traceCalls != 1 || f.cancelCalls != 1 {
		t.Fatal("denied requests reached a provider operation")
	}
}

func TestNativeWorkflowPermissionRevocationBeforeLogResponseAndCancellation(t *testing.T) {
	s, f, pool, slot, owner, _ := nativeWorkflowFixture(t)
	nativeRefresh(t, s, pool, slot)
	ctx := context.Background()
	handler := s.Handler()
	base := "/api/v1/applications/" + pool.ApplicationID + "/actions/runner/jobs/" + slot.ID
	for _, operation := range []string{"logs", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			permission, method := "logs:read", "GET"
			if operation == "cancel" {
				permission, method = "deployments:write", "POST"
			}
			metadata, key, err := s.Store.CreateKey(ctx, owner, store.KeyInput{Name: "revoked-" + operation, Project: pool.Project, Environment: pool.Environment, Application: pool.ApplicationName, Permissions: []string{permission}, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			revoke := func() {
				if _, err := s.Store.Pool.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1`, metadata.ID); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "logs" {
				f.afterTrace = revoke
			} else {
				f.beforeCancel = revoke
			}
			response := nativeRequest(handler, method, base+"/"+operation, key)
			if response.Code != 403 || strings.Contains(response.Body.String(), "development provider job trace") || f.cancelCalls != 0 {
				t.Fatalf("revoked permission used: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestNativeWorkflowReuseHoldsPoolAcrossHistoryExpiry(t *testing.T) {
	s, f, pool, slot, _, _ := nativeWorkflowFixture(t)
	f.reuseJobs = true
	pool.Config.Suspended = true
	gitlabCleaned(t, s, f, pool)
	h := nativeHistory(t, s, pool, slot)
	if h.Job.DiscoveryState != "reuse_detected" || !h.Finished {
		t.Fatal("reuse was not recorded")
	}
	var evidence []byte
	if err := s.Store.Pool.QueryRow(context.Background(), `SELECT provider_reuse_evidence FROM actions_jobs WHERE slot_id=$1`, slot.ID).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	var jobs []actions.ProviderJob
	if json.Unmarshal(evidence, &jobs) != nil || len(jobs) != 2 || jobs[0].Identity == jobs[1].Identity {
		t.Fatal("reuse hold lost the two verified job identities")
	}
	if _, err := s.Store.Pool.Exec(context.Background(), `DELETE FROM actions_jobs WHERE slot_id=$1`, slot.ID); err != nil {
		t.Fatal(err)
	}
	pool.Config.Suspended = false
	gitlabReconcile(t, s, pool)
	if len(f.starts) != 1 || len(f.runners) != 0 {
		t.Fatal("history expiry silently replenished a held pool")
	}
	held, err := s.Store.ActionsProviderPoolHeld(context.Background(), pool.ApplicationID, pool.Service)
	if err != nil || !held {
		t.Fatal("reuse hold was not durable", err)
	}
}
