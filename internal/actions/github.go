// Package actions manages short-lived GitHub runner registrations. Long-lived
// provider credentials are used by the control plane, never passed to job pods.
package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
var labelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func ValidRepository(repository string) bool {
	return repositoryPattern.MatchString(repository) && !strings.Contains(repository, "..")
}

// Target identifies one GitHub registration scope. Zero RunnerGroupID selects
// the organization's default group; repository pools retain GitHub's group 1.
type Target struct {
	Repository    string
	Organization  string
	RunnerGroupID int64
}

var organizationPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

func (t Target) Valid() bool {
	if t.Organization != "" {
		return t.Repository == "" && organizationPattern.MatchString(t.Organization) && !strings.Contains(t.Organization, "--") && t.RunnerGroupID >= 0 && t.RunnerGroupID <= 9007199254740991
	}
	return ValidRepository(t.Repository) && t.RunnerGroupID == 0
}

func ValidLabels(labels []string) bool {
	if len(labels) < 1 || len(labels) > 8 {
		return false
	}
	seen := map[string]bool{}
	for _, label := range labels {
		if !labelPattern.MatchString(label) || seen[strings.ToLower(label)] {
			return false
		}
		seen[strings.ToLower(label)] = true
	}
	return true
}

type Runner struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Busy   bool   `json:"busy"`
}
type Registration struct {
	Runner        Runner `json:"runner"`
	EncodedConfig string `json:"encoded_jit_config"`
}

// Client intentionally has no configurable production host or redirects. A
// repository name cannot route its scoped credential to a different origin.
type Client struct {
	token string
	http  *http.Client
	base  string
}

func New(token string) (*Client, error) {
	if len(token) < 16 || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00 ") {
		return nil, errors.New("supply a GitHub runner-management credential")
	}
	return &Client{token: token, base: "https://api.github.com", http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// StatusError excludes provider response bodies, which may contain credentials.
type StatusError struct{ Status int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("GitHub runner request returned HTTP %d", e.Status)
}

func (c *Client) request(ctx context.Context, method, path string, body any, out any) error {
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return errors.New("invalid runner request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(encoded))
	if err != nil {
		return errors.New("invalid runner request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("GitHub runner request did not complete; reconcile its recorded runner name before retrying")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &StatusError{Status: res.StatusCode}
	}
	if out == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("GitHub runner response exceeds its bounds")
	}
	if json.Unmarshal(data, out) != nil {
		return errors.New("invalid GitHub runner response")
	}
	return nil
}
func runnerBase(target Target) (string, error) {
	if !target.Valid() {
		return "", errors.New("select either a GitHub.com organization or owner/repository; runner groups apply only to organizations")
	}
	if target.Organization != "" {
		return "/orgs/" + url.PathEscape(target.Organization) + "/actions/runners", nil
	}
	parts := strings.Split(target.Repository, "/")
	return "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/actions/runners", nil
}

func (c *Client) defaultRunnerGroup(ctx context.Context, organization string) (int64, error) {
	for page := 1; page <= 10; page++ {
		var result struct {
			Total  int `json:"total_count"`
			Groups []struct {
				ID      int64 `json:"id"`
				Default bool  `json:"default"`
			} `json:"runner_groups"`
		}
		path := fmt.Sprintf("/orgs/%s/actions/runner-groups?per_page=100&page=%d", url.PathEscape(organization), page)
		if err := c.request(ctx, http.MethodGet, path, nil, &result); err != nil {
			return 0, err
		}
		if result.Total > 1000 || len(result.Groups) > 100 {
			return 0, errors.New("runner group inventory exceeds the supported bound; select a runner group ID")
		}
		for _, group := range result.Groups {
			if group.Default && group.ID > 0 {
				return group.ID, nil
			}
		}
		if len(result.Groups) < 100 {
			break
		}
	}
	return 0, errors.New("GitHub default runner group was not found; select a runner group ID")
}

func (c *Client) Register(ctx context.Context, target Target, name string, labels []string) (Registration, error) {
	var result Registration
	base, err := runnerBase(target)
	if err != nil {
		return result, err
	}
	if !labelPattern.MatchString(name) || !ValidLabels(labels) {
		return result, errors.New("invalid runner name or labels")
	}
	group := int64(1)
	if target.Organization != "" {
		group = target.RunnerGroupID
		if group == 0 {
			group, err = c.defaultRunnerGroup(ctx, target.Organization)
			if err != nil {
				return result, err
			}
		}
	}
	err = c.request(ctx, http.MethodPost, base+"/generate-jitconfig", map[string]any{"name": name, "runner_group_id": group, "labels": labels, "work_folder": "_work"}, &result)
	if err != nil {
		return Registration{}, err
	}
	if result.Runner.ID <= 0 || result.Runner.Name != name || len(result.EncodedConfig) < 1 || len(result.EncodedConfig) > 128<<10 {
		return Registration{}, errors.New("GitHub runner registration is incomplete; reconcile the recorded name")
	}
	return result, nil
}
func (c *Client) Get(ctx context.Context, target Target, id int64) (Runner, error) {
	var out Runner
	base, err := runnerBase(target)
	if err != nil {
		return out, err
	}
	if id <= 0 {
		return out, errors.New("invalid runner identity")
	}
	err = c.request(ctx, http.MethodGet, fmt.Sprintf("%s/%d", base, id), nil, &out)
	if err == nil && out.ID != id {
		return Runner{}, errors.New("GitHub returned a different runner identity")
	}
	return out, err
}
func (c *Client) Delete(ctx context.Context, target Target, id int64) error {
	base, err := runnerBase(target)
	if err != nil {
		return err
	}
	if id <= 0 {
		return errors.New("invalid runner identity")
	}
	err = c.request(ctx, http.MethodDelete, fmt.Sprintf("%s/%d", base, id), nil, nil)
	var status *StatusError
	if errors.As(err, &status) && status.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// Find recovers an ambiguous registration response using its previously persisted
// unique name. It never creates another runner while the first outcome is unknown.
func (c *Client) Find(ctx context.Context, target Target, name string) (*Runner, error) {
	base, err := runnerBase(target)
	if err != nil {
		return nil, err
	}
	if !labelPattern.MatchString(name) {
		return nil, errors.New("invalid runner name")
	}
	for page := 1; page <= 10; page++ {
		var list struct {
			Runners []Runner `json:"runners"`
			Total   int      `json:"total_count"`
		}
		err = c.request(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=100&page=%d", base, page), nil, &list)
		if err != nil {
			return nil, err
		}
		if len(list.Runners) > 100 || list.Total > 1000 {
			return nil, errors.New("runner inventory exceeds the supported bound")
		}
		for _, runner := range list.Runners {
			if runner.Name == name {
				if runner.ID <= 0 {
					return nil, errors.New("invalid runner identity")
				}
				return &runner, nil
			}
		}
		if len(list.Runners) < 100 {
			return nil, nil
		}
	}
	return nil, errors.New("runner inventory could not be fully reconciled")
}
