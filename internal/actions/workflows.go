package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Observation contains only the job hook's allowlisted metadata. It is untrusted
// until GitHub confirms the recorded runner was assigned this run and attempt.
type Observation struct {
	Repository string `json:"repository"`
	Workflow   string `json:"workflow"`
	RunID      int64  `json:"run_id"`
	RunNumber  int64  `json:"run_number"`
	Attempt    int64  `json:"attempt"`
	JobKey     string `json:"job_key"`
	Branch     string `json:"branch"`
	SHA        string `json:"sha"`
}

func (o Observation) Valid(target Target) bool {
	if !ValidRepository(o.Repository) || o.RunID <= 0 || o.RunID > 9007199254740991 || o.Attempt < 1 || o.Attempt > 10000 {
		return false
	}
	if len(o.Workflow) > 256 || len(o.JobKey) > 256 || len(o.Branch) > 256 || len(o.SHA) > 64 {
		return false
	}
	if target.Repository != "" {
		return strings.EqualFold(target.Repository, o.Repository)
	}
	return target.Valid() && strings.EqualFold(strings.Split(o.Repository, "/")[0], target.Organization)
}

type Step struct {
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	Number      int        `json:"number"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}
type Job struct {
	ID          int64      `json:"id"`
	RunID       int64      `json:"run_id"`
	RunAttempt  int64      `json:"run_attempt"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	RunnerID    int64      `json:"runner_id"`
	RunnerName  string     `json:"runner_name"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	Steps       []Step     `json:"steps"`
}

// AssignedJob cannot return jobs belonging to another runner in the same repo.
func (c *Client) AssignedJob(ctx context.Context, o Observation, runnerID int64, runnerName string) (*Job, error) {
	if !ValidRepository(o.Repository) || o.RunID <= 0 || o.Attempt < 1 || runnerID <= 0 || !labelPattern.MatchString(runnerName) {
		return nil, errors.New("invalid workflow identity")
	}
	for page := 1; page <= 5; page++ {
		var result struct {
			Jobs  []Job `json:"jobs"`
			Total int   `json:"total_count"`
		}
		path := fmt.Sprintf("/repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=100&page=%d", o.Repository, o.RunID, o.Attempt, page)
		if err := c.request(ctx, http.MethodGet, path, nil, &result); err != nil {
			return nil, err
		}
		if len(result.Jobs) > 100 {
			return nil, errors.New("workflow jobs exceed response bounds")
		}
		for _, job := range result.Jobs {
			if job.RunnerID == runnerID && job.RunnerName == runnerName && job.RunID == o.RunID && job.RunAttempt == o.Attempt && job.ID > 0 && len(job.Steps) <= 1000 {
				return &job, nil
			}
		}
		if len(result.Jobs) < 100 {
			return nil, nil
		}
	}
	return nil, errors.New("workflow has more than 500 jobs; open it in GitHub")
}

type LogLine struct {
	Number int64  `json:"number"`
	Text   string `json:"text"`
}
type Output struct {
	Job       *Observation `json:"job,omitempty"`
	Lines     []LogLine    `json:"lines"`
	Truncated bool         `json:"truncated"`
	Finished  bool         `json:"finished"`
	Available bool         `json:"available"`
}

// ParseOutput accepts only the observer's envelope; diagnostic lines never
// become workflow output. Callers establish pod/application ownership first.
func ParseOutput(data []byte) Output {
	out := Output{Lines: []LogLine{}, Truncated: len(data) >= 2<<20}
	bytes := 0
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "HAKOPOD_WORKFLOW_V1 ") || len(line) > 128<<10 {
			continue
		}
		var item struct {
			Job       *Observation `json:"job"`
			Line      int64        `json:"line"`
			Text      string       `json:"text"`
			Truncated bool         `json:"truncated"`
			Finished  bool         `json:"finished"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "HAKOPOD_WORKFLOW_V1 ")), &item) != nil {
			continue
		}
		out.Available = true
		if item.Job != nil {
			out.Job = item.Job
		}
		out.Truncated = out.Truncated || item.Truncated
		out.Finished = out.Finished || item.Finished
		if item.Line > 0 && item.Line <= 10000000 && len(item.Text) <= 64<<10 {
			if len(out.Lines) > 0 && item.Line <= out.Lines[len(out.Lines)-1].Number {
				continue
			}
			out.Lines = append(out.Lines, LogLine{Number: item.Line, Text: item.Text})
			bytes += len(item.Text)
			for bytes > 1<<20 || len(out.Lines) > 10000 {
				bytes -= len(out.Lines[0].Text)
				out.Lines = out.Lines[1:]
				out.Truncated = true
			}
		}
	}
	if len(out.Lines) > 0 && out.Lines[0].Number > 1 {
		out.Truncated = true
	}
	return out
}

// JobLogs uses a separate credential-free transport for GitHub's signed URL.
// No arbitrary redirect host, Authorization forwarding or redirect chain.
func (c *Client) JobLogs(ctx context.Context, repository string, id int64) ([]LogLine, bool, error) {
	if !ValidRepository(repository) || id <= 0 {
		return nil, false, errors.New("invalid job identity")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/actions/jobs/%d/logs", c.base, repository, id), nil)
	if err != nil {
		return nil, false, errors.New("invalid log request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, false, errors.New("GitHub logs are unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		return nil, false, &StatusError{Status: res.StatusCode}
	}
	signed, err := url.Parse(res.Header.Get("Location"))
	if err != nil || signed.Scheme != "https" || signed.User != nil || signed.Port() != "" || !strings.HasSuffix(signed.Hostname(), ".blob.core.windows.net") {
		return nil, false, errors.New("GitHub returned an unsupported log download location")
	}
	download, err := http.NewRequestWithContext(ctx, http.MethodGet, signed.String(), nil)
	if err != nil {
		return nil, false, errors.New("invalid log download")
	}
	transport := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	body, err := transport.Do(download)
	if err != nil {
		return nil, false, errors.New("GitHub log download did not complete")
	}
	defer body.Body.Close()
	if body.StatusCode != 200 {
		return nil, false, &StatusError{Status: body.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(body.Body, (1<<20)+1))
	if err != nil {
		return nil, false, errors.New("GitHub log download did not complete")
	}
	truncated := len(data) > 1<<20
	if truncated {
		data = data[:1<<20]
	}
	lines := []LogLine{}
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if i >= 10000 {
			truncated = true
			break
		}
		lines = append(lines, LogLine{Number: int64(i + 1), Text: strings.TrimSuffix(line, "\r")})
	}
	return lines, truncated, nil
}
