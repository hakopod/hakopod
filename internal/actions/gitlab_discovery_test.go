package actions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func gitlabDiscoveredJob(id int64) map[string]any {
	return map[string]any{"id": id, "pipeline": map[string]any{"id": 21}, "project": map[string]any{"id": 12}}
}

func TestGitLabRunnerDiscoveryCandidatesDoNotRequireJobReadPermission(t *testing.T) {
	c := gitlabTestClient(t, false, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/runners/41/jobs" {
			t.Fatalf("runner credential read a job endpoint: %s", r.URL.Path)
		}
		return gitlabResponse(200, []any{gitlabDiscoveredJob(31)}), nil
	})
	identities, err := c.DiscoverJobCandidates(context.Background(), c.target, "41", gitlabFixtureName)
	if err != nil || len(identities) != 1 || identities[0] != gitlabIdentityFixture() {
		t.Fatal("bounded runner candidates were not returned for independent verification", err)
	}
}

func TestGitLabHistoricalReadsAllowOnlyDeletedRunnerAssociation(t *testing.T) {
	for _, mutation := range []string{"deleted", "runner", "project", "pipeline", "job"} {
		t.Run(mutation, func(t *testing.T) {
			traces := 0
			c := gitlabTestClient(t, false, func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/trace") {
					traces++
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("historical development trace\n"))}, nil
				}
				if r.URL.Path != "/api/v4/projects/12/jobs/31" {
					t.Fatal("historical read escaped the exact original job")
				}
				job := gitlabJobFixture("success")
				job["runner"] = nil
				switch mutation {
				case "runner":
					job["runner"] = map[string]any{"id": 42, "description": gitlabFixtureName, "runner_type": "project_type"}
				case "project":
					job["pipeline"].(map[string]any)["project_id"] = 13
				case "pipeline":
					job["pipeline"].(map[string]any)["id"] = 22
				case "job":
					job["id"] = 32
				}
				return gitlabResponse(200, job), nil
			})
			job, err := c.AssignedJob(context.Background(), c.target, gitlabIdentityFixture())
			if err == nil || job != nil {
				t.Fatal("unproven historical identity bypassed live runner validation")
			}
			job, err = c.HistoricalJob(context.Background(), c.target, gitlabIdentityFixture())
			if mutation == "deleted" {
				if err != nil || job == nil || job.Status != "completed" {
					t.Fatal("persisted exact binding could not read completed job with deleted runner", err)
				}
			} else if err == nil || job != nil {
				t.Fatal("historical path accepted a conflicting binding")
			}
			lines, _, err := c.HistoricalJobLogs(context.Background(), c.target, gitlabIdentityFixture())
			if mutation == "deleted" {
				if err != nil || len(lines) != 1 || traces != 1 {
					t.Fatal("historical logs unavailable", err)
				}
			} else if err == nil || traces != 0 {
				t.Fatal("historical logs skipped binding verification")
			}
		})
	}
}

func TestGitLabDiscoveryUsesReadOnlyRunnerJobsAndVerifiesAssignment(t *testing.T) {
	for _, change := range []string{"none", "runner", "project", "pipeline", "job", "duplicate", "two", "truncated", "null"} {
		t.Run(change, func(t *testing.T) {
			calls := 0
			c := gitlabTestClient(t, false, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet {
					t.Fatal("discovery made a provider mutation")
				}
				if r.URL.Path == "/api/v4/runners/41/jobs" {
					if r.URL.Query().Get("per_page") != "2" || r.URL.Query().Get("page") != "1" || r.URL.Query().Get("status") != "" {
						t.Fatal("discovery broadened its bound or hid completed jobs")
					}
					if change == "null" {
						return gitlabResponse(200, nil), nil
					}
					listed := []any{gitlabDiscoveredJob(31)}
					if change == "duplicate" {
						listed = append(listed, gitlabDiscoveredJob(31))
					}
					if change == "two" {
						listed = append(listed, gitlabDiscoveredJob(32))
					}
					response := gitlabResponse(200, listed)
					if change == "truncated" {
						response.Header.Set("X-Next-Page", "2")
					}
					return response, nil
				}
				if r.URL.Path != "/api/v4/projects/12/jobs/31" && r.URL.Path != "/api/v4/projects/12/jobs/32" {
					t.Fatalf("read credential reached non-job endpoint %s", r.URL.Path)
				}
				job := gitlabJobFixture("running")
				if r.URL.Path == "/api/v4/projects/12/jobs/32" {
					job["id"] = 32
				}
				switch change {
				case "runner":
					job["runner"].(map[string]any)["id"] = 42
				case "project":
					job["pipeline"].(map[string]any)["project_id"] = 13
				case "pipeline":
					job["pipeline"].(map[string]any)["id"] = 22
				case "job":
					job["id"] = 99
				}
				return gitlabResponse(200, job), nil
			})
			job, err := c.DiscoverJob(context.Background(), c.target, "41", gitlabFixtureName)
			if change == "none" {
				if err != nil || job == nil || job.Identity != gitlabIdentityFixture() || calls != 2 {
					t.Fatal("discovery did not verify exact assignment with minimal job access", err)
				}
			} else if change == "two" {
				var reuse *RunnerReuseError
				if !errors.As(err, &reuse) || len(reuse.Jobs) != 2 || job != nil {
					t.Fatal("one-job reuse did not preserve both verified identities", err)
				}
			} else if err == nil || job != nil {
				t.Fatal("unverified assignment or partial inventory accepted")
			}
		})
	}
}

func TestGitLabDiscoveryEmptyResultDoesNotInventJobOrClaimAbsence(t *testing.T) {
	c := gitlabTestClient(t, false, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v4/runners/41/jobs" {
			t.Fatal("empty discovery reached manager API")
		}
		return gitlabResponse(200, []any{}), nil
	})
	job, err := c.DiscoverJob(context.Background(), c.target, "41", gitlabFixtureName)
	if err != nil || job != nil {
		t.Fatal("empty visible inventory invented a job", err)
	}
}

func TestGitLabCancellationRechecksAuthorizationImmediatelyBeforeWrite(t *testing.T) {
	posts := 0
	c := gitlabTestClient(t, false, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			posts++
			return gitlabResponse(200, gitlabJobFixture("canceled")), nil
		}
		if r.URL.Path != "/api/v4/projects/12/jobs/31" {
			t.Fatal("exact project cancellation required unrelated project access")
		}
		return gitlabResponse(200, gitlabJobFixture("running")), nil
	})
	revoked := fmt.Errorf("development fixture access revoked")
	scope, err := c.CancelAuthorized(context.Background(), c.target, gitlabIdentityFixture(), func(context.Context) error { return revoked })
	if !errors.Is(err, revoked) || posts != 0 || scope != CancellationNone {
		t.Fatal("revoked write access reached provider mutation")
	}
	scope, err = c.CancelAuthorized(context.Background(), c.target, gitlabIdentityFixture(), func(context.Context) error { return nil })
	if err != nil || posts != 1 || scope != CancellationJob {
		t.Fatal("authorized exact-job cancellation failed", err)
	}
}
