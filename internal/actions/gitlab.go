package actions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const gitlabMaxRunnerPages = 5

var gitlabOwnedName = regexp.MustCompile(`^hakopod-[a-f0-9]{32}$`)
var gitlabNamespacePath = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*$`)

type GitLabClient struct {
	target     ProviderTarget
	credential string
	http       *http.Client
	budget     *RequestBudget
	budgetKey  [32]byte
	timeout    int64
}

var _ ProviderClient = (*GitLabClient)(nil)
var _ ProviderJobClient = (*GitLabClient)(nil)
var _ ProviderCancellationClient = (*GitLabClient)(nil)
var _ ProviderDrainClient = (*GitLabClient)(nil)

func NewGitLabClient(target ProviderTarget, credential string, options GitLabClientOptions) (*GitLabClient, error) {
	target, err := target.Canonical()
	if err != nil || target.Provider != ProviderGitLab {
		return nil, errors.New("select a valid GitLab runner target")
	}
	if len(credential) < 16 || len(credential) > 4096 || strings.IndexFunc(credential, func(r rune) bool { return r <= ' ' || r >= 127 }) >= 0 {
		return nil, errors.New("supply a scoped GitLab credential")
	}
	if options.TimeoutMinutes == 0 {
		options.TimeoutMinutes = 60
	}
	if options.TimeoutMinutes < 5 || options.TimeoutMinutes > 360 {
		return nil, errors.New("GitLab runner timeout must be between 5 and 360 minutes")
	}
	policy, roots, err := gitlabPolicy(target, options.TrustPolicy)
	if err != nil {
		return nil, err
	}
	policy.freshDenied = options.FreshDeniedNetworks
	return &GitLabClient{target: target, credential: credential, http: newGitLabHTTP(policy, roots), budget: options.Budget, budgetKey: gitlabBudgetKey(target, credential), timeout: options.TimeoutMinutes * 60}, nil
}

func (c *GitLabClient) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Provider: ProviderGitLab, Lifecycle: "ephemeral", Available: false, Reason: "GitLab native runner execution and cleanup are not yet qualified",
		MinimumResources:  ProviderMinimumResources{CPURequest: "200m", CPULimit: "1", MemoryRequest: "2Gi", MemoryLimit: "4Gi", WorkspaceGiB: 2},
		Cache:             ProviderCacheCapabilities{Reason: "Persistent cache requires configured and qualified storage"},
		Build:             ProviderBuildCapabilities{NativeArchitectures: []string{}, Reason: "Native and cross-architecture builds are not yet qualified"},
		Isolation:         ProviderIsolationCapabilities{Reason: "Native manager credential and single-job isolation require runtime qualification"},
		CancellationScope: CancellationJob,
	}
}

func (c *GitLabClient) requireTarget(target ProviderTarget) error {
	canonical, err := target.Canonical()
	if err != nil || canonical.Provider != ProviderGitLab || *canonical.GitLab != *c.target.GitLab {
		return &GitLabError{Kind: "scope"}
	}
	return nil
}

func (c *GitLabClient) runnerScope() (string, string) {
	if c.target.GitLab.ProjectID > 0 {
		return "/projects/" + strconv.FormatInt(c.target.GitLab.ProjectID, 10), "project_type"
	}
	return "/groups/" + strconv.FormatInt(c.target.GitLab.GroupID, 10), "group_type"
}

func (c *GitLabClient) ownership(name string) string {
	scope, _ := c.runnerScope()
	digest := sha256.Sum256([]byte(c.target.GitLab.URL + "\x00" + scope + "\x00" + name))
	return "hakopod-managed-v1:" + hex.EncodeToString(digest[:])
}

// GitLabManagerConfig is the private, versioned registration envelope consumed
// by the native runtime. Store it encrypted; never return it through an API.
type GitLabManagerConfig struct {
	SchemaVersion    int                     `json:"schema_version"`
	URL              string                  `json:"url"`
	RunnerID         string                  `json:"runner_id"`
	Name             string                  `json:"name"`
	Token            string                  `json:"token"`
	TimeoutSeconds   int64                   `json:"timeout_seconds"`
	ExpiresAt        *time.Time              `json:"expires_at,omitempty"`
	CacheCredentials *GitLabCacheCredentials `json:"cache_credentials,omitempty"`
}

// GitLabCacheCredentials are private manager material. The native adapter
// signs bounded requests; neither these values nor a credentials file enters
// the job-visible daemon or the public transport policy.
type GitLabCacheCredentials struct {
	AccessKey    string `json:"access_key"`
	SecretKey    string `json:"secret_key"`
	SessionToken string `json:"session_token,omitempty"`
}

func (c GitLabCacheCredentials) Validate() error {
	for _, field := range []struct {
		value            string
		minimum, maximum int
	}{{c.AccessKey, 3, 256}, {c.SecretKey, 16, 4096}, {c.SessionToken, 0, 8192}} {
		if len(field.value) < field.minimum || len(field.value) > field.maximum || strings.IndexFunc(field.value, func(r rune) bool { return r <= ' ' || r >= 127 }) >= 0 {
			return errors.New("GitLab cache requires bounded S3 credentials in its selected application secret")
		}
	}
	return nil
}

func (c *GitLabClient) Register(ctx context.Context, target ProviderTarget, name string, labels []string) (ProviderRegistration, error) {
	if err := c.requireTarget(target); err != nil {
		return ProviderRegistration{}, err
	}
	if !gitlabOwnedName.MatchString(name) || !ValidLabels(labels) {
		return ProviderRegistration{}, errors.New("invalid GitLab runner name or labels")
	}
	ctx, release, err := c.reserve(ctx, 1)
	if err != nil {
		return ProviderRegistration{}, err
	}
	defer release()
	_, kind := c.runnerScope()
	body := map[string]any{"runner_type": kind, "description": name, "maintenance_note": c.ownership(name), "run_untagged": false, "tag_list": strings.Join(labels, ","), "maximum_timeout": c.timeout}
	if c.target.GitLab.ProjectID > 0 {
		body["project_id"], body["locked"] = c.target.GitLab.ProjectID, true
	} else {
		body["group_id"] = c.target.GitLab.GroupID
	}
	var result struct {
		ID        int64      `json:"id"`
		Token     string     `json:"token"`
		ExpiresAt *time.Time `json:"token_expires_at"`
	}
	if _, err = c.json(ctx, http.MethodPost, "/user/runners", nil, body, &result); err != nil {
		return ProviderRegistration{}, err
	}
	if result.ID <= 0 || !strings.HasPrefix(result.Token, "glrt-") || len(result.Token) < 16 || len(result.Token) > 4096 || strings.IndexFunc(result.Token, func(r rune) bool { return r <= ' ' || r >= 127 }) >= 0 || (result.ExpiresAt != nil && !result.ExpiresAt.After(time.Now())) {
		return ProviderRegistration{}, &GitLabError{Kind: "response", Ambiguous: true}
	}
	id := strconv.FormatInt(result.ID, 10)
	config, err := json.Marshal(GitLabManagerConfig{SchemaVersion: 1, URL: c.target.GitLab.URL, RunnerID: id, Name: name, Token: result.Token, TimeoutSeconds: c.timeout, ExpiresAt: result.ExpiresAt})
	if err != nil {
		return ProviderRegistration{}, &GitLabError{Kind: "response", Ambiguous: true}
	}
	registration := ProviderRegistration{Runner: ProviderRunner{ID: id, Name: name, Status: "starting"}, ManagerConfig: config, CleanupCredential: []byte(result.Token), ExpiresAt: result.ExpiresAt}
	if err = registration.Validate(name); err != nil {
		return ProviderRegistration{}, &GitLabError{Kind: "response", Ambiguous: true}
	}
	return registration, nil
}

type gitlabRunner struct {
	ID              int64  `json:"id"`
	Description     string `json:"description"`
	RunnerType      string `json:"runner_type"`
	MaintenanceNote string `json:"maintenance_note"`
	Status          string `json:"status"`
	ExecutionStatus string `json:"job_execution_status"`
	Paused          *bool  `json:"paused"`
	Projects        []struct {
		ID int64 `json:"id"`
	} `json:"projects"`
}

func gitlabID(id string) (int64, bool) {
	n, err := strconv.ParseInt(id, 10, 64)
	return n, err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

// Inventory is scoped by the provider endpoint and read to its bounded end.
// Group lists include inherited runners, so the ownership marker is also checked.
func (c *GitLabClient) inventory(ctx context.Context, id int64, name string) ([]gitlabRunner, error) {
	scope, kind := c.runnerScope()
	matched := []gitlabRunner{}
	seen := map[int64]bool{}
	for page := 1; page <= gitlabMaxRunnerPages; page++ {
		var runners []gitlabRunner
		header, err := c.json(ctx, http.MethodGet, scope+"/runners", url.Values{"type": {kind}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}, nil, &runners)
		if err != nil {
			return nil, err
		}
		if runners == nil {
			return nil, &GitLabError{Kind: "response"}
		}
		if len(runners) > 100 {
			return nil, &GitLabError{Kind: "bounds"}
		}
		if total := header.Get("X-Total-Pages"); total != "" {
			pages, e := strconv.Atoi(total)
			if e != nil || pages < 0 || (pages > 0 && pages < page) {
				return nil, &GitLabError{Kind: "response"}
			}
			if pages > gitlabMaxRunnerPages {
				return nil, &GitLabError{Kind: "bounds"}
			}
			if pages > page && header.Get("X-Next-Page") == "" {
				return nil, &GitLabError{Kind: "response"}
			}
		}
		for _, runner := range runners {
			if runner.ID <= 0 || seen[runner.ID] || len(runner.Description) > 1024 {
				return nil, &GitLabError{Kind: "response"}
			}
			seen[runner.ID] = true
			if (id > 0 && runner.ID == id) || (name != "" && runner.Description == name) {
				if runner.RunnerType != kind || !gitlabOwnedName.MatchString(runner.Description) {
					return nil, &GitLabError{Kind: "scope"}
				}
				matched = append(matched, runner)
				if len(matched) > 2 {
					return nil, &GitLabError{Kind: "bounds"}
				}
			}
		}
		next := header.Get("X-Next-Page")
		if next != "" {
			if next != strconv.Itoa(page+1) {
				return nil, &GitLabError{Kind: "response"}
			}
		} else if _, supplied := header["X-Next-Page"]; supplied || len(runners) < 100 {
			return matched, nil
		}
	}
	return nil, &GitLabError{Kind: "bounds"}
}

func (c *GitLabClient) detail(ctx context.Context, listed gitlabRunner, name string, requirePaused bool) (ProviderRunner, error) {
	var runner gitlabRunner
	if _, err := c.json(ctx, http.MethodGet, "/runners/"+strconv.FormatInt(listed.ID, 10), nil, nil, &runner); err != nil {
		return ProviderRunner{}, err
	}
	_, kind := c.runnerScope()
	if runner.ID != listed.ID || runner.Description != listed.Description || (name != "" && runner.Description != name) || !gitlabOwnedName.MatchString(runner.Description) || runner.RunnerType != kind || runner.MaintenanceNote != c.ownership(runner.Description) || len(runner.Status) > 64 || len(runner.ExecutionStatus) > 64 || len(runner.Projects) > 100 {
		return ProviderRunner{}, &GitLabError{Kind: "identity"}
	}
	if c.target.GitLab.ProjectID > 0 && (len(runner.Projects) != 1 || runner.Projects[0].ID != c.target.GitLab.ProjectID) {
		return ProviderRunner{}, &GitLabError{Kind: "scope"}
	}
	if requirePaused && (runner.Paused == nil || !*runner.Paused) {
		return ProviderRunner{}, &GitLabError{Kind: "busy"}
	}
	status := "offline"
	if runner.Status == "online" {
		status = "online"
	}
	return ProviderRunner{ID: strconv.FormatInt(runner.ID, 10), Name: runner.Description, Status: status, Busy: runner.ExecutionStatus != "idle", ObservedAt: time.Now().UTC()}, nil
}

func (c *GitLabClient) Get(ctx context.Context, target ProviderTarget, id string) (ProviderRunner, error) {
	return c.GetOwned(ctx, target, id, "")
}

func (c *GitLabClient) GetOwned(ctx context.Context, target ProviderTarget, id, name string) (ProviderRunner, error) {
	if err := c.requireTarget(target); err != nil {
		return ProviderRunner{}, err
	}
	numericID, valid := gitlabID(id)
	if !valid || (name != "" && !gitlabOwnedName.MatchString(name)) {
		return ProviderRunner{}, &GitLabError{Kind: "identity"}
	}
	ctx, release, err := c.reserve(ctx, 6)
	if err != nil {
		return ProviderRunner{}, err
	}
	defer release()
	runners, err := c.inventory(ctx, numericID, "")
	if err != nil {
		return ProviderRunner{}, err
	}
	if len(runners) == 0 {
		return ProviderRunner{}, ErrRunnerAbsent
	}
	if len(runners) != 1 {
		return ProviderRunner{}, &GitLabError{Kind: "identity"}
	}
	return c.detail(ctx, runners[0], name, false)
}

func (c *GitLabClient) FindOwned(ctx context.Context, target ProviderTarget, name string) ([]ProviderRunner, error) {
	if err := c.requireTarget(target); err != nil {
		return nil, err
	}
	if !gitlabOwnedName.MatchString(name) {
		return nil, &GitLabError{Kind: "identity"}
	}
	ctx, release, err := c.reserve(ctx, 7)
	if err != nil {
		return nil, err
	}
	defer release()
	runners, err := c.inventory(ctx, 0, name)
	if err != nil {
		return nil, err
	}
	result := make([]ProviderRunner, 0, len(runners))
	for _, runner := range runners {
		verified, err := c.detail(ctx, runner, name, false)
		if err != nil {
			return nil, err
		}
		result = append(result, verified)
	}
	return result, nil
}

func (c *GitLabClient) Delete(ctx context.Context, target ProviderTarget, id string) error {
	return c.DeleteOwned(ctx, target, id, "")
}

func (c *GitLabClient) DeleteOwned(ctx context.Context, target ProviderTarget, id, name string) error {
	if err := c.requireTarget(target); err != nil {
		return err
	}
	ctx, release, err := c.reserve(ctx, 9)
	if err != nil {
		return err
	}
	defer release()
	runner, err := c.GetOwned(ctx, target, id, name)
	if err != nil {
		return err
	}
	if err = c.pause(ctx, runner); err != nil {
		return err
	}
	// Pausing closes acquisition before the final idle observation. An idle
	// observation followed directly by DELETE could interrupt a newly claimed job.
	listed := gitlabRunner{ID: mustGitLabID(id), Description: runner.Name}
	runner, err = c.detail(ctx, listed, runner.Name, true)
	if err != nil {
		return err
	}
	if runner.Busy {
		return &GitLabError{Kind: "busy"}
	}
	res, err := c.request(ctx, http.MethodDelete, "/runners/"+id, nil, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		return &GitLabError{Kind: "response"}
	}
	return nil
}

func mustGitLabID(id string) int64 {
	value, _ := gitlabID(id)
	return value
}

func (c *GitLabClient) pause(ctx context.Context, runner ProviderRunner) error {
	var result gitlabRunner
	if _, err := c.json(ctx, http.MethodPut, "/runners/"+runner.ID, nil, map[string]bool{"paused": true}, &result); err != nil {
		return err
	}
	if result.ID != mustGitLabID(runner.ID) || result.Description != runner.Name || result.Paused == nil || !*result.Paused {
		return &GitLabError{Kind: "identity"}
	}
	return nil
}

func (c *GitLabClient) Drain(ctx context.Context, target ProviderTarget, id string) error {
	if err := c.requireTarget(target); err != nil {
		return err
	}
	ctx, release, err := c.reserve(ctx, 7)
	if err != nil {
		return err
	}
	defer release()
	runner, err := c.Get(ctx, target, id)
	if err != nil {
		return err
	}
	return c.pause(ctx, runner)
}

type gitlabJob struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Pipeline   struct {
		ID        int64 `json:"id"`
		ProjectID int64 `json:"project_id"`
	} `json:"pipeline"`
	Runner *gitlabRunner `json:"runner"`
}

func (c *GitLabClient) jobProject(ctx context.Context, projectID int64) error {
	if c.target.GitLab.ProjectID > 0 {
		if projectID != c.target.GitLab.ProjectID {
			return &GitLabError{Kind: "scope"}
		}
		// The exact project job endpoint verifies the job and pipeline binding.
		// Job readers do not need unrelated project administration permission.
		return nil
	}
	var project struct {
		ID        int64 `json:"id"`
		Namespace struct {
			ID       int64  `json:"id"`
			Kind     string `json:"kind"`
			FullPath string `json:"full_path"`
		} `json:"namespace"`
	}
	if _, err := c.json(ctx, http.MethodGet, "/projects/"+strconv.FormatInt(projectID, 10), nil, nil, &project); err != nil {
		return err
	}
	if project.ID != projectID {
		return &GitLabError{Kind: "scope"}
	}
	if c.target.GitLab.GroupID == 0 {
		return nil
	}
	var group struct {
		ID       int64  `json:"id"`
		FullPath string `json:"full_path"`
	}
	if _, err := c.json(ctx, http.MethodGet, "/groups/"+strconv.FormatInt(c.target.GitLab.GroupID, 10), nil, nil, &group); err != nil {
		return err
	}
	ns := project.Namespace
	if group.ID != c.target.GitLab.GroupID || ns.Kind != "group" || ns.ID <= 0 || len(group.FullPath) > 1024 || len(ns.FullPath) > 1024 || !gitlabNamespacePath.MatchString(group.FullPath) || !gitlabNamespacePath.MatchString(ns.FullPath) || !(ns.FullPath == group.FullPath || strings.HasPrefix(ns.FullPath, group.FullPath+"/")) || (ns.FullPath == group.FullPath && ns.ID != group.ID) {
		return &GitLabError{Kind: "scope"}
	}
	return nil
}

func (c *GitLabClient) verifiedJob(ctx context.Context, identity ProviderJobIdentity) (*ProviderJob, error) {
	return c.verifiedBoundJob(ctx, identity, false)
}

func (c *GitLabClient) verifiedBoundJob(ctx context.Context, identity ProviderJobIdentity, historical bool) (*ProviderJob, error) {
	projectID, projectOK := gitlabID(identity.Repository)
	runnerID, runnerOK := gitlabID(identity.RunnerID)
	runID, runOK := gitlabID(identity.RunID)
	jobID, jobOK := gitlabID(identity.JobID)
	if !projectOK || !runnerOK || !runOK || !jobOK || identity.Attempt != 0 || !gitlabOwnedName.MatchString(identity.RunnerName) {
		return nil, &GitLabError{Kind: "identity"}
	}
	if err := c.jobProject(ctx, projectID); err != nil {
		return nil, err
	}
	var job gitlabJob
	if _, err := c.json(ctx, http.MethodGet, "/projects/"+identity.Repository+"/jobs/"+identity.JobID, nil, nil, &job); err != nil {
		return nil, err
	}
	_, kind := c.runnerScope()
	if job.ID != jobID || job.Pipeline.ID != runID || job.Pipeline.ProjectID != projectID || len(job.Name) > 512 {
		return nil, &GitLabError{Kind: "identity"}
	}
	if (job.Runner == nil && !historical) || (job.Runner != nil && (job.Runner.ID != runnerID || job.Runner.Description != identity.RunnerName || job.Runner.RunnerType != kind)) {
		return nil, &GitLabError{Kind: "identity"}
	}
	status, conclusion := "", ""
	switch job.Status {
	case "created", "pending", "preparing", "waiting_for_resource", "scheduled", "manual":
		status = "queued"
	case "running", "canceling":
		status = "in_progress"
	case "success":
		status, conclusion = "completed", "success"
	case "failed":
		status, conclusion = "completed", "failure"
	case "canceled":
		status, conclusion = "completed", "cancelled"
	case "skipped":
		status, conclusion = "completed", "skipped"
	default:
		return nil, &GitLabError{Kind: "response"}
	}
	return &ProviderJob{Identity: identity, Name: job.Name, Status: status, Conclusion: conclusion, StartedAt: job.StartedAt, CompletedAt: job.FinishedAt, Steps: []Step{}}, nil
}

func (c *GitLabClient) AssignedJob(ctx context.Context, target ProviderTarget, identity ProviderJobIdentity) (*ProviderJob, error) {
	if err := c.requireTarget(target); err != nil {
		return nil, err
	}
	ctx, release, err := c.reserve(ctx, 3)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.verifiedJob(ctx, identity)
}

func (c *GitLabClient) JobLogs(ctx context.Context, target ProviderTarget, identity ProviderJobIdentity) ([]LogLine, bool, error) {
	return c.boundJobLogs(ctx, target, identity, false)
}

func (c *GitLabClient) HistoricalJob(ctx context.Context, target ProviderTarget, identity ProviderJobIdentity) (*ProviderJob, error) {
	if err := c.requireTarget(target); err != nil {
		return nil, err
	}
	ctx, release, err := c.reserve(ctx, 3)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.verifiedBoundJob(ctx, identity, true)
}

func (c *GitLabClient) HistoricalJobLogs(ctx context.Context, target ProviderTarget, identity ProviderJobIdentity) ([]LogLine, bool, error) {
	return c.boundJobLogs(ctx, target, identity, true)
}

func (c *GitLabClient) boundJobLogs(ctx context.Context, target ProviderTarget, identity ProviderJobIdentity, historical bool) ([]LogLine, bool, error) {
	if err := c.requireTarget(target); err != nil {
		return nil, false, err
	}
	ctx, release, err := c.reserve(ctx, 4)
	if err != nil {
		return nil, false, err
	}
	defer release()
	job, err := c.verifiedBoundJob(ctx, identity, historical)
	if err != nil {
		return nil, false, err
	}
	res, err := c.request(ctx, http.MethodGet, "/projects/"+identity.Repository+"/jobs/"+identity.JobID+"/trace", nil, nil)
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, false, &GitLabError{Kind: "response"}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		return nil, false, &GitLabError{Kind: "transport"}
	}
	lines, truncated := ParseLogText(data, 1<<20, job != nil && job.Status == "completed", c.credential)
	return lines, truncated, nil
}

func (c *GitLabClient) Cancel(ctx context.Context, target ProviderTarget, identity ProviderJobIdentity) (CancellationScope, error) {
	return c.CancelAuthorized(ctx, target, identity, nil)
}

// CancelAuthorized rechecks application write access after provider identity
// verification and immediately before the consequential provider request.
func (c *GitLabClient) CancelAuthorized(ctx context.Context, target ProviderTarget, identity ProviderJobIdentity, beforeWrite func(context.Context) error) (CancellationScope, error) {
	if err := c.requireTarget(target); err != nil {
		return CancellationNone, err
	}
	ctx, release, err := c.reserve(ctx, 4)
	if err != nil {
		return CancellationNone, err
	}
	defer release()
	job, err := c.verifiedJob(ctx, identity)
	if err != nil {
		return CancellationNone, err
	}
	if job.Status == "completed" {
		return CancellationJob, nil
	}
	if beforeWrite != nil {
		if err = beforeWrite(ctx); err != nil {
			return CancellationNone, err
		}
	}
	var result gitlabJob
	if _, err = c.json(ctx, http.MethodPost, "/projects/"+identity.Repository+"/jobs/"+identity.JobID+"/cancel", nil, nil, &result); err != nil {
		return CancellationNone, err
	}
	if strconv.FormatInt(result.ID, 10) != identity.JobID || strconv.FormatInt(result.Pipeline.ID, 10) != identity.RunID || strconv.FormatInt(result.Pipeline.ProjectID, 10) != identity.Repository || result.Runner == nil || strconv.FormatInt(result.Runner.ID, 10) != identity.RunnerID || result.Runner.Description != identity.RunnerName || (result.Status != "canceling" && result.Status != "canceled") {
		return CancellationNone, &GitLabError{Kind: "identity"}
	}
	return CancellationJob, nil
}
