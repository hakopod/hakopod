package actions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCredentialBudgetSharedAcrossManagementAndWorkflowCalls(t *testing.T) {
	now := time.Unix(1800000000, 0)
	budget := &RequestBudget{now: func() time.Time { return now }}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"id":1,"jobs":[],"total_count":0}`)
	}))
	defer server.Close()
	one, _ := NewWithBudget("fixture_shared_provider_token", budget)
	two, _ := NewWithBudget("fixture_shared_provider_token", budget)
	one.base = server.URL
	two.base = server.URL
	ctx := context.Background()
	for i := 0; i < credentialBurst; i++ {
		if _, err := one.GetFresh(ctx, Target{Organization: "team"}, 1); err != nil {
			t.Fatal(err)
		}
	}
	_, err := two.AssignedJob(ctx, Observation{Repository: "team/repo", RunID: 1, Attempt: 1}, 1, "fixture")
	var retry *RetryError
	if !errors.As(err, &retry) || calls != credentialBurst {
		t.Fatalf("workflow bypassed shared quota: calls=%d err=%v", calls, err)
	}
	if _, _, err = two.JobLogs(ctx, "team/repo", 1); !errors.As(err, &retry) || calls != credentialBurst {
		t.Fatalf("log endpoint bypassed shared quota: calls=%d err=%v", calls, err)
	}
	now = now.Add(5 * time.Second)
	if _, err = two.AssignedJob(ctx, Observation{Repository: "team/repo", RunID: 1, Attempt: 1}, 1, "fixture"); err != nil {
		t.Fatal(err)
	}
	other, _ := NewWithBudget("fixture_independent_provider_token", budget)
	other.base = server.URL
	if _, err = other.GetFresh(ctx, Target{Organization: "team"}, 1); err != nil {
		t.Fatal("unrelated credential blocked", err)
	}
}

func TestCredentialBudgetHonorsProviderDeadlines(t *testing.T) {
	for _, kind := range []string{"retry-seconds", "retry-date", "reset", "secondary"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Unix(1800000000, 0)
			budget := &RequestBudget{now: func() time.Time { return now }}
			calls := 0
			until := now.Add(2 * time.Minute)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls > 1 {
					fmt.Fprint(w, `{"id":1}`)
					return
				}
				switch kind {
				case "retry-seconds":
					w.Header().Set("Retry-After", "120")
				case "retry-date":
					w.Header().Set("Retry-After", until.UTC().Format(http.TimeFormat))
				case "reset":
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(until.Unix(), 10))
				case "secondary":
					until = now.Add(time.Minute)
				}
				w.WriteHeader(429)
			}))
			defer server.Close()
			client, _ := NewWithBudget("fixture_shared_provider_token", budget)
			client.base = server.URL
			_, err := client.GetFresh(context.Background(), Target{Organization: "team"}, 1)
			var retry *RetryError
			if !errors.As(err, &retry) || retry.At.Before(until) {
				t.Fatalf("missing deadline: %v", err)
			}
			now = until.Add(-time.Second)
			if _, err = client.GetFresh(context.Background(), Target{Organization: "team"}, 1); !errors.As(err, &retry) || calls != 1 {
				t.Fatal("provider called during cooldown", calls, err)
			}
			now = retry.At
			if _, err = client.GetFresh(context.Background(), Target{Organization: "team"}, 1); err != nil || calls != 2 {
				t.Fatal("did not resume", calls, err)
			}
		})
	}
}

func TestCredentialBudgetBoundsConcurrentRequests(t *testing.T) {
	budget := &RequestBudget{}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		fmt.Fprint(w, `{"id":1}`)
	}))
	defer server.Close()
	client, _ := NewWithBudget("fixture_concurrent_provider_token", budget)
	client.base = server.URL
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := client.GetFresh(context.Background(), Target{Organization: "team"}, 1)
			results <- err
		}()
	}
	<-entered
	<-entered
	_, err := client.GetFresh(context.Background(), Target{Organization: "team"}, 1)
	var retry *RetryError
	if !errors.As(err, &retry) || calls.Load() != 2 {
		t.Fatal("concurrency bound escaped", err, calls.Load())
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCredentialBudgetStateIsBoundedAndInactiveEntriesExpire(t *testing.T) {
	now := time.Unix(1800000000, 0)
	budget := &RequestBudget{now: func() time.Time { return now }}
	for i := 0; i < maxCredentialBudgets; i++ {
		v, err := budget.acquire(sha256.Sum256([]byte(strconv.Itoa(i))))
		if err != nil {
			t.Fatal(err)
		}
		budget.release(v)
	}
	key := sha256.Sum256([]byte("new-credential"))
	if _, err := budget.acquire(key); err == nil {
		t.Fatal("budget map grew past its bound")
	}
	now = now.Add(time.Hour)
	v, err := budget.acquire(key)
	if err != nil {
		t.Fatal(err)
	}
	budget.release(v)
	if len(budget.entries) > maxCredentialBudgets {
		t.Fatal("unbounded budget entries")
	}
}

func TestRegistrationReservesGroupLookupAndJITCredits(t *testing.T) {
	now := time.Unix(1800000000, 0)
	budget := &RequestBudget{now: func() time.Time { return now }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			fmt.Fprint(w, `{"runner":{"id":42,"name":"fixture"},"encoded_jit_config":"opaque"}`)
		case r.URL.Path == "/orgs/team/actions/runner-groups":
			fmt.Fprint(w, `{"total_count":1,"runner_groups":[{"id":1,"default":true}]}`)
		default:
			fmt.Fprint(w, `{"id":1}`)
		}
	}))
	defer server.Close()
	client, _ := NewWithBudget("fixture_reserved_provider_token", budget)
	client.base = server.URL
	other, _ := NewWithBudget("fixture_reserved_provider_token", budget)
	other.base = server.URL
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		if _, err := other.GetFresh(ctx, Target{Organization: "team"}, 1); err != nil {
			t.Fatal(err)
		}
	}
	release, err := client.PrepareRegistration(ctx, Target{Organization: "team"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var retry *RetryError
	if _, err = other.GetFresh(ctx, Target{Organization: "team"}, 1); !errors.As(err, &retry) {
		t.Fatal("another client spent reserved credits", err)
	}
	if registration, err := client.Register(ctx, Target{Organization: "team"}, "fixture", []string{"hakopod"}); err != nil || registration.Runner.ID != 42 {
		t.Fatal("reserved registration failed", registration, err)
	}
	release()
	for _, state := range budget.entries {
		if state.inflight != 0 {
			t.Fatal("registration leaked its concurrency reservation")
		}
	}
}

func TestPermissionFailurePreservesStatusAndCancellationDoesNotBlockCredential(t *testing.T) {
	budget := &RequestBudget{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer server.Close()
	client, _ := NewWithBudget("fixture_permission_provider_token", budget)
	client.base = server.URL
	_, err := client.GetFresh(context.Background(), Target{Organization: "team"}, 1)
	var status *StatusError
	var retry *RetryError
	if !errors.As(err, &status) || status.Status != 403 || !errors.As(err, &retry) {
		t.Fatal("permission failure lost diagnostics or cooldown", err)
	}
	clean := &RequestBudget{}
	canceled, _ := NewWithBudget("fixture_canceled_provider_token", clean)
	canceled.base = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = canceled.GetFresh(ctx, Target{Organization: "team"}, 1)
	for _, state := range clean.entries {
		if state.inflight != 0 || !state.blocked.IsZero() {
			t.Fatal("a canceled reader poisoned shared provider access")
		}
	}
}

func TestPaginatedDefaultGroupNeverSpendsReservedJITCredit(t *testing.T) {
	now := time.Unix(1800000000, 0)
	budget := &RequestBudget{now: func() time.Time { return now }}
	groupPages, posts := []int{}, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			fmt.Fprint(w, `{"runner":{"id":42,"name":"fixture"},"encoded_jit_config":"opaque"}`)
			return
		}
		if strings.Contains(r.URL.Path, "runner-groups") {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			groupPages = append(groupPages, page)
			if page == 1 {
				fmt.Fprint(w, `{"total_count":101,"runner_groups":[`)
				for i := 0; i < 100; i++ {
					if i > 0 {
						fmt.Fprint(w, ",")
					}
					fmt.Fprintf(w, `{"id":%d,"default":false}`, i+1)
				}
				fmt.Fprint(w, "]}")
			} else {
				fmt.Fprint(w, `{"total_count":101,"runner_groups":[{"id":101,"default":true}]}`)
			}
			return
		}
		fmt.Fprint(w, `{"id":1}`)
	}))
	defer server.Close()
	client, _ := NewWithBudget("fixture_paged_group_provider_token", budget)
	client.base = server.URL
	for i := 0; i < 8; i++ {
		if _, err := client.GetFresh(context.Background(), Target{Organization: "team"}, 1); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		release, err := client.PrepareRegistration(context.Background(), Target{Organization: "team"})
		var retry *RetryError
		if !errors.As(err, &retry) || release != nil {
			t.Fatal("intent became possible before JIT credit existed", release != nil, err)
		}
	}
	now = now.Add(time.Second)
	release, err := client.PrepareRegistration(context.Background(), Target{Organization: "team"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err = client.Register(context.Background(), Target{Organization: "team"}, "fixture", []string{"hakopod"}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(groupPages) != "[1 2]" || posts != 1 {
		t.Fatal("group pagination did not resume/cache before JIT", groupPages, posts)
	}
}

type budgetTestTransport func(*http.Request) (*http.Response, error)

func (f budgetTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSignedLogDownloadsDoNotHoldRunnerManagementPermits(t *testing.T) {
	budget := &RequestBudget{}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	old := http.DefaultTransport
	http.DefaultTransport = budgetTestTransport(func(r *http.Request) (*http.Response, error) {
		status := 200
		header := http.Header{}
		body := `{"id":1}`
		if r.URL.Host == "fixture.blob.core.windows.net" {
			started <- struct{}{}
			<-release
			body = "2026-09-29T00:00:00Z fixture log"
		} else if strings.HasSuffix(r.URL.Path, "/logs") {
			status = 302
			header.Set("Location", "https://fixture.blob.core.windows.net/job-log")
			body = ""
		} else if strings.Contains(r.URL.Path, "/actions/jobs/") {
			body = `{"id":1,"run_id":9,"run_attempt":2,"runner_id":42,"runner_name":"hakopod-one"}`
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	defer func() { http.DefaultTransport = old }()
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		client, _ := NewWithBudget("fixture_shared_download_token", budget)
		go func() {
			done, err := client.PrepareWorkflowLogs(context.Background(), true)
			if err != nil {
				results <- err
				return
			}
			defer done()
			if _, err = client.AssignedJobID(context.Background(), Observation{Repository: "team/repo", RunID: 9, Attempt: 2}, 42, "hakopod-one", 1); err == nil {
				_, _, err = client.JobLogs(context.Background(), "team/repo", 1)
			}
			results <- err
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case err := <-results:
			close(release)
			t.Fatal("download did not start", err)
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("timed out waiting for signed downloads")
		}
	}
	client, _ := NewWithBudget("fixture_shared_download_token", budget)
	_, err := client.GetFresh(context.Background(), Target{Repository: "team/repo"}, 1)
	close(release)
	if err != nil {
		t.Fatal("signed downloads starved runner management", err)
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestKnownWorkflowLogsCompleteWithTwoRemainingCredits(t *testing.T) {
	now := time.Unix(1800000000, 0)
	budget := &RequestBudget{now: func() time.Time { return now }}
	requests, downloads := 0, 0
	old := http.DefaultTransport
	http.DefaultTransport = budgetTestTransport(func(r *http.Request) (*http.Response, error) {
		status := 200
		header := http.Header{}
		body := `{"id":42}`
		if r.URL.Host == "fixture.blob.core.windows.net" {
			downloads++
			if r.Header.Get("Authorization") != "" {
				t.Error("credential reached signed download")
			}
			body = "2026-09-29T00:00:00Z fixture log"
		} else {
			requests++
			if strings.HasSuffix(r.URL.Path, "/logs") {
				status = 302
				header.Set("Location", "https://fixture.blob.core.windows.net/job-log")
				body = ""
			} else if strings.Contains(r.URL.Path, "/actions/jobs/") {
				body = `{"id":1,"run_id":9,"run_attempt":2,"runner_id":42,"runner_name":"hakopod-one"}`
			}
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	defer func() { http.DefaultTransport = old }()
	client, _ := NewWithBudget("fixture_known_workflow_budget_token", budget)
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		if _, err := client.GetFresh(ctx, Target{Repository: "team/repo"}, 42); err != nil {
			t.Fatal(err)
		}
	}
	release, err := client.PrepareWorkflowLogs(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	job, err := client.AssignedJobID(ctx, Observation{Repository: "team/repo", RunID: 9, Attempt: 2}, 42, "hakopod-one", 1)
	if err != nil || job == nil {
		t.Fatal("known job verification did not fit reserved budget", job, err)
	}
	lines, truncated, err := client.JobLogs(ctx, "team/repo", job.ID)
	if err != nil || truncated || len(lines) != 1 || requests != 10 || downloads != 1 {
		t.Fatal("verified log download exceeded its two API credits", lines, truncated, err, requests, downloads)
	}
	if _, err = client.GetFresh(ctx, Target{Repository: "team/repo"}, 42); err == nil || requests != 10 {
		t.Fatal("fixture did not exhaust the request budget", err, requests)
	}
}

func TestDiscoveredWorkflowLogsReserveFifthPageAndRedirectTogether(t *testing.T) {
	now := time.Unix(1800000000, 0)
	budget := &RequestBudget{now: func() time.Time { return now }}
	requests, pages, downloads := 0, []int{}, 0
	old := http.DefaultTransport
	http.DefaultTransport = budgetTestTransport(func(r *http.Request) (*http.Response, error) {
		status := 200
		header := http.Header{}
		body := `{"id":42}`
		if r.URL.Host == "fixture.blob.core.windows.net" {
			downloads++
			body = "2026-09-29T00:00:00Z fixture log"
		} else {
			requests++
			if strings.HasSuffix(r.URL.Path, "/logs") {
				status = 302
				header.Set("Location", "https://fixture.blob.core.windows.net/job-log")
				body = ""
			} else if strings.HasSuffix(r.URL.Path, "/jobs") {
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				pages = append(pages, page)
				jobs := make([]Job, 100)
				for i := range jobs {
					jobs[i] = Job{ID: int64((page-1)*100 + i + 1), RunID: 9, RunAttempt: 2, RunnerID: 99, RunnerName: "other-runner"}
				}
				if page == 5 {
					jobs[99].RunnerID = 42
					jobs[99].RunnerName = "hakopod-one"
				}
				encoded, _ := json.Marshal(map[string]any{"total_count": 500, "jobs": jobs})
				body = string(encoded)
			}
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	defer func() { http.DefaultTransport = old }()
	client, _ := NewWithBudget("fixture_discovered_workflow_budget_token", budget)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := client.GetFresh(ctx, Target{Repository: "team/repo"}, 42); err != nil {
			t.Fatal(err)
		}
	}
	if release, err := client.PrepareWorkflowLogs(ctx, false); err == nil || release != nil || requests != 5 {
		t.Fatal("discovery began without redirect capacity", err, requests)
	}
	now = now.Add(time.Second)
	release, err := client.PrepareWorkflowLogs(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	job, err := client.AssignedJob(ctx, Observation{Repository: "team/repo", RunID: 9, Attempt: 2}, 42, "hakopod-one")
	if err != nil || job == nil || job.ID != 500 {
		t.Fatal("fifth-page discovery did not complete within reservation", job, err)
	}
	lines, truncated, err := client.JobLogs(ctx, "team/repo", job.ID)
	if err != nil || truncated || len(lines) != 1 || fmt.Sprint(pages) != "[1 2 3 4 5]" || requests != 11 || downloads != 1 {
		t.Fatal("fifth page starved its signed redirect", err, truncated, len(lines), pages, requests, downloads)
	}
	if _, err = client.GetFresh(ctx, Target{Repository: "team/repo"}, 42); err == nil || requests != 11 {
		t.Fatal("fixture did not exhaust the request budget", err, requests)
	}
}

func TestCleanupReservesFreshCheckAndDeleteBeforeSpendingEither(t *testing.T) {
	now := time.Unix(1800000000, 0)
	budget := &RequestBudget{now: func() time.Time { return now }}
	gets, deletes := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes++
			w.WriteHeader(204)
			return
		}
		gets++
		fmt.Fprint(w, `{"id":1,"name":"fixture","status":"online","busy":false}`)
	}))
	defer server.Close()
	client, _ := NewWithBudget("fixture_cleanup_provider_token", budget)
	client.base = server.URL
	for i := 0; i < 9; i++ {
		if _, err := client.GetFresh(context.Background(), Target{Organization: "team"}, 1); err != nil {
			t.Fatal(err)
		}
	}
	if release, err := client.PrepareCleanup(context.Background(), Target{Organization: "team"}, 1); err == nil || release != nil || gets != 9 {
		t.Fatal("cleanup spent its only token on a partial attempt", err, gets)
	}
	now = now.Add(time.Second)
	release, err := client.PrepareCleanup(context.Background(), Target{Organization: "team"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err = client.GetKnownFresh(context.Background(), Target{Organization: "team"}, 1, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err = client.Delete(context.Background(), Target{Organization: "team"}, 1); err != nil {
		t.Fatal(err)
	}
	if gets != 10 || deletes != 1 {
		t.Fatal("cleanup did not make progress after two credits refilled", gets, deletes)
	}
}
