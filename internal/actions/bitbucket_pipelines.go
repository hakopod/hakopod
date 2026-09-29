package actions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const bitbucketActivityPageSize = 20
const bitbucketActivityMaxPages = 5
const bitbucketLogBytes = 1 << 20

var bitbucketContentRange = regexp.MustCompile(`^bytes 0-([0-9]{1,7})/([0-9]{1,16}|\*)$`)

// BitbucketRepositoryPipeline is repository activity, not a claim that a
// particular Hakopod runner executed this pipeline or any of its steps.
type BitbucketRepositoryPipeline struct {
	ID          string     `json:"id"`
	Number      int64      `json:"number"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type BitbucketRepositoryStep struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type bitbucketPipelineState struct {
	Name   string `json:"name"`
	Result *struct {
		Name string `json:"name"`
	} `json:"result"`
}

func (s bitbucketPipelineState) normalized() (string, string, error) {
	switch s.Name {
	case "PENDING", "PAUSED":
		if s.Result == nil {
			return "queued", "", nil
		}
	case "IN_PROGRESS":
		if s.Result == nil {
			return "in_progress", "", nil
		}
	case "COMPLETED":
		if s.Result != nil {
			switch s.Result.Name {
			case "SUCCESSFUL":
				return "completed", "success", nil
			case "FAILED", "ERROR":
				return "completed", "failure", nil
			case "STOPPED":
				return "completed", "cancelled", nil
			case "EXPIRED":
				return "completed", "timed_out", nil
			case "SKIPPED", "NOT_RUN":
				return "completed", "skipped", nil
			}
		}
	}
	return "", "", &BitbucketError{Kind: "response"}
}

type bitbucketPipeline struct {
	UUID        string                 `json:"uuid"`
	BuildNumber int64                  `json:"build_number"`
	CreatedOn   time.Time              `json:"created_on"`
	CompletedOn *time.Time             `json:"completed_on"`
	State       bitbucketPipelineState `json:"state"`
	Repository  struct {
		UUID string `json:"uuid"`
	} `json:"repository"`
}

type bitbucketPipelineStep struct {
	UUID        string                 `json:"uuid"`
	Name        string                 `json:"name"`
	StartedOn   *time.Time             `json:"started_on"`
	CompletedOn *time.Time             `json:"completed_on"`
	State       bitbucketPipelineState `json:"state"`
}

func (c *BitbucketClient) repositoryPipelinesPath(target ProviderTarget) (string, error) {
	if err := c.requireTarget(target); err != nil {
		return "", err
	}
	if c.target.Bitbucket.Repository == "" {
		return "", &BitbucketError{Kind: "scope"}
	}
	return "/2.0/repositories/" + c.target.Bitbucket.Workspace + "/" + c.target.Bitbucket.Repository + "/pipelines", nil
}

func (c *BitbucketClient) validRepositoryActivityPath(path string) bool {
	base, err := c.repositoryPipelinesPath(c.target)
	if err != nil {
		return false
	}
	if path == base {
		return true
	}
	if !strings.HasPrefix(path, base+"/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, base+"/"), "/")
	if id, ok := canonicalProviderUUID(parts[0]); !ok || id != parts[0] {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	if len(parts) > 4 || parts[1] != "steps" {
		return false
	}
	if len(parts) == 2 {
		return true
	}
	if id, ok := canonicalProviderUUID(parts[2]); !ok || id != parts[2] {
		return false
	}
	return len(parts) == 3 || (len(parts) == 4 && parts[3] == "log")
}

// repositoryActivityRequest deliberately only reads fixed-origin public API
// paths. It shares the adapter's credential budget and never follows redirects.
func (c *BitbucketClient) repositoryActivityRequest(ctx context.Context, path string, query url.Values, log bool) (*http.Response, error) {
	if !c.validRepositoryActivityPath(path) || (log != strings.HasSuffix(path, "/log")) {
		return nil, &BitbucketError{Kind: "scope"}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	address := url.URL{Scheme: "https", Host: "api.bitbucket.org", Path: path, RawQuery: query.Encode()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		return nil, &BitbucketError{Kind: "scope"}
	}
	req.Header.Set("Authorization", c.authorization)
	if log {
		req.Header.Set("Range", "bytes=0-1048575")
		req.Header.Set("Accept", "text/plain")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	reservation, ok := ctx.Value(bitbucketReservationKey{}).(*bitbucketReservation)
	if !ok || reservation.client != c || reservation.remaining <= 0 {
		return nil, &BitbucketError{Kind: "bounds"}
	}
	c.budget.mu.Lock()
	blocked, until, status := reservation.state.blocked.After(c.budget.time()), reservation.state.blocked, reservation.state.status
	c.budget.mu.Unlock()
	if blocked {
		return nil, &BitbucketError{Kind: "rate", RetryAt: until, Status: status}
	}
	reservation.remaining--
	markRequest(ctx)
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			c.budget.observe(reservation.state, 0, nil)
		}
		return nil, c.failure("transport", 0, false)
	}
	c.budget.observe(reservation.state, response.StatusCode, response.Header)
	if response.StatusCode != http.StatusOK && !(log && response.StatusCode == http.StatusPartialContent) {
		response.Body.Close()
		kind := "status"
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			kind = "redirect"
		}
		return nil, c.failure(kind, response.StatusCode, false)
	}
	return response, nil
}

func (c *BitbucketClient) repositoryActivityJSON(ctx context.Context, path string, query url.Values, out any) error {
	response, err := c.repositoryActivityRequest(ctx, path, query, false)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, out) != nil {
		return c.failure("response", response.StatusCode, false)
	}
	return nil
}

func (c *BitbucketClient) pipelineView(pipeline bitbucketPipeline) (BitbucketRepositoryPipeline, error) {
	id, ok := canonicalProviderUUID(pipeline.UUID)
	repository, repoOK := canonicalProviderUUID(pipeline.Repository.UUID)
	if !ok || !repoOK || repository != c.target.Bitbucket.Repository || pipeline.BuildNumber <= 0 || pipeline.BuildNumber > 9007199254740991 || pipeline.CreatedOn.IsZero() {
		return BitbucketRepositoryPipeline{}, &BitbucketError{Kind: "identity"}
	}
	status, conclusion, err := pipeline.State.normalized()
	if err != nil || (pipeline.CompletedOn != nil && pipeline.CompletedOn.Before(pipeline.CreatedOn)) || (status != "completed" && pipeline.CompletedOn != nil) {
		return BitbucketRepositoryPipeline{}, &BitbucketError{Kind: "response"}
	}
	return BitbucketRepositoryPipeline{ID: id, Number: pipeline.BuildNumber, Status: status, Conclusion: conclusion, CreatedAt: pipeline.CreatedOn, CompletedAt: pipeline.CompletedOn}, nil
}

func bitbucketStepView(step bitbucketPipelineStep) (BitbucketRepositoryStep, error) {
	id, ok := canonicalProviderUUID(step.UUID)
	if !ok || len(step.Name) > 512 || strings.ContainsAny(step.Name, "\x00\r\n") {
		return BitbucketRepositoryStep{}, &BitbucketError{Kind: "identity"}
	}
	status, conclusion, err := step.State.normalized()
	if err != nil || (step.StartedOn != nil && step.CompletedOn != nil && step.CompletedOn.Before(*step.StartedOn)) || (status != "completed" && step.CompletedOn != nil) {
		return BitbucketRepositoryStep{}, &BitbucketError{Kind: "response"}
	}
	return BitbucketRepositoryStep{ID: id, Name: step.Name, Status: status, Conclusion: conclusion, StartedAt: step.StartedOn, CompletedAt: step.CompletedOn}, nil
}

func bitbucketActivityPage(path string, next string, page int) error {
	if next == "" {
		return nil
	}
	u, err := url.Parse(next)
	if err != nil || len(next) > 4096 || u.Scheme != "https" || u.Host != "api.bitbucket.org" || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.Path != path || u.RawPath != "" {
		return &BitbucketError{Kind: "scope"}
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || query.Get("page") != strconv.Itoa(page+1) || query.Get("pagelen") != strconv.Itoa(bitbucketActivityPageSize) {
		return &BitbucketError{Kind: "bounds"}
	}
	for key, values := range query {
		if len(values) != 1 || (key != "page" && key != "pagelen" && key != "sort") || (key == "sort" && values[0] != "-created_on") {
			return &BitbucketError{Kind: "scope"}
		}
	}
	return nil
}

// RepositoryPipelines returns at most twenty pipelines from a bounded five-page
// window. The caller must label this as repository activity, not runner history.
func (c *BitbucketClient) RepositoryPipelines(ctx context.Context, target ProviderTarget, page int) ([]BitbucketRepositoryPipeline, bool, error) {
	path, err := c.repositoryPipelinesPath(target)
	if err != nil {
		return nil, false, err
	}
	if page < 1 || page > bitbucketActivityMaxPages {
		return nil, false, &BitbucketError{Kind: "bounds"}
	}
	ctx, release, err := c.reserve(ctx, 1)
	if err != nil {
		return nil, false, err
	}
	defer release()
	var response struct {
		Values []bitbucketPipeline `json:"values"`
		Next   string              `json:"next"`
		Page   int                 `json:"page"`
	}
	query := url.Values{"page": {strconv.Itoa(page)}, "pagelen": {strconv.Itoa(bitbucketActivityPageSize)}, "sort": {"-created_on"}}
	if err = c.repositoryActivityJSON(ctx, path, query, &response); err != nil {
		return nil, false, err
	}
	if response.Values == nil || len(response.Values) > bitbucketActivityPageSize || (response.Page != 0 && response.Page != page) || (response.Next != "" && len(response.Values) == 0) {
		return nil, false, &BitbucketError{Kind: "bounds"}
	}
	if err = bitbucketActivityPage(path, response.Next, page); err != nil {
		return nil, false, err
	}
	items, seen := make([]BitbucketRepositoryPipeline, 0, len(response.Values)), map[string]bool{}
	for _, pipeline := range response.Values {
		item, err := c.pipelineView(pipeline)
		if err != nil {
			return nil, false, err
		}
		if seen[item.ID] {
			return nil, false, &BitbucketError{Kind: "identity"}
		}
		seen[item.ID] = true
		items = append(items, item)
	}
	return items, response.Next != "", nil
}

func (c *BitbucketClient) repositoryPipeline(ctx context.Context, path, id string) (BitbucketRepositoryPipeline, error) {
	var pipeline bitbucketPipeline
	if err := c.repositoryActivityJSON(ctx, path+"/"+id, nil, &pipeline); err != nil {
		return BitbucketRepositoryPipeline{}, err
	}
	item, err := c.pipelineView(pipeline)
	if err == nil && item.ID != id {
		err = &BitbucketError{Kind: "identity"}
	}
	return item, err
}

func (c *BitbucketClient) RepositoryPipelineSteps(ctx context.Context, target ProviderTarget, pipelineID string) ([]BitbucketRepositoryStep, bool, error) {
	path, err := c.repositoryPipelinesPath(target)
	id, ok := canonicalProviderUUID(pipelineID)
	if err != nil || !ok || id != pipelineID {
		return nil, false, &BitbucketError{Kind: "scope"}
	}
	ctx, release, err := c.reserve(ctx, 2)
	if err != nil {
		return nil, false, err
	}
	defer release()
	if _, err = c.repositoryPipeline(ctx, path, id); err != nil {
		return nil, false, err
	}
	path += "/" + id + "/steps"
	var response struct {
		Values []bitbucketPipelineStep `json:"values"`
		Next   string                  `json:"next"`
	}
	if err = c.repositoryActivityJSON(ctx, path, url.Values{"pagelen": {strconv.Itoa(bitbucketActivityPageSize)}, "page": {"1"}}, &response); err != nil {
		return nil, false, err
	}
	if response.Values == nil || len(response.Values) > bitbucketActivityPageSize || (response.Next != "" && len(response.Values) == 0) {
		return nil, false, &BitbucketError{Kind: "bounds"}
	}
	if err = bitbucketActivityPage(path, response.Next, 1); err != nil {
		return nil, false, err
	}
	steps, seen := make([]BitbucketRepositoryStep, 0, len(response.Values)), map[string]bool{}
	for _, value := range response.Values {
		step, err := bitbucketStepView(value)
		if err != nil {
			return nil, false, err
		}
		if seen[step.ID] {
			return nil, false, &BitbucketError{Kind: "identity"}
		}
		seen[step.ID] = true
		steps = append(steps, step)
	}
	return steps, response.Next != "", nil
}

// RepositoryStepLogs verifies the repository, pipeline and step on every read.
// Those facts intentionally do not establish assignment to a Hakopod runner.
func (c *BitbucketClient) RepositoryStepLogs(ctx context.Context, target ProviderTarget, pipelineID, stepID string) ([]LogLine, bool, error) {
	path, err := c.repositoryPipelinesPath(target)
	pipeline, pipelineOK := canonicalProviderUUID(pipelineID)
	step, stepOK := canonicalProviderUUID(stepID)
	if err != nil || !pipelineOK || !stepOK || pipeline != pipelineID || step != stepID {
		return nil, false, &BitbucketError{Kind: "scope"}
	}
	ctx, release, err := c.reserve(ctx, 3)
	if err != nil {
		return nil, false, err
	}
	defer release()
	if _, err = c.repositoryPipeline(ctx, path, pipeline); err != nil {
		return nil, false, err
	}
	path += "/" + pipeline + "/steps/" + step
	var nativeStep bitbucketPipelineStep
	if err = c.repositoryActivityJSON(ctx, path, nil, &nativeStep); err != nil {
		return nil, false, err
	}
	observed, err := bitbucketStepView(nativeStep)
	if err != nil || observed.ID != step {
		return nil, false, &BitbucketError{Kind: "identity"}
	}
	response, err := c.repositoryActivityRequest(ctx, path+"/log", nil, true)
	if err != nil {
		return nil, false, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, bitbucketLogBytes+1))
	if err != nil {
		return nil, false, &BitbucketError{Kind: "transport"}
	}
	truncated := len(data) > bitbucketLogBytes
	if response.StatusCode == http.StatusPartialContent {
		parts := bitbucketContentRange.FindStringSubmatch(response.Header.Get("Content-Range"))
		if parts == nil {
			return nil, false, &BitbucketError{Kind: "response"}
		}
		end, parseErr := strconv.ParseInt(parts[1], 10, 64)
		if parseErr != nil || end < 0 || end >= bitbucketLogBytes || end+1 != int64(len(data)) {
			return nil, false, &BitbucketError{Kind: "response"}
		}
		if parts[2] == "*" {
			truncated = true
		} else {
			total, parseErr := strconv.ParseInt(parts[2], 10, 64)
			if parseErr != nil || total <= end {
				return nil, false, &BitbucketError{Kind: "response"}
			}
			truncated = total > end+1
		}
	} else if response.Header.Get("Content-Range") != "" {
		return nil, false, &BitbucketError{Kind: "response"}
	}
	lines, cut := ParseLogText(data, bitbucketLogBytes, observed.Status == "completed" && !truncated, c.logSecrets...)
	return lines, truncated || cut, nil
}
