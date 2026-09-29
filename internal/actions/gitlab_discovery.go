package actions

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Discovery uses the provider's runner/job relationship, never manager output.
// An empty result may reflect restricted project visibility; it is not proof
// that no job ran. Native orchestration preserves that uncertainty in history.
type ProviderJobDiscoveryClient interface {
	DiscoverJobCandidates(context.Context, ProviderTarget, string, string) ([]ProviderJobIdentity, error)
}

type RunnerReuseError struct{ Jobs []ProviderJob }

func (e *RunnerReuseError) Error() string {
	return "A one-job runner processed multiple jobs; inspect the pool before starting more runners"
}

func (c *GitLabClient) DiscoverJob(ctx context.Context, target ProviderTarget, runnerID, runnerName string) (*ProviderJob, error) {
	identities, err := c.DiscoverJobCandidates(ctx, target, runnerID, runnerName)
	if err != nil {
		return nil, err
	}
	jobs := make([]ProviderJob, 0, len(identities))
	for _, identity := range identities {
		job, err := c.AssignedJob(ctx, target, identity)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	if len(jobs) > 1 {
		return nil, &RunnerReuseError{Jobs: jobs}
	}
	if len(jobs) == 0 {
		return nil, nil
	}
	return &jobs[0], nil
}

// The runner inventory endpoint requires read_runner. Its candidates must be
// independently verified with the job reader before persisting an assignment.
func (c *GitLabClient) DiscoverJobCandidates(ctx context.Context, target ProviderTarget, runnerID, runnerName string) ([]ProviderJobIdentity, error) {
	if err := c.requireTarget(target); err != nil {
		return nil, err
	}
	if _, valid := gitlabID(runnerID); !valid || !gitlabOwnedName.MatchString(runnerName) {
		return nil, &GitLabError{Kind: "identity"}
	}
	ctx, release, err := c.reserve(ctx, 1)
	if err != nil {
		return nil, err
	}
	defer release()
	var listed []struct {
		ID       int64 `json:"id"`
		Pipeline struct {
			ID        int64 `json:"id"`
			ProjectID int64 `json:"project_id"`
		} `json:"pipeline"`
		Project struct {
			ID int64 `json:"id"`
		} `json:"project"`
	}
	header, err := c.json(ctx, http.MethodGet, "/runners/"+runnerID+"/jobs", url.Values{"per_page": {"2"}, "page": {"1"}, "order_by": {"id"}, "sort": {"desc"}}, nil, &listed)
	if err != nil {
		return nil, err
	}
	if listed == nil || len(listed) > 2 {
		return nil, &GitLabError{Kind: "response"}
	}
	more := header.Get("X-Next-Page") != "" || strings.Contains(header.Get("Link"), `rel="next"`)
	for _, key := range []string{"X-Total", "X-Total-Pages"} {
		if value := header.Get(key); value != "" {
			n, e := strconv.Atoi(value)
			if e != nil || n < 0 {
				return nil, &GitLabError{Kind: "response"}
			}
			more = more || (key == "X-Total" && n > len(listed)) || (key == "X-Total-Pages" && n > 1)
		}
	}
	if more && len(listed) < 2 {
		return nil, &GitLabError{Kind: "bounds"}
	}
	identities := make([]ProviderJobIdentity, 0, len(listed))
	seen := map[int64]bool{}
	for _, item := range listed {
		if item.ID <= 0 || item.Pipeline.ID <= 0 || item.Project.ID <= 0 || seen[item.ID] || (item.Pipeline.ProjectID != 0 && item.Pipeline.ProjectID != item.Project.ID) {
			return nil, &GitLabError{Kind: "identity"}
		}
		seen[item.ID] = true
		identity := ProviderJobIdentity{RunnerID: runnerID, RunnerName: runnerName, Repository: strconv.FormatInt(item.Project.ID, 10), RunID: strconv.FormatInt(item.Pipeline.ID, 10), JobID: strconv.FormatInt(item.ID, 10)}
		identities = append(identities, identity)
	}
	return identities, nil
}

// DrainOwned permits orchestration to checkpoint acquisition shutdown before
// bounded history discovery. The read credential never manages registrations.
func (c *GitLabClient) DrainOwned(ctx context.Context, target ProviderTarget, id, name string) error {
	if err := c.requireTarget(target); err != nil {
		return err
	}
	ctx, release, err := c.reserve(ctx, 7)
	if err != nil {
		return err
	}
	defer release()
	runner, err := c.GetOwned(ctx, target, id, name)
	if err != nil {
		return err
	}
	return c.pause(ctx, runner)
}
