package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

type gitlabWorkflowProvider interface {
	actions.ProviderJobClient
}

func gitlabWorkflowState(err error) (string, string) {
	var native *actions.GitLabError
	var reuse *actions.RunnerReuseError
	var unsupported *actions.UnsupportedProviderError
	if errors.As(err, &native) || errors.As(err, &reuse) || errors.As(err, &unsupported) {
		return workflowState(err)
	}
	return "unavailable", "GitLab job access is temporarily unavailable. The last verified metadata remains visible."
}

func (s *Server) gitlabWorkflowClient(ctx context.Context, p store.ActionsPool, job store.ActionsJob) (gitlabWorkflowProvider, error) {
	if actionsConfigProvider(p.Config) != actions.ProviderGitLab || (s.actionsGitLabJobsClient == nil && !s.actionsNativeConfigured) {
		return nil, unsupportedActionsProvider(actions.ProviderGitLab)
	}
	if _, err := p.Config.Actions.ProviderTarget().Canonical(); err != nil {
		return nil, &actions.GitLabError{Kind: "scope"}
	}
	reference := p.Config.Actions.EffectiveJobsCredential()
	if reference == "" {
		return nil, unsupportedActionsProvider(actions.ProviderGitLab)
	}
	target := actionsTarget(p)
	token, err := s.actionRuntime().ActionsCredential(ctx, target, reference)
	if err != nil {
		return nil, err
	}
	var client gitlabWorkflowProvider
	if s.actionsGitLabJobsClient != nil {
		client, err = s.actionsGitLabJobsClient(ctx, target, p.Config, token)
	} else {
		if len(job.EncryptedProviderIntent) == 0 {
			history, e := s.Store.ActionsNativeHistory(ctx, p.ApplicationID, p.Service, job.SlotID)
			if e != nil {
				return nil, e
			}
			job = history.Job
		}
		original, e := s.gitlabHistoryRuntime(p, job)
		if e != nil {
			return nil, e
		}
		client, err = s.installationGitLabClient(ctx, target, p.Service, p.Config, token, original)
	}
	if err == nil && client == nil {
		return nil, unsupportedActionsProvider(actions.ProviderGitLab)
	}
	return client, err
}

func nativeWorkflowJobPool(p store.ActionsPool, job store.ActionsJob) (store.ActionsPool, bool) {
	config := job.ProviderConfig
	if config == nil || config.Provider.Effective() != actions.ProviderGitLab || config.EffectiveJobsCredential() == "" || !actions.ValidProviderID(job.ProviderRunnerID) {
		return store.ActionsPool{}, false
	}
	if _, err := config.ProviderTarget().Canonical(); err != nil {
		return store.ActionsPool{}, false
	}
	if native := job.NativeJob; native != nil && (native.Identity.RunnerID != job.ProviderRunnerID || native.Identity.RunnerName != "hakopod-"+job.SlotID || !actions.ValidProviderID(native.Identity.JobID) || !actions.ValidProviderID(native.Identity.RunID) || !actions.ValidProviderID(native.Identity.Repository)) {
		return store.ActionsPool{}, false
	}
	p.Config.Actions = config
	return p, true
}

func discoverGitLabJob(ctx context.Context, p store.ActionsPool, item store.ActionsJob, reader gitlabWorkflowProvider, manager gitlabRunnerProvider) (*actions.ProviderJob, error) {
	discovery, ok := manager.(actions.ProviderJobDiscoveryClient)
	if !ok {
		return nil, unsupportedActionsProvider(actions.ProviderGitLab)
	}
	identities, err := discovery.DiscoverJobCandidates(ctx, p.Config.Actions.ProviderTarget(), item.ProviderRunnerID, "hakopod-"+item.SlotID)
	if err != nil {
		return nil, err
	}
	if len(identities) > 2 {
		return nil, &actions.GitLabError{Kind: "bounds"}
	}
	jobs := make([]actions.ProviderJob, 0, len(identities))
	for _, identity := range identities {
		if identity.RunnerID != item.ProviderRunnerID || identity.RunnerName != "hakopod-"+item.SlotID {
			return nil, &actions.GitLabError{Kind: "identity"}
		}
		job, err := reader.AssignedJob(ctx, p.Config.Actions.ProviderTarget(), identity)
		if err != nil {
			return nil, err
		}
		if job == nil || job.Identity != identity || (len(jobs) > 0 && jobs[0].Identity == job.Identity) {
			return nil, &actions.GitLabError{Kind: "identity"}
		}
		jobs = append(jobs, *job)
	}
	if len(jobs) == 2 {
		return nil, &actions.RunnerReuseError{Jobs: jobs}
	}
	if len(jobs) == 1 {
		return &jobs[0], nil
	}
	return nil, nil
}

func nativeAssignedJob(ctx context.Context, client gitlabWorkflowProvider, p store.ActionsPool, item store.ActionsJob) (*actions.ProviderJob, error) {
	if item.ProviderRemoved {
		historical, ok := client.(actions.ProviderHistoricalJobClient)
		if !ok {
			return nil, unsupportedActionsProvider(actions.ProviderGitLab)
		}
		return historical.HistoricalJob(ctx, p.Config.Actions.ProviderTarget(), item.NativeJob.Identity)
	}
	return client.AssignedJob(ctx, p.Config.Actions.ProviderTarget(), item.NativeJob.Identity)
}

func (s *Server) refreshNativeActionsJob(ctx context.Context, p store.ActionsPool, item store.ActionsJob) (store.ActionsJob, error) {
	bound, ok := nativeWorkflowJobPool(p, item)
	if !ok {
		return item, unsupportedActionsProvider(actions.ProviderGitLab)
	}
	h, err := s.Store.ActionsNativeHistory(ctx, p.ApplicationID, p.Service, item.SlotID)
	if err != nil {
		return item, err
	}
	if h.Job.DiscoveryState == "reuse_detected" {
		return h.Job, &actions.RunnerReuseError{}
	}
	if h.Finished && h.Job.NativeJob == nil {
		return h.Job, nil
	}
	client, err := s.gitlabWorkflowClient(ctx, bound, h.Job)
	if err != nil {
		return item, err
	}
	var job *actions.ProviderJob
	if h.Job.NativeJob != nil {
		job, err = nativeAssignedJob(ctx, client, bound, h.Job)
	} else {
		original, e := s.gitlabHistoryRuntime(bound, h.Job)
		if e != nil {
			return item, e
		}
		manager, e := s.gitlabRunnerClient(ctx, actionsTarget(bound), bound.Service, bound.Config, original)
		if e != nil {
			return item, e
		}
		job, err = discoverGitLabJob(ctx, bound, h.Job, client, manager)
	}
	var reuse *actions.RunnerReuseError
	if errors.As(err, &reuse) {
		if e := s.Store.HoldActionsProviderReuse(ctx, p.ApplicationID, p.Service, h, reuse.Jobs); e != nil {
			return item, e
		}
		item.DiscoveryState = "reuse_detected"
		return item, err
	}
	if err != nil || job == nil {
		return h.Job, err
	}
	if err = s.Store.RecordActionsNativeJob(ctx, p.ApplicationID, p.Service, h, *job); err != nil {
		return item, err
	}
	h.Job.NativeJob, h.Job.DiscoveryState, h.Job.UpdatedAt = job, "observed", time.Now().UTC()
	h.Job.CanCancel = !h.Job.ProviderRemoved && job.Status != "completed"
	return h.Job, nil
}

func (s *Server) observeGitLabActionsJob(ctx context.Context, t cluster.Target, slot store.ActionsSlot) {
	if !slot.ManagerLaunchAttempted || slot.ProviderRunnerID == "" {
		return
	}
	h, err := s.Store.EnsureActionsNativeHistory(ctx, slot)
	if err != nil || h.Job.DiscoveryState == "reuse_detected" || (h.Job.NativeJob != nil && h.Job.NativeJob.Status == "completed") {
		return
	}
	claimed, err := s.Store.ClaimActionsJobRefresh(ctx, slot.ApplicationID, slot.Service, slot.ID)
	if err != nil || !claimed {
		return
	}
	step, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	p := store.ActionsPool{ApplicationID: t.ApplicationID, Project: t.Project, Environment: t.Environment, ApplicationName: t.Spec.Name, Service: slot.Service, Revision: t.Revision, Config: slot.Config}
	_, _ = s.refreshNativeActionsJob(step, p, h.Job)
}

// Pause first, then make at most three bounded discovery attempts in sixty
// seconds. Read-credential failure cannot indefinitely delay safe revocation.
func (s *Server) prepareGitLabJobCleanup(ctx context.Context, t cluster.Target, slot store.ActionsSlot, client gitlabRunnerProvider) (bool, error) {
	h, err := s.Store.EnsureActionsNativeHistory(ctx, slot)
	if err != nil {
		return false, err
	}
	if h.Finished {
		return true, nil
	}
	if !h.Paused {
		drainer, ok := client.(interface {
			DrainOwned(context.Context, actions.ProviderTarget, string, string) error
		})
		if !ok {
			return false, unsupportedActionsProvider(actions.ProviderGitLab)
		}
		if err = actionsFence(ctx, t); err != nil {
			return false, err
		}
		err = drainer.DrainOwned(ctx, slot.Config.Actions.ProviderTarget(), slot.ProviderRunnerID, "hakopod-"+slot.ID)
		if err != nil && !errors.Is(err, actions.ErrRunnerAbsent) {
			return false, err
		}
		if err = actionsFence(ctx, t); err != nil {
			return false, err
		}
		return false, s.Store.MarkActionsNativePaused(ctx, slot, h)
	}
	var job *actions.ProviderJob
	var reuse []actions.ProviderJob
	if h.StartedAt == nil || time.Since(*h.StartedAt) < time.Minute {
		step, cancel := context.WithTimeout(ctx, 8*time.Second)
		p := store.ActionsPool{ApplicationID: t.ApplicationID, Project: t.Project, Environment: t.Environment, ApplicationName: t.Spec.Name, Service: slot.Service, Revision: t.Revision, Config: slot.Config}
		reader, e := s.gitlabWorkflowClient(step, p, h.Job)
		if e == nil {
			job, e = discoverGitLabJob(step, p, h.Job, reader, client)
		}
		var violation *actions.RunnerReuseError
		if errors.As(e, &violation) {
			reuse = violation.Jobs
		}
		cancel()
		if job != nil && h.Job.NativeJob != nil && job.Identity != h.Job.NativeJob.Identity {
			reuse = []actions.ProviderJob{*h.Job.NativeJob, *job}
			job = nil
		}
	}
	if err = actionsFence(ctx, t); err != nil {
		return false, err
	}
	return false, s.Store.FinishActionsNativeDiscovery(ctx, slot, h, job, reuse)
}

type actionsLogsResponse func([]actions.LogLine, bool, string, string, string)

func (s *Server) nativeActionsJobLogs(ctx context.Context, p store.ActionsPool, selected store.ActionsJob, respond actionsLogsResponse) {
	bound, ok := nativeWorkflowJobPool(p, selected)
	if !ok {
		respond(nil, false, "gitlab", "unavailable", "This job's original provider settings are unavailable.")
		return
	}
	if selected.NativeJob == nil {
		state, message := "waiting", "Waiting for GitLab to identify the job assigned to this runner."
		if selected.DiscoveryState == "unavailable" || selected.DiscoveryState == "reuse_detected" {
			state, message = "unavailable", "The job assignment could not be verified. Manager diagnostics are not exposed as workflow logs."
		}
		respond(nil, false, "gitlab", state, message)
		return
	}
	client, err := s.gitlabWorkflowClient(ctx, bound, selected)
	if err != nil {
		state, message := gitlabWorkflowState(err)
		respond(nil, false, "gitlab", state, message)
		return
	}
	job, err := nativeAssignedJob(ctx, client, bound, selected)
	if err != nil || job == nil {
		state, message := gitlabWorkflowState(err)
		respond(nil, false, "gitlab", state, message)
		return
	}
	var lines []actions.LogLine
	var truncated bool
	if selected.ProviderRemoved {
		if historical, ok := client.(actions.ProviderHistoricalJobClient); ok {
			lines, truncated, err = historical.HistoricalJobLogs(ctx, bound.Config.Actions.ProviderTarget(), selected.NativeJob.Identity)
		} else {
			err = unsupportedActionsProvider(actions.ProviderGitLab)
		}
	} else {
		lines, truncated, err = client.JobLogs(ctx, bound.Config.Actions.ProviderTarget(), selected.NativeJob.Identity)
	}
	if err != nil {
		state, message := gitlabWorkflowState(err)
		respond(nil, false, "gitlab", state, message)
		return
	}
	state := "live"
	if job.Status == "completed" {
		state = "complete"
	}
	respond(lines, truncated, "gitlab", state, "")
}

func (s *Server) actionsJobCancel(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authorizedActionsPool(w, r, "deployments:write")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	h, err := s.Store.ActionsNativeHistory(ctx, p.ApplicationID, p.Service, r.PathValue("slot"))
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "not_found", "This verified native job is no longer available in this pool.")
		return
	}
	if err != nil {
		failure(w, err)
		return
	}
	bound, ok := nativeWorkflowJobPool(p, h.Job)
	if !ok || h.Job.NativeJob == nil || h.Job.ProviderRemoved {
		problem(w, 409, "unavailable", "This job has no verified active runner eligible for cancellation.")
		return
	}
	original, err := s.gitlabHistoryRuntime(bound, h.Job)
	if err != nil {
		problem(w, 503, "unavailable", safeActionsError(err))
		return
	}
	client, err := s.gitlabRunnerClient(ctx, actionsTarget(bound), bound.Service, bound.Config, original)
	if err != nil {
		problem(w, 503, "unavailable", safeActionsError(err))
		return
	}
	canceller, ok := client.(interface {
		CancelAuthorized(context.Context, actions.ProviderTarget, actions.ProviderJobIdentity, func(context.Context) error) (actions.CancellationScope, error)
	})
	if !ok {
		problem(w, 409, "unavailable", "Job cancellation is unavailable for this provider.")
		return
	}
	check := func(ctx context.Context) error {
		principal, e := s.Store.KeyPrincipal(ctx, who(r).KeyID)
		if e != nil || !principal.Allows("deployments:write", p.Project, p.Environment, p.ApplicationName) {
			return store.ErrForbidden
		}
		return nil
	}
	if err = check(ctx); err != nil {
		authFailure(w, err)
		return
	}
	scope, err := canceller.CancelAuthorized(ctx, bound.Config.Actions.ProviderTarget(), h.Job.NativeJob.Identity, check)
	if errors.Is(err, store.ErrForbidden) {
		authFailure(w, err)
		return
	}
	if err != nil || scope != actions.CancellationJob {
		problem(w, 503, "cancellation_unknown", "GitLab has not confirmed job cancellation. Refresh the job before retrying.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, 202, map[string]string{"status": "cancellation_requested", "scope": "job"})
}
