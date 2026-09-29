package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"net/http"
	"sort"
	"time"
)

type actionsOutputRuntime interface {
	ActionsWorkflowOutput(context.Context, cluster.Target, string, string) (actions.Output, error)
}

func (s *Server) observeActionsJob(ctx context.Context, t cluster.Target, v store.ActionsSlot) {
	if actionsConfigProvider(v.Config) != actions.ProviderGitHub {
		return
	}
	runtime, ok := s.actionRuntime().(actionsOutputRuntime)
	if !ok || v.RunnerID == 0 {
		return
	}
	step, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	output, err := runtime.ActionsWorkflowOutput(step, t, v.Service, v.ID)
	if err == nil && output.Job != nil {
		_ = s.Store.RecordActionsJob(step, v, *output.Job)
	}
}
func (s *Server) authorizedActionsPool(w http.ResponseWriter, r *http.Request, permission string) (store.ActionsPool, bool) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), permission)
	if !ok {
		return store.ActionsPool{}, false
	}
	pool, err := s.Store.ActionsPool(r.Context(), a.ID, r.PathValue("service"))
	if err != nil {
		authFailure(w, err)
		return store.ActionsPool{}, false
	}
	if pool != nil {
		return *pool, true
	}
	problem(w, 404, "not_found", "This runner pool is no longer available.")
	return store.ActionsPool{}, false
}
func workflowState(err error) (string, string) {
	var reuse *actions.RunnerReuseError
	if errors.As(err, &reuse) {
		return "unavailable", reuse.Error()
	}
	var native *actions.GitLabError
	if errors.As(err, &native) {
		if native.Status == 401 || native.Status == 403 {
			return "permission_denied", "GitLab could not authorize job access. Check the selected read credential and project permissions."
		}
		if !native.RetryAt.IsZero() || native.Status == 429 {
			return "rate_limited", native.Error()
		}
		if native.Status == 404 || native.Status == 410 {
			return "unavailable", "GitLab has not made this job available, or its original read credential cannot access it."
		}
		return "disconnected", native.Error()
	}
	var unsupported *actions.UnsupportedProviderError
	if errors.As(err, &unsupported) {
		return "unavailable", "Job access is not configured for this provider."
	}
	var status *actions.StatusError
	if errors.As(err, &status) && (status.Status == 401 || status.Status == 403) {
		return "permission_denied", "GitHub could not authorize this request. Check Actions read permission and repository access; a provider cooldown may also apply."
	}
	var retry *actions.RetryError
	if errors.As(err, &retry) {
		return "rate_limited", retry.Error() + ". Try again after this time."
	}
	if errors.As(err, &status) {
		switch status.Status {
		case 401, 403:
			return "permission_denied", "GitHub could not authorize this request. Check Actions read permission and the credential's repository access; GitHub may also be limiting requests."
		case 404, 410:
			return "unavailable", "GitHub has not published this job yet, its logs expired, or the credential cannot access its repository."
		case 429:
			return "rate_limited", "GitHub is limiting requests. Try again later."
		}
	}
	return "disconnected", "GitHub is temporarily unavailable. The last observation is shown."
}
func (s *Server) workflowClient(ctx context.Context, p store.ActionsPool) (*actions.Client, error) {
	if actionsConfigProvider(p.Config) != actions.ProviderGitHub {
		return nil, unsupportedActionsProvider(actionsConfigProvider(p.Config))
	}
	return s.actionsProvider(ctx, actionsTarget(p), p.Config.Actions.EffectiveJobsCredential())
}

// History follows the slot's original target and credential reference even after
// the pool is edited. Never infer missing historical scope from current settings.
func workflowJobPool(p store.ActionsPool, job store.ActionsJob) (store.ActionsPool, bool) {
	config := job.ProviderConfig
	if config == nil || config.Provider.Effective() != actions.ProviderGitHub || config.EffectiveJobsCredential() == "" || !job.Observation.Valid(config.Target()) {
		return store.ActionsPool{}, false
	}
	p.Config.Actions = config
	return p, true
}
func (s *Server) actionsJobs(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authorizedActionsPool(w, r, "deployments:read")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	jobs, err := s.Store.ActionsJobs(ctx, p.ApplicationID, p.Service)
	if err != nil {
		authFailure(w, err)
		return
	}
	state, message := "ready", ""
	checked := 0
	order := make([]int, len(jobs))
	for i := range jobs {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return jobs[order[i]].CheckedAt.Before(jobs[order[j]].CheckedAt) })
	for _, i := range order {
		item := &jobs[i]
		if item.Provider == actions.ProviderGitLab {
			if item.DiscoveryState == "reuse_detected" {
				state, message = "unavailable", (&actions.RunnerReuseError{}).Error()
				continue
			}
			if item.DiscoveryState == "unavailable" && item.NativeJob == nil {
				state, message = "unavailable", "Some job assignments could not be verified before runner cleanup."
				continue
			}
			if (item.NativeJob != nil && item.NativeJob.Status == "completed") || time.Since(item.CheckedAt) < 15*time.Second {
				continue
			}
			if checked >= 5 {
				break
			}
			claimed, e := s.Store.ClaimActionsJobRefresh(ctx, p.ApplicationID, p.Service, item.SlotID)
			if e != nil {
				authFailure(w, e)
				return
			}
			if !claimed {
				continue
			}
			checked++
			refreshed, e := s.refreshNativeActionsJob(ctx, p, *item)
			if e != nil {
				state, message = gitlabWorkflowState(e)
				break
			}
			*item = refreshed
			continue
		}
		jobPool, bound := workflowJobPool(p, *item)
		if !bound {
			continue
		}
		if item.Job != nil && (item.Job.Status == "completed" || time.Since(item.CheckedAt) < 15*time.Second) {
			continue
		}
		if checked >= 5 {
			break
		}
		claimed, e := s.Store.ClaimActionsJobRefresh(ctx, p.ApplicationID, p.Service, item.SlotID)
		if e != nil {
			authFailure(w, e)
			return
		}
		if !claimed {
			continue
		}
		checked++
		client, err := s.workflowClient(ctx, jobPool)
		if err != nil {
			state, message = workflowState(err)
			break
		}
		var job *actions.Job
		if item.Job != nil && item.Job.ID > 0 {
			job, e = client.AssignedJobID(ctx, item.Observation, item.RunnerID, "hakopod-"+item.SlotID, item.Job.ID)
		} else {
			job, e = client.AssignedJob(ctx, item.Observation, item.RunnerID, "hakopod-"+item.SlotID)
		}
		if e != nil {
			state, message = workflowState(e)
			break
		}
		if job != nil {
			if e = s.Store.UpdateActionsJob(ctx, p.ApplicationID, p.Service, item.SlotID, *job); e != nil {
				authFailure(w, e)
				return
			}
			item.Job = job
			item.UpdatedAt = time.Now().UTC()
		}
	}
	// Fit the shared Cloud proxy budget even for workflows with many steps.
	bounded := jobs[:0]
	size := 512
	truncated := false
	for _, item := range jobs {
		encoded, _ := json.Marshal(item)
		if size+len(encoded) > 1536<<10 {
			truncated = true
			break
		}
		size += len(encoded)
		bounded = append(bounded, item)
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, 200, map[string]any{"items": bounded, "state": state, "message": message, "observed_at": time.Now().UTC(), "limit": 100, "truncated": truncated})
}
func (s *Server) actionsJobLogs(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authorizedActionsPool(w, r, "logs:read")
	if !ok {
		return
	}
	if !s.streamSlot(w) {
		return
	}
	defer func() { <-s.streams }()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	jobs, err := s.Store.ActionsJobs(ctx, p.ApplicationID, p.Service)
	if err != nil {
		authFailure(w, err)
		return
	}
	var selected *store.ActionsJob
	for i := range jobs {
		if jobs[i].SlotID == r.PathValue("slot") {
			selected = &jobs[i]
			break
		}
	}
	if selected == nil {
		problem(w, 404, "not_found", "This job is no longer available in this pool.")
		return
	}
	// The original read credential remains server-only. Failed secret lookup
	// cannot fall back to returning an unredacted provider or runner response.
	if selected.ProviderConfig == nil {
		problem(w, 409, "unavailable", "This job's original log configuration is unavailable.")
		return
	}
	logCredentials, err := s.actionsLogCredentials(ctx, p, selected.ProviderConfig)
	if err != nil {
		problem(w, 503, "logs_unavailable", "Log redaction credentials are unavailable. Retry after secret storage recovers.")
		return
	}
	respond := func(lines []actions.LogLine, truncated bool, source, state, message string) {
		principal, accessErr := s.Store.KeyPrincipal(ctx, who(r).KeyID)
		if accessErr != nil || !principal.Allows("logs:read", p.Project, p.Environment, p.ApplicationName) {
			problem(w, 403, "forbidden", "Log access was revoked.")
			return
		}
		if lines == nil {
			lines = []actions.LogLine{}
		}
		lines = actions.MaskLogLines(lines, logCredentials...)
		var bounded bool
		lines, bounded = actions.BoundLogLines(lines, source == "runner")
		truncated = truncated || bounded
		w.Header().Set("Cache-Control", "no-store")
		write(w, 200, map[string]any{"lines": lines, "truncated": truncated, "source": source, "state": state, "message": message, "observed_at": time.Now().UTC()})
	}
	if selected.Provider == actions.ProviderGitLab {
		s.nativeActionsJobLogs(ctx, p, *selected, respond)
		return
	}
	jobPool, bound := workflowJobPool(p, *selected)
	if !bound {
		problem(w, 409, "unavailable", "This job's original provider settings are unavailable. Its saved metadata remains visible.")
		return
	}
	// Live output is scoped to a recorded runner pod, never a caller-supplied pod
	// name or a different job on the same GitHub repository.
	slots, err := s.Store.ActionsSlots(ctx, p.ApplicationID, p.Service)
	if err != nil {
		authFailure(w, err)
		return
	}
	if runtime, ok := s.actionRuntime().(actionsOutputRuntime); ok {
		for _, slot := range slots {
			if slot.ID != selected.SlotID {
				continue
			}
			output, e := runtime.ActionsWorkflowOutput(ctx, actionsTarget(jobPool), p.Service, slot.ID)
			if e == nil && output.Available && (selected.Job == nil || selected.Job.Status != "completed") {
				respond(output.Lines, output.Truncated, "runner", "live", "Live output can lag briefly while the runner flushes its log.")
				return
			}
		}
	}
	client, err := s.workflowClient(ctx, jobPool)
	if err != nil {
		state, message := workflowState(err)
		respond(nil, false, "github", state, message)
		return
	}
	// Always revalidate with GitHub before requesting a completed log body.
	known := selected.Job != nil && selected.Job.ID > 0
	release, err := client.PrepareWorkflowLogs(ctx, known)
	if err != nil {
		state, message := workflowState(err)
		respond(nil, false, "github", state, message)
		return
	}
	defer release()
	var job *actions.Job
	if known {
		job, err = client.AssignedJobID(ctx, selected.Observation, selected.RunnerID, "hakopod-"+selected.SlotID, selected.Job.ID)
	} else {
		job, err = client.AssignedJob(ctx, selected.Observation, selected.RunnerID, "hakopod-"+selected.SlotID)
	}
	if err != nil {
		state, message := workflowState(err)
		respond(nil, false, "github", state, message)
		return
	}
	if job == nil || job.Status != "completed" {
		respond(nil, false, "github", "waiting", "Waiting for GitHub to publish completed logs. Live output requires a runner using the current Hakopod image.")
		return
	}
	lines, truncated, err := client.JobLogs(ctx, selected.Observation.Repository, job.ID)
	if err != nil {
		state, message := workflowState(err)
		respond(nil, false, "github", state, message)
		return
	}
	respond(lines, truncated, "github", "complete", "")
}
