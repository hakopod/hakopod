package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const gitlabFixtureName = "hakopod-1234567890abcdef1234567890abcdef"
const gitlabFixtureCredential = "glpat-fixture-control-plane-token"

type gitlabRoundTrip func(*http.Request) (*http.Response, error)

func (f gitlabRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func gitlabResponse(status int, value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}
}

func gitlabTestClient(t *testing.T, group bool, handler gitlabRoundTrip) *GitLabClient {
	t.Helper()
	scope := &GitLabTarget{ProjectID: 12}
	if group {
		scope = &GitLabTarget{GroupID: 15}
	}
	c, err := NewGitLabClient(ProviderTarget{Provider: ProviderGitLab, GitLab: scope}, gitlabFixtureCredential, GitLabClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = gitlabRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "gitlab.com" || !strings.HasPrefix(r.URL.Path, "/api/v4/") || r.Header.Get("PRIVATE-TOKEN") != gitlabFixtureCredential {
			t.Error("request escaped the configured provider or lost its credential header")
		}
		return handler(r)
	})
	return c
}

func gitlabRunnerFixture(c *GitLabClient, id int64) map[string]any {
	_, kind := c.runnerScope()
	return map[string]any{"id": id, "description": gitlabFixtureName, "runner_type": kind, "maintenance_note": c.ownership(gitlabFixtureName), "status": "online", "job_execution_status": "idle", "paused": false, "projects": []map[string]any{{"id": 12}}}
}

func gitlabJobFixture(status string) map[string]any {
	return map[string]any{"id": 31, "name": "native-fixture", "status": status, "pipeline": map[string]any{"id": 21, "project_id": 12}, "runner": map[string]any{"id": 41, "description": gitlabFixtureName, "runner_type": "project_type"}}
}

func gitlabIdentityFixture() ProviderJobIdentity {
	return ProviderJobIdentity{Repository: "12", RunnerID: "41", RunnerName: gitlabFixtureName, RunID: "21", JobID: "31"}
}

func TestGitLabRegistrationUsesNativeOwnedScopeAndPrivatePayload(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(fmt.Sprint(group), func(t *testing.T) {
			calls := 0
			c := gitlabTestClient(t, group, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/api/v4/user/runners" {
					t.Fatal("registration used an unexpected endpoint")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["description"] != gitlabFixtureName || body["run_untagged"] != false || body["tag_list"] != "hakopod,native" || body["maximum_timeout"] != float64(3600) {
					t.Fatal("registration omitted its ownership, label or timeout controls")
				}
				if group {
					if body["runner_type"] != "group_type" || body["group_id"] != float64(15) || body["project_id"] != nil {
						t.Fatal("group registration broadened its scope")
					}
				} else if body["runner_type"] != "project_type" || body["project_id"] != float64(12) || body["locked"] != true || body["group_id"] != nil {
					t.Fatal("project registration was not locked to its project")
				}
				return gitlabResponse(201, map[string]any{"id": 41, "token": "glrt-fixture-runner-authentication-token"}), nil
			})
			registered, err := c.Register(context.Background(), c.target, gitlabFixtureName, []string{"hakopod", "native"})
			if err != nil || calls != 1 || registered.Runner.ID != "41" || c.Capabilities().Available {
				t.Fatal("registration failed or enabled an unqualified runtime", err)
			}
			var private GitLabManagerConfig
			if json.Unmarshal(registered.ManagerConfig, &private) != nil || private.SchemaVersion != 1 || private.RunnerID != "41" || private.URL != "https://gitlab.com" || private.Token != string(registered.CleanupCredential) || private.TimeoutSeconds != 3600 {
				t.Fatal("private registration envelope is incomplete")
			}
			public, _ := json.Marshal(registered)
			if strings.Contains(string(public), "token") || strings.Contains(string(public), "glrt-") || strings.Contains(string(registered.ManagerConfig), gitlabFixtureCredential) {
				t.Fatal("registration exposed a provider credential")
			}
		})
	}
}

func TestGitLabLostRegistrationResponseRecoversWithoutSecondPost(t *testing.T) {
	posts := 0
	var c *GitLabClient
	c = gitlabTestClient(t, false, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			posts++
			return nil, errors.New("fixture transport containing " + gitlabFixtureCredential)
		}
		if r.URL.Path == "/api/v4/projects/12/runners" {
			return gitlabResponse(200, []any{gitlabRunnerFixture(c, 41)}), nil
		}
		return gitlabResponse(200, gitlabRunnerFixture(c, 41)), nil
	})
	_, err := c.Register(context.Background(), c.target, gitlabFixtureName, []string{"hakopod"})
	var failure *GitLabError
	if !errors.As(err, &failure) || !failure.Ambiguous || strings.Contains(err.Error(), gitlabFixtureCredential) {
		t.Fatal("lost registration did not retain a sanitized ambiguous outcome")
	}
	runners, err := c.FindOwned(context.Background(), c.target, gitlabFixtureName)
	if err != nil || len(runners) != 1 || runners[0].ID != "41" || posts != 1 {
		t.Fatal("registration recovery retried creation or lost its owned identity", err)
	}
}

func TestGitLabInventoryNeverTreatsFailureOrTruncationAsAbsence(t *testing.T) {
	for name, response := range map[string]func() *http.Response{
		"permission":    func() *http.Response { return gitlabResponse(403, map[string]any{"token": gitlabFixtureCredential}) },
		"missing scope": func() *http.Response { return gitlabResponse(404, nil) },
		"null list":     func() *http.Response { return gitlabResponse(200, nil) },
		"wrong type":    func() *http.Response { return gitlabResponse(200, map[string]any{"items": []any{}}) },
		"oversized inventory": func() *http.Response {
			r := gitlabResponse(200, []any{})
			header := http.Header{"X-Total-Pages": {"6"}, "X-Next-Page": {"2"}}
			r.Header = header
			return r
		},
		"skipped page": func() *http.Response {
			r := gitlabResponse(200, []any{})
			r.Header.Set("X-Next-Page", "3")
			return r
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := gitlabTestClient(t, false, func(*http.Request) (*http.Response, error) { return response(), nil })
			got, err := c.FindOwned(context.Background(), c.target, gitlabFixtureName)
			if err == nil || got != nil || strings.Contains(err.Error(), gitlabFixtureCredential) {
				t.Fatal("uncertain inventory was treated as confirmed absence")
			}
			if _, err = c.GetOwned(context.Background(), c.target, "41", gitlabFixtureName); err == nil || errors.Is(err, ErrRunnerAbsent) {
				t.Fatal("uncertain inventory authorized runner deletion recovery")
			}
		})
	}
	c := gitlabTestClient(t, false, func(*http.Request) (*http.Response, error) { return gitlabResponse(200, []any{}), nil })
	if got, err := c.FindOwned(context.Background(), c.target, gitlabFixtureName); err != nil || got == nil || len(got) != 0 {
		t.Fatal("authoritative empty scoped inventory did not confirm absence", err)
	}
	if _, err := c.GetOwned(context.Background(), c.target, "41", gitlabFixtureName); !errors.Is(err, ErrRunnerAbsent) {
		t.Fatal("complete scoped inventory did not confirm recorded runner absence", err)
	}
}

func TestGitLabOwnershipAndPausedCleanup(t *testing.T) {
	for _, mutation := range []string{"none", "name", "marker", "project", "busy", "unknown", "delete404"} {
		t.Run(mutation, func(t *testing.T) {
			paused, deleted := false, false
			var c *GitLabClient
			c = gitlabTestClient(t, false, func(r *http.Request) (*http.Response, error) {
				detail := gitlabRunnerFixture(c, 41)
				detail["paused"] = paused
				if r.URL.Path == "/api/v4/projects/12/runners" {
					return gitlabResponse(200, []any{detail}), nil
				}
				if r.Method == http.MethodPut {
					paused = true
					detail["paused"] = true
					return gitlabResponse(200, detail), nil
				}
				if r.Method == http.MethodDelete {
					if !paused {
						t.Error("deletion did not pause acquisition first")
					}
					if mutation == "delete404" {
						return gitlabResponse(404, nil), nil
					}
					deleted = true
					return gitlabResponse(204, nil), nil
				}
				switch mutation {
				case "name":
					detail["description"] = "hakopod-abcdef1234567890abcdef1234567890"
				case "marker":
					detail["maintenance_note"] = "another target"
				case "project":
					detail["projects"] = []any{map[string]any{"id": 13}}
				case "busy":
					detail["job_execution_status"] = "running"
				case "unknown":
					delete(detail, "job_execution_status")
				}
				return gitlabResponse(200, detail), nil
			})
			err := c.DeleteOwned(context.Background(), c.target, "41", gitlabFixtureName)
			if mutation == "none" {
				if err != nil || !deleted {
					t.Fatal("owned idle runner was not deleted", err)
				}
			} else if err == nil || deleted {
				t.Fatal("ambiguous, busy or foreign runner was deleted", err)
			}
		})
	}
}

func TestGitLabJobsVerifyIdentityBeforeTraceAndCancellation(t *testing.T) {
	for _, change := range []string{"none", "runner", "runner-name", "pipeline", "project", "job", "missing-runner"} {
		t.Run(change, func(t *testing.T) {
			traceCalls, cancelCalls := 0, 0
			c := gitlabTestClient(t, false, func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/v4/projects/12" {
					return gitlabResponse(200, map[string]any{"id": 12}), nil
				}
				if strings.HasSuffix(r.URL.Path, "/trace") {
					traceCalls++
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("\x1b[0Ksection_start:123:setup[collapsed=true]\r\x1b[0KSetup\nhello\r\n"))}, nil
				}
				if strings.HasSuffix(r.URL.Path, "/cancel") {
					cancelCalls++
					return gitlabResponse(200, gitlabJobFixture("canceled")), nil
				}
				job := gitlabJobFixture("running")
				switch change {
				case "runner":
					job["runner"].(map[string]any)["id"] = 42
				case "runner-name":
					job["runner"].(map[string]any)["description"] = "another"
				case "pipeline":
					job["pipeline"].(map[string]any)["id"] = 22
				case "project":
					job["pipeline"].(map[string]any)["project_id"] = 13
				case "job":
					job["id"] = 32
				case "missing-runner":
					job["runner"] = nil
				}
				return gitlabResponse(200, job), nil
			})
			lines, _, logErr := c.JobLogs(context.Background(), c.target, gitlabIdentityFixture())
			scope, cancelErr := c.Cancel(context.Background(), c.target, gitlabIdentityFixture())
			if change == "none" {
				if logErr != nil || cancelErr != nil || traceCalls != 1 || cancelCalls != 1 || scope != CancellationJob || len(lines) != 2 || (!strings.Contains(lines[0].Text, "\r") || strings.Contains(lines[0].Text, "\x1b")) || lines[1].Text != "hello" {
					t.Fatal("native log controls or single-job cancellation changed", logErr, cancelErr)
				}
			} else if logErr == nil || cancelErr == nil || traceCalls != 0 || cancelCalls != 0 {
				t.Fatal("foreign job identity reached traces or cancellation")
			}
		})
	}
}

func TestGitLabGroupJobsRequireConcreteProjectMembership(t *testing.T) {
	for _, path := range []string{"company", "company/team", "company-other", "other/company"} {
		t.Run(path, func(t *testing.T) {
			jobCalls := 0
			c := gitlabTestClient(t, true, func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/v4/projects/12" {
					ns := 16
					if path == "company" {
						ns = 15
					}
					return gitlabResponse(200, map[string]any{"id": 12, "namespace": map[string]any{"id": ns, "kind": "group", "full_path": path}}), nil
				}
				if r.URL.Path == "/api/v4/groups/15" {
					return gitlabResponse(200, map[string]any{"id": 15, "full_path": "company"}), nil
				}
				jobCalls++
				job := gitlabJobFixture("success")
				job["runner"].(map[string]any)["runner_type"] = "group_type"
				return gitlabResponse(200, job), nil
			})
			job, err := c.AssignedJob(context.Background(), c.target, gitlabIdentityFixture())
			if path == "company" || path == "company/team" {
				if err != nil || job == nil || job.Status != "completed" || len(job.Steps) != 0 {
					t.Fatal("group job rejected or steps invented", err)
				}
			} else if err == nil || jobCalls != 0 {
				t.Fatal("sibling project passed the group boundary")
			}
		})
	}
}

func TestGitLabBudgetSharesCredentialOriginAndPreservesCooldown(t *testing.T) {
	now := time.Now()
	budget := NewRequestBudget(func() time.Time { return now })
	requests := 0
	makeClient := func(project int64, credential string) *GitLabClient {
		c, err := NewGitLabClient(ProviderTarget{Provider: ProviderGitLab, GitLab: &GitLabTarget{ProjectID: project}}, credential, GitLabClientOptions{Budget: &budget})
		if err != nil {
			t.Fatal(err)
		}
		c.http.Transport = gitlabRoundTrip(func(*http.Request) (*http.Response, error) {
			requests++
			r := gitlabResponse(429, map[string]any{"secret": gitlabFixtureCredential})
			r.Header.Set("Retry-After", "120")
			return r, nil
		})
		return c
	}
	first, second := makeClient(12, gitlabFixtureCredential), makeClient(13, gitlabFixtureCredential)
	_, err := first.FindOwned(context.Background(), first.target, gitlabFixtureName)
	var retry *RetryError
	if !errors.As(err, &retry) || retry.At.Before(now.Add(120*time.Second)) || strings.Contains(err.Error(), gitlabFixtureCredential) {
		t.Fatal("GitLab cooldown or redaction was lost", err)
	}
	_, err = second.FindOwned(context.Background(), second.target, gitlabFixtureName)
	if err == nil || requests != 1 || first.budgetKey != second.budgetKey {
		t.Fatal("another pool bypassed the credential cooldown")
	}
	third := makeClient(12, "glpat-other-fixture-control-plane")
	if third.budgetKey == first.budgetKey {
		t.Fatal("independent credentials shared their budget key")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = third.FindOwned(ctx, third.target, gitlabFixtureName)
	if len(budget.entries) != 1 {
		t.Fatal("a cancelled operation created or poisoned provider budget state")
	}
}
