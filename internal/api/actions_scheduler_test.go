package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func actionsSchedulePools(t *testing.T, db *store.Store, count int) []store.ActionsPool {
	t.Helper()
	out := make([]store.ActionsPool, 0, count)
	for i := 0; i < count; i++ {
		app := store.Application{ID: fmt.Sprintf("schedule-%03d", i), Project: "fixture", Environment: "test", Name: fmt.Sprintf("pool-%03d", i)}
		config := spec.Application{SchemaVersion: 1, Name: app.Name, Services: map[string]spec.Service{"runner": {Image: spec.ActionsRunnerImage, Replicas: 10, Actions: &spec.Actions{Repository: "team/repo", Credential: "token"}}}}
		if err := db.SyncActions(context.Background(), app, config, 1); err != nil {
			t.Fatal(err)
		}
		pools, err := db.ActionsPools(context.Background(), app.ID)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, pools[0])
	}
	return out
}

func TestActionsSchedulerSlowPoolDoesNotBlockFleet(t *testing.T) {
	db := actionsDatabase(t)
	pools := actionsSchedulePools(t, db, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var active, maxActive atomic.Int32
	var mu sync.Mutex
	latencies := make([]time.Duration, 0, 99)
	started := time.Now()
	slowStarted := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		runActionsWorkers(ctx, db, func(ctx context.Context, p store.ActionsPool) error {
			current := active.Add(1)
			defer active.Add(-1)
			for previous := maxActive.Load(); current > previous; previous = maxActive.Load() {
				if maxActive.CompareAndSwap(previous, current) {
					break
				}
			}
			if p.ApplicationID == pools[0].ApplicationID {
				close(slowStarted)
				<-ctx.Done()
				return ctx.Err()
			}
			// Ten independent runner observations represent a full configured pool.
			for slot := 0; slot < 10; slot++ {
				time.Sleep(time.Millisecond)
			}
			mu.Lock()
			latencies = append(latencies, time.Since(started))
			complete := len(latencies) == 99
			mu.Unlock()
			if complete {
				cancel()
			}
			return nil
		})
		close(finished)
	}()
	<-slowStarted
	<-finished
	if len(latencies) != 99 {
		t.Fatalf("slow pool blocked fleet: completed %d/99", len(latencies))
	}
	if maxActive.Load() != actionsWorkers || active.Load() != 0 {
		t.Fatalf("worker bound/release: peak=%d remaining=%d", maxActive.Load(), active.Load())
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("100 pools / 1,000 slots, one held pool: 99 other pools completed; worker peak=%d; p95=%s max=%s", maxActive.Load(), latencies[94], latencies[98])
}

func TestActionsSchedulingLeaseRecoveryAndProviderDeadline(t *testing.T) {
	db := actionsDatabase(t)
	actionsSchedulePools(t, db, 1)
	ctx := context.Background()
	work, err := db.NextActionsPool(ctx)
	if err != nil || work == nil {
		t.Fatal(err)
	}
	if duplicate, err := db.NextActionsPool(ctx); err != nil || duplicate != nil {
		t.Fatal("leased work duplicated", err)
	}
	// A restarted process resumes an expired lease without special memory.
	if _, err = db.Pool.Exec(ctx, `UPDATE actions_pools SET next_reconcile_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	recovered, err := db.NextActionsPool(ctx)
	if err != nil || recovered == nil {
		t.Fatal("expired lease not recovered", err)
	}
	deadline := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	if err = db.CompleteActionsPool(ctx, *recovered, deadline, false); err != nil {
		t.Fatal(err)
	}
	// Completion from the old process cannot erase a newer provider cooldown.
	if err = db.CompleteActionsPool(ctx, *work, time.Now().Add(-time.Second), true); err != nil {
		t.Fatal(err)
	}
	var saved time.Time
	if err = db.Pool.QueryRow(ctx, `SELECT next_reconcile_at FROM actions_pools`).Scan(&saved); err != nil || !saved.Equal(deadline) {
		t.Fatal("retry deadline lost", saved, err)
	}
	if early, err := db.NextActionsPool(ctx); err != nil || early != nil {
		t.Fatal("work ran during provider cooldown", err)
	}
}

func TestActionsCancelledClaimClosesItsSessionBeforeReleaseReturns(t *testing.T) {
	db := actionsDatabase(t)
	ctx := context.Background()
	claim, err := db.ClaimActions(ctx, "cancelled-claim-fixture")
	if err != nil || claim == nil {
		t.Fatal("fixture claim unavailable", err)
	}
	defer claim.Release()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// Keep the fence query in flight until its context is cancelled. pgx closes
	// this session asynchronously, while the separate transaction stays open.
	if _, err = tx.Exec(ctx, `LOCK TABLE deployments IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	fence, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	err = claim.Check(fence)
	cancel()
	if err == nil {
		t.Fatal("blocked fence was not cancelled")
	}
	started := time.Now()
	claim.Release()
	if elapsed := time.Since(started); elapsed > 3500*time.Millisecond {
		t.Fatal("claim release exceeded its bounded cleanup deadline", elapsed)
	}
	var held int
	if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&held); err != nil || held != 0 {
		t.Fatal("cancelled session retained its application claim after release", held, err)
	}
	if err = claim.Check(ctx); !errors.Is(err, store.ErrClaimLost) {
		t.Fatal("released claim remained usable", err)
	}
}

func TestActionsSchedulerRereadsSelectedPoolBeyondHistoricalPage(t *testing.T) {
	server, fake, pool, _ := actionsHarness(t)
	ctx := context.Background()
	_, err := server.Store.Pool.Exec(ctx, `INSERT INTO actions_pools(application_id,service,project,environment,application_name,revision,config,removed)
SELECT application_id,'old-'||lpad(i::text,3,'0'),project,environment,application_name,revision,config,true
FROM actions_pools CROSS JOIN generate_series(1,205) AS i WHERE application_id=$1 AND service=$2`, pool.ApplicationID, pool.Service)
	if err != nil {
		t.Fatal(err)
	}
	page, err := server.Store.ActionsPoolsPage(ctx, pool.ApplicationID, "", "")
	if err != nil || len(page) != 200 || page[len(page)-1].Service == pool.Service {
		t.Fatal("fixture did not place active pool beyond first historical page", len(page), err)
	}
	err = server.reconcileScheduledActions(ctx, pool)
	var warm *actionsWarmup
	if err != nil && !errors.As(err, &warm) {
		t.Fatal(err)
	}
	if len(fake.runners) != 1 || len(fake.pods) != 1 {
		t.Fatal("historical pool page hid selected active service", len(fake.runners), len(fake.pods))
	}
	// A stale queued selection must still observe a newly suspended pool under
	// the application lock rather than registering another runner.
	if _, err = server.Store.Pool.Exec(ctx, `UPDATE actions_pools SET config=jsonb_set(config,'{suspended}','true'::jsonb) WHERE application_id=$1 AND service=$2`, pool.ApplicationID, pool.Service); err != nil {
		t.Fatal(err)
	}
	if err = server.reconcileScheduledActions(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if len(fake.runners) != 0 || len(fake.pods) != 0 {
		t.Fatal("selected pool was not reread under lock", len(fake.runners), len(fake.pods))
	}
}

type actionsTestTransport func(*http.Request) (*http.Response, error)

func (f actionsTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Qualification advances logical provider time per dispatch so one credential's
// 1 request/second limit can be exercised over thousands of simulated seconds.
// Real PostgreSQL still owns slots, scheduling order, leases and application locks.
type acceleratedActionsSchedule struct {
	*store.Store
	start   time.Time
	seconds atomic.Int64
}

func (s *acceleratedActionsSchedule) now() time.Time {
	return s.start.Add(time.Duration(s.seconds.Load()) * 250 * time.Millisecond)
}
func (s *acceleratedActionsSchedule) NextActionsPool(ctx context.Context) (*store.ActionsPoolWork, error) {
	s.seconds.Add(1)
	return s.Store.NextActionsPool(ctx)
}
func (s *acceleratedActionsSchedule) CompleteActionsPool(ctx context.Context, w store.ActionsPoolWork, next time.Time, progress bool) error {
	// Preserve due ordering while avoiding a 20-minute wall-clock test. Provider
	// cooldown correctness itself is checked with an unaccelerated lease above.
	return s.Store.CompleteActionsPool(ctx, w, time.Unix(0, 0).Add(next.Sub(s.start)), progress)
}

func TestActionsSchedulerSharedCredentialFairnessAcross100Pools(t *testing.T) {
	db := actionsDatabase(t)
	db.ManagedCloud = true
	db.ActionsAccess = func(context.Context, string, string) error { return nil }
	pools := actionsSchedulePools(t, db, 100)
	fake := &actionsFake{pods: map[string]bool{}}
	for poolIndex, p := range pools {
		for slotIndex := 0; slotIndex < 10; slotIndex++ {
			v, err := db.NewActionsSlot(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			if err = db.UpdateActionsSlot(context.Background(), v.ID, int64(poolIndex*10+slotIndex+1), "online"); err != nil {
				t.Fatal(err)
			}
			fake.pods[v.ID] = true
		}
	}
	schedule := &acceleratedActionsSchedule{Store: db, start: time.Now()}
	if _, err := db.Pool.Exec(context.Background(), `UPDATE actions_slots SET updated_at=now()-interval '3 minutes'`); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: db, actionsTestRuntime: fake, actionsBudget: actions.NewRequestBudget(schedule.now)}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var mu sync.Mutex
	requests, pages, direct := 0, 0, 0
	firstPool := map[string]time.Duration{}
	started := time.Now()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = actionsTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" || r.Method != "GET" {
			return nil, fmt.Errorf("unexpected qualification request")
		}
		mu.Lock()
		requests++
		status := 200
		header := http.Header{}
		body := "{}"
		if requests == 3 {
			status = 429
			header.Set("Retry-After", "60")
		} else if strings.HasSuffix(r.URL.Path, "/runners") {
			pages++
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			runners := []actions.Runner{}
			for id := (page-1)*100 + 1; id <= min(1000, page*100); id++ {
				runners = append(runners, actions.Runner{ID: int64(id), Name: fmt.Sprintf("runner-%d", id), Status: "online"})
			}
			data, _ := json.Marshal(map[string]any{"total_count": 1000, "runners": runners})
			body = string(data)
		} else {
			direct++
			id, err := strconv.ParseInt(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], 10, 64)
			if err != nil {
				mu.Unlock()
				return nil, err
			}
			body = fmt.Sprintf(`{"id":%d,"status":"online","busy":false}`, id)
		}
		mu.Unlock()
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	defer func() { http.DefaultTransport = oldTransport }()
	runActionsWorkersWithClock(ctx, schedule, func(ctx context.Context, p store.ActionsPool) error {
		err := server.reconcileScheduledActions(ctx, p)
		var fresh int
		if e := db.Pool.QueryRow(ctx, `SELECT count(*) FROM actions_slots WHERE application_id=$1 AND updated_at>=$2`, p.ApplicationID, schedule.start).Scan(&fresh); e == nil && fresh == 10 {
			mu.Lock()
			if _, exists := firstPool[p.ApplicationID]; !exists {
				firstPool[p.ApplicationID] = time.Since(started)
			}
			done := len(firstPool) == 100
			mu.Unlock()
			if done {
				cancel()
			}
		}
		return err
	}, schedule.now)
	var heldLocks int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&heldLocks); err != nil || heldLocks != 0 {
		t.Fatal("shutdown leaked application claims", heldLocks, err)
	}
	mu.Lock()
	defer mu.Unlock()
	var freshCount int
	var oldest time.Time
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*),min(updated_at) FROM actions_slots WHERE updated_at>=$1`, schedule.start).Scan(&freshCount, &oldest); err != nil {
		t.Fatal(err)
	}
	if freshCount != 1000 || len(firstPool) != 100 || requests > 100 {
		t.Fatalf("shared credential batching/fairness failed: %d/1000 slots, %d/100 pools, %d requests, logical %s", freshCount, len(firstPool), requests, schedule.now().Sub(schedule.start))
	}
	maxAge := schedule.now().Sub(oldest)
	if maxAge >= 2*time.Minute {
		t.Fatalf("provider observations exceeded readiness freshness: %s", maxAge)
	}
	latencies := make([]time.Duration, 0, len(firstPool))
	for _, latency := range firstPool {
		latencies = append(latencies, latency)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("real PostgreSQL;100 pools/1000 slots/one credential, injected60s429:all fresh; requests=%d pages=%d direct=%d logical=%s max observation age=%s wall=%s first-service p95=%s max=%s", requests, pages, direct, schedule.now().Sub(schedule.start), maxAge, time.Since(started), latencies[94], latencies[99])
}

func TestActionsKnownBudgetExhaustionDoesNotCreateRegistrationIntent(t *testing.T) {
	server, _, pool, _ := actionsHarness(t)
	server.actionsClient = nil
	now := time.Now()
	server.actionsBudget = actions.NewRequestBudget(func() time.Time { return now })
	oldTransport := http.DefaultTransport
	http.DefaultTransport = actionsTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":1}`)), Request: r}, nil
	})
	defer func() { http.DefaultTransport = oldTransport }()
	client, err := server.runnerClient(context.Background(), actionsTarget(pool), pool.Config)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err = client.(interface {
			GetFresh(context.Context, actions.Target, int64) (actions.Runner, error)
		}).GetFresh(context.Background(), pool.Config.Actions.Target(), 1); err != nil {
			t.Fatal(err)
		}
	}
	err = server.reconcileActionsPool(context.Background(), actionsTarget(pool), pool)
	var retry *actions.RetryError
	if !errors.As(err, &retry) {
		t.Fatal("known throttle was not surfaced", err)
	}
	slots, err := server.Store.ActionsSlots(context.Background(), pool.ApplicationID, pool.Service)
	if err != nil || len(slots) != 0 {
		t.Fatal("local throttle manufactured an ambiguous registration", slots, err)
	}
}

func TestActionsCleanupRetryWaitsForNewlyBusyRunner(t *testing.T) {
	server, fake, pool, _ := actionsHarness(t)
	ctx := context.Background()
	if err := server.reconcileActionsPool(ctx, actionsTarget(pool), pool); err != nil {
		t.Fatal(err)
	}
	pool.Config.Suspended = true
	fake.deleteFails = true
	if err := server.reconcileActionsPool(ctx, actionsTarget(pool), pool); err == nil {
		t.Fatal("fixture deletion did not fail")
	}
	runner := fake.runners[1]
	runner.Busy = true
	fake.runners[1] = runner
	fake.deleteFails = false
	if err := server.reconcileActionsPool(ctx, actionsTarget(pool), pool); err != nil {
		t.Fatal(err)
	}
	if len(fake.runners) != 1 || len(fake.pods) != 1 {
		t.Fatal("cleanup retry interrupted a newly assigned job")
	}
	runner.Busy = false
	fake.runners[1] = runner
	if err := server.reconcileActionsPool(ctx, actionsTarget(pool), pool); err != nil {
		t.Fatal(err)
	}
	if len(fake.runners) != 0 || len(fake.pods) != 0 {
		t.Fatal("idle runner did not finish draining")
	}
}

func TestActionsWarmupFillsTenSlotsWithoutTenSecondPauses(t *testing.T) {
	server, fake, pool, _ := actionsHarness(t)
	pool.Config.Replicas = 10
	app := store.Application{ID: pool.ApplicationID, Project: pool.Project, Environment: pool.Environment, Name: pool.ApplicationName}
	if err := server.Store.SyncActions(context.Background(), app, actionsTarget(pool).Spec, pool.Revision); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
	defer cancel()
	started := time.Now()
	ready := false
	runActionsWorkers(ctx, server.Store, func(ctx context.Context, p store.ActionsPool) error {
		err := server.reconcileScheduledActions(ctx, p)
		slots, e := server.Store.ActionsSlots(ctx, p.ApplicationID, p.Service)
		if e == nil && len(slots) == 10 {
			all := true
			for _, slot := range slots {
				all = all && slot.Phase == "online"
			}
			if all {
				ready = true
				cancel()
			}
		}
		return err
	})
	if !ready || len(fake.runners) != 10 {
		t.Fatalf("warmup stalled: ready=%v runners=%d elapsed=%s", ready, len(fake.runners), time.Since(started))
	}
	t.Logf("real PostgreSQL, simulated immediate-ready runtime/provider:10 durable registrations and10 online slots in %s; one registration per pass", time.Since(started))
}

func TestActionsProviderScopeFailuresNeverDeleteRunningPods(t *testing.T) {
	for _, phase := range []string{"online", "starting", "cleanup"} {
		t.Run(phase, func(t *testing.T) {
			server, fake, pool, _ := actionsHarness(t)
			ctx := context.Background()
			if err := server.reconcileActionsPool(ctx, actionsTarget(pool), pool); err != nil {
				t.Fatal(err)
			}
			slots, err := server.Store.ActionsSlots(ctx, pool.ApplicationID, pool.Service)
			if err != nil {
				t.Fatal(err)
			}
			if err = server.Store.UpdateActionsSlot(ctx, slots[0].ID, 1, phase); err != nil {
				t.Fatal(err)
			}
			server.actionsClient = nil
			deletes := 0
			old := http.DefaultTransport
			http.DefaultTransport = actionsTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodDelete {
					deletes++
				}
				return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			})
			defer func() { http.DefaultTransport = old }()
			if err = server.reconcileActionsPool(ctx, actionsTarget(pool), pool); err == nil {
				t.Fatal("provider scope failure disappeared")
			}
			if len(fake.pods) != 1 || deletes != 0 {
				t.Fatal("inaccessible scope caused destructive runner cleanup", len(fake.pods), deletes)
			}
		})
	}
}

func TestActionsProviderBusyConflictRetainsPod(t *testing.T) {
	server, fake, pool, _ := actionsHarness(t)
	ctx := context.Background()
	if err := server.reconcileActionsPool(ctx, actionsTarget(pool), pool); err != nil {
		t.Fatal(err)
	}
	pool.Config.Suspended = true
	server.actionsClient = nil
	old := http.DefaultTransport
	http.DefaultTransport = actionsTestTransport(func(r *http.Request) (*http.Response, error) {
		status := 200
		body := `{"id":1,"status":"online","busy":false}`
		if r.Method == http.MethodDelete {
			status = 422
			body = ""
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	defer func() { http.DefaultTransport = old }()
	if err := server.reconcileActionsPool(ctx, actionsTarget(pool), pool); err == nil {
		t.Fatal("provider busy conflict disappeared")
	}
	if len(fake.pods) != 1 {
		t.Fatal("provider422 removed running pod")
	}
}

func TestActionsCleanupContradictoryIdentityWaitsForDirectRecovery(t *testing.T) {
	server, fake, pool, _ := actionsHarness(t)
	ctx := context.Background()
	if err := server.reconcileActionsPool(ctx, actionsTarget(pool), pool); err != nil {
		t.Fatal(err)
	}
	slots, err := server.Store.ActionsSlots(ctx, pool.ApplicationID, pool.Service)
	if err != nil || len(slots) != 1 {
		t.Fatal(slots, err)
	}
	pool.Config.Suspended = true
	server.actionsClient = nil
	now := time.Now()
	server.actionsBudget = actions.NewRequestBudget(func() time.Time { return now })
	contradictory, requests, deletes := false, 0, 0
	old := http.DefaultTransport
	http.DefaultTransport = actionsTestTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		status := 200
		body := fmt.Sprintf(`{"id":1,"name":"hakopod-%s","status":"online","busy":false}`, slots[0].ID)
		if r.Method == http.MethodDelete {
			deletes++
			status = 204
			body = ""
		} else if r.URL.Query().Get("name") != "" {
			body = fmt.Sprintf(`{"total_count":1,"runners":[%s]}`, body)
		} else if contradictory {
			status = 404
			body = ""
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	defer func() { http.DefaultTransport = old }()
	client, err := server.actionsProvider(ctx, actionsTarget(pool), pool.Config.Actions.Credential)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err = client.GetFresh(ctx, pool.Config.Actions.Target(), 1); err != nil {
			t.Fatal(err)
		}
	}
	contradictory = true
	if err = server.reconcileActionsPool(ctx, actionsTarget(pool), pool); err == nil || errors.Is(err, actions.ErrRunnerAbsent) {
		t.Fatal("contradictory404 did not preserve uncertain identity", err)
	}
	if deletes != 0 || requests != 10 || len(fake.pods) != 1 {
		t.Fatal("contradictory identity attempted retirement", deletes, requests, len(fake.pods))
	}
	contradictory = false
	now = now.Add(2 * time.Second)
	if err = server.reconcileActionsPool(ctx, actionsTarget(pool), pool); err != nil {
		t.Fatal("consistent direct identity did not resume cleanup", err)
	}
	if deletes != 1 || requests != 12 || len(fake.pods) != 0 {
		t.Fatal("cleanup failed to complete within two replenished credits", deletes, requests, len(fake.pods))
	}
}
