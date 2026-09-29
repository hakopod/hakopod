package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func inventoryPage(w http.ResponseWriter, total, page int, busy bool) {
	runners := []Runner{}
	for id := (page-1)*100 + 1; id <= min(total, page*100); id++ {
		runners = append(runners, Runner{ID: int64(id), Name: fmt.Sprintf("runner-%d", id), Status: "online", Busy: busy})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"total_count": total, "runners": runners})
}

func TestInventoryShares1000RunnersWithoutRefreshingTheirTimestamps(t *testing.T) {
	now := time.Unix(1800000000, 0)
	start := now
	budget := &RequestBudget{now: func() time.Time { return now }}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			t.Error("unexpected direct lookup")
			page = 1
		}
		inventoryPage(w, 1000, page, false)
	}))
	defer server.Close()
	client, _ := NewWithBudget("fixture_inventory_provider_token", budget)
	client.base = server.URL
	for page := 1; page <= 10; page++ {
		runner, err := client.Get(context.Background(), Target{Organization: "team"}, 1)
		if page < 10 {
			var retry *RetryError
			if !errors.As(err, &retry) || runner.ID != 0 {
				t.Fatal("partial inventory was exposed", page, runner, err)
			}
		} else if err != nil || runner.ID != 1 || !runner.ObservedAt.Equal(start) {
			t.Fatal("complete inventory invalid", runner, err)
		}
		now = now.Add(time.Second)
	}
	second, _ := NewWithBudget("fixture_inventory_provider_token", budget)
	second.base = server.URL
	for id := int64(1); id <= 1000; id++ {
		runner, err := second.Get(context.Background(), Target{Organization: "team"}, id)
		if err != nil || runner.ID != id || now.Sub(runner.ObservedAt) > 10*time.Second {
			t.Fatal(id, runner, err)
		}
	}
	if calls != 10 {
		t.Fatalf("1000 runners used %d provider calls, want10 pages", calls)
	}
	now = now.Add(30 * time.Second)
	runner, err := second.Get(context.Background(), Target{Organization: "team"}, 1)
	var retry *RetryError
	if !errors.As(err, &retry) || runner.ID != 0 || calls != 11 {
		t.Fatal("expired snapshot was reused", runner, err, calls)
	}
	t.Log("1000 runners:10 provider pages; all cache hits retain original page timestamps; expired snapshot is unavailable until refresh completes")
}

func TestInventoryMissingAndRetirementUseFreshIdentity(t *testing.T) {
	now := time.Unix(1800000000, 0)
	budget := &RequestBudget{now: func() time.Time { return now }}
	busy := false
	direct := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/runners") {
			inventoryPage(w, 1, 1, busy)
			return
		}
		direct++
		if strings.HasSuffix(r.URL.Path, "/2") {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(Runner{ID: 1, Name: "runner-1", Status: "online", Busy: busy})
	}))
	defer server.Close()
	client, _ := NewWithBudget("fixture_inventory_provider_token", budget)
	client.base = server.URL
	if runner, err := client.Get(context.Background(), Target{Organization: "team"}, 1); err != nil || runner.Busy {
		t.Fatal(runner, err)
	}
	busy = true
	now = now.Add(time.Second)
	if runner, err := client.GetFresh(context.Background(), Target{Organization: "team"}, 1); err != nil || !runner.Busy || direct != 1 {
		t.Fatal("retirement reused stale idle state", runner, err)
	}
	_, err := client.Get(context.Background(), Target{Organization: "team"}, 2)
	var status *StatusError
	if !errors.As(err, &status) || status.Status != 404 || direct != 2 {
		t.Fatal("snapshot absence bypassed identity check", err, direct)
	}
}

func TestInventoryCoalescesConcurrentPages(t *testing.T) {
	budget := &RequestBudget{}
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(entered)
		<-release
		inventoryPage(w, 1000, 1, false)
	}))
	defer server.Close()
	client, _ := NewWithBudget("fixture_inventory_provider_token", budget)
	client.base = server.URL
	done := make(chan error, 1)
	go func() { _, err := client.Get(context.Background(), Target{Organization: "team"}, 1); done <- err }()
	<-entered
	_, err := client.Get(context.Background(), Target{Organization: "team"}, 2)
	var retry *RetryError
	if !errors.As(err, &retry) || calls.Load() != 1 {
		t.Fatal("concurrent reader duplicated refresh", err, calls.Load())
	}
	close(release)
	if err = <-done; !errors.As(err, &retry) {
		t.Fatal(err)
	}
}

func TestInventoryResumesPageAfterTransientFailure(t *testing.T) {
	now := time.Unix(1800000000, 0)
	budget := &RequestBudget{now: func() time.Time { return now }}
	pages := []int{}
	failed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		pages = append(pages, page)
		if page == 2 && !failed {
			failed = true
			w.WriteHeader(503)
			return
		}
		inventoryPage(w, 250, page, false)
	}))
	defer server.Close()
	client, _ := NewWithBudget("fixture_inventory_provider_token", budget)
	client.base = server.URL
	for i := 0; i < 4; i++ {
		runner, err := client.Get(context.Background(), Target{Organization: "team"}, 1)
		if i == 3 && (err != nil || runner.ID != 1) {
			t.Fatal(runner, err)
		}
		now = now.Add(time.Second)
	}
	if fmt.Sprint(pages) != "[1 2 2 3]" {
		t.Fatal("pagination restarted or skipped a failed page", pages)
	}
}

func TestInventoryLargeOrganizationFallsBackWithoutBlockingTrackedRunner(t *testing.T) {
	budget := &RequestBudget{}
	pages, direct := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/runners") {
			pages++
			inventoryPage(w, 100001, 1, false)
		} else {
			direct++
			fmt.Fprint(w, `{"id":42,"name":"tracked","status":"online"}`)
		}
	}))
	defer server.Close()
	client, _ := NewWithBudget("fixture_inventory_provider_token", budget)
	client.base = server.URL
	for i := 0; i < 3; i++ {
		runner, err := client.Get(context.Background(), Target{Organization: "team"}, 42)
		if err != nil || runner.ID != 42 {
			t.Fatal("unrelated fleet blocked tracked runner", runner, err)
		}
	}
	if pages != 1 || direct != 3 || budget.inventoryRecords != 0 {
		t.Fatal("fallback repeatedly scanned unrelated fleet", pages, direct, budget.inventoryRecords)
	}
}

func TestInventoryRecordBoundFallsBackAndStaleEntriesExpire(t *testing.T) {
	now := time.Unix(1800000000, 0)
	budget := &RequestBudget{now: func() time.Time { return now }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/runners") {
			inventoryPage(w, 100, 1, false)
		} else {
			fmt.Fprint(w, `{"id":1,"status":"online"}`)
		}
	}))
	defer server.Close()
	for i := 0; i < 101; i++ {
		client, _ := NewWithBudget(fmt.Sprintf("fixture_inventory_provider_token_%d", i), budget)
		client.base = server.URL
		if runner, err := client.Get(context.Background(), Target{Organization: "team"}, 1); err != nil || runner.ID != 1 {
			t.Fatal(i, runner, err)
		}
	}
	if budget.inventoryRecords != maxInventoryRecords || len(budget.inventories) > maxInventoryTargets {
		t.Fatal("inventory exceeded memory bound", budget.inventoryRecords, len(budget.inventories))
	}
	now = now.Add(inventoryTTL)
	client, _ := NewWithBudget("fixture_new_inventory_provider_token", budget)
	client.base = server.URL
	if _, err := client.Get(context.Background(), Target{Organization: "team"}, 1); err != nil {
		t.Fatal(err)
	}
	if budget.inventoryRecords != 100 {
		t.Fatal("expired records were retained", budget.inventoryRecords)
	}
}

func TestRunnerAbsenceRequiresSuccessfulScopedNameConfirmation(t *testing.T) {
	for _, scenario := range []string{"collection-denied", "direct-denied", "confirmed-absent", "named-busy"} {
		t.Run(scenario, func(t *testing.T) {
			budget := &RequestBudget{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("name") != "" {
					switch scenario {
					case "confirmed-absent":
						fmt.Fprint(w, `{"total_count":0,"runners":[]}`)
					case "named-busy":
						fmt.Fprint(w, `{"total_count":1,"runners":[{"id":1,"name":"fixture","status":"online","busy":true}]}`)
					default:
						w.WriteHeader(404)
					}
					return
				}
				w.WriteHeader(404)
			}))
			defer server.Close()
			client, _ := NewWithBudget("fixture_absence_provider_token", budget)
			client.base = server.URL
			runner, err := client.ObserveRunner(context.Background(), Target{Organization: "team"}, 1, "fixture", scenario != "collection-denied")
			switch scenario {
			case "confirmed-absent":
				if !errors.Is(err, ErrRunnerAbsent) {
					t.Fatal("missing confirmed absence", err)
				}
			case "named-busy":
				if err == nil || errors.Is(err, ErrRunnerAbsent) || runner.ID != 0 {
					t.Fatal("contradictory identity was accepted as authoritative", runner, err)
				}
			default:
				var status *StatusError
				if errors.Is(err, ErrRunnerAbsent) || !errors.As(err, &status) || !status.Scope {
					t.Fatal("scope failure was treated as runner absence", err)
				}
			}
		})
	}
}
