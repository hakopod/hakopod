package actions

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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
		if _, err := one.Get(ctx, Target{Organization: "team"}, 1); err != nil {
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
	now = now.Add(time.Second)
	if _, err = two.AssignedJob(ctx, Observation{Repository: "team/repo", RunID: 1, Attempt: 1}, 1, "fixture"); err != nil {
		t.Fatal(err)
	}
	other, _ := NewWithBudget("fixture_independent_provider_token", budget)
	other.base = server.URL
	if _, err = other.Get(ctx, Target{Organization: "team"}, 1); err != nil {
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
			_, err := client.Get(context.Background(), Target{Organization: "team"}, 1)
			var retry *RetryError
			if !errors.As(err, &retry) || retry.At.Before(until) {
				t.Fatalf("missing deadline: %v", err)
			}
			now = until.Add(-time.Second)
			if _, err = client.Get(context.Background(), Target{Organization: "team"}, 1); !errors.As(err, &retry) || calls != 1 {
				t.Fatal("provider called during cooldown", calls, err)
			}
			now = retry.At
			if _, err = client.Get(context.Background(), Target{Organization: "team"}, 1); err != nil || calls != 2 {
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
		go func() { _, err := client.Get(context.Background(), Target{Organization: "team"}, 1); results <- err }()
	}
	<-entered
	<-entered
	_, err := client.Get(context.Background(), Target{Organization: "team"}, 1)
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
		if _, err := other.Get(ctx, Target{Organization: "team"}, 1); err != nil {
			t.Fatal(err)
		}
	}
	release, err := client.PrepareRegistration(ctx, Target{Organization: "team"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var retry *RetryError
	if _, err = other.Get(ctx, Target{Organization: "team"}, 1); !errors.As(err, &retry) {
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
	_, err := client.Get(context.Background(), Target{Organization: "team"}, 1)
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
	_, _ = canceled.Get(ctx, Target{Organization: "team"}, 1)
	for _, state := range clean.entries {
		if state.inflight != 0 || !state.blocked.IsZero() {
			t.Fatal("a canceled reader poisoned shared provider access")
		}
	}
}
