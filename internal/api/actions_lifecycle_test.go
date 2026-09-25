package api

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"net/url"
	"os"
	"testing"
	"time"
)

// Provider and pod failures are deterministic here; state and advisory locks
// use an isolated real PostgreSQL database, never an in-memory store.
func actionsDatabase(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("HAKOPOD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "hakopod_actions_test_" + store.NewID()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		admin.Close(cleanup)
	})
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

type actionsFake struct {
	runners                             map[int64]actions.Runner
	pods, configs                       map[string]bool
	next                                int64
	registerLost, saveLost, deleteFails bool
}

func (f *actionsFake) Register(_ context.Context, _ string, name string, _ []string) (actions.Registration, error) {
	f.next++
	r := actions.Runner{ID: f.next, Name: name, Status: "online"}
	f.runners[r.ID] = r
	if f.registerLost {
		f.registerLost = false
		return actions.Registration{}, errors.New("response lost")
	}
	return actions.Registration{Runner: r, EncodedConfig: "one-job-only"}, nil
}
func (f *actionsFake) Get(_ context.Context, _ string, id int64) (actions.Runner, error) {
	r, ok := f.runners[id]
	if !ok {
		return r, &actions.StatusError{Status: 404}
	}
	return r, nil
}
func (f *actionsFake) Find(_ context.Context, _ string, name string) (*actions.Runner, error) {
	for _, r := range f.runners {
		if r.Name == name {
			return &r, nil
		}
	}
	return nil, nil
}
func (f *actionsFake) Delete(_ context.Context, _ string, id int64) error {
	if f.deleteFails {
		return &actions.StatusError{Status: 503}
	}
	delete(f.runners, id)
	return nil
}
func (f *actionsFake) ActionsAvailable(context.Context) error { return nil }
func (f *actionsFake) ActionsCredential(context.Context, cluster.Target, string) (string, error) {
	return "control-plane-fixture-only", nil
}
func (f *actionsFake) ActionsPodPhase(_ context.Context, _ cluster.Target, id string) (string, error) {
	if f.pods[id] {
		return "Running", nil
	}
	return "missing", nil
}
func (f *actionsFake) DeleteActionsPod(_ context.Context, _ cluster.Target, id string) (bool, error) {
	delete(f.pods, id)
	return true, nil
}
func (f *actionsFake) DeleteActionsConfig(_ context.Context, _ cluster.Target, id string) error {
	delete(f.configs, id)
	return nil
}
func (f *actionsFake) SaveActionsConfig(_ context.Context, _ cluster.Target, _ string, id, _ string) error {
	if f.saveLost {
		f.saveLost = false
		return errors.New("save failed")
	}
	f.configs[id] = true
	return nil
}
func (f *actionsFake) StartActionsPod(_ context.Context, _ cluster.Target, _ string, id string, _ spec.Service) error {
	if !f.configs[id] {
		return errors.New("missing JIT configuration")
	}
	f.pods[id] = true
	return nil
}
func (f *actionsFake) HasActionsConfig(_ context.Context, _ cluster.Target, id string) (bool, error) {
	return f.configs[id], nil
}
func actionsHarness(t *testing.T) (*Server, *actionsFake, store.ActionsPool, *bool) {
	db := actionsDatabase(t)
	allowed := true
	db.ManagedCloud = true
	db.ActionsAccess = func(context.Context, string, string) error {
		if !allowed {
			return store.ErrLicenseRequired
		}
		return nil
	}
	a, err := spec.Normalize(spec.Application{Name: "runners", Services: map[string]spec.Service{"runner": {Actions: &spec.Actions{Repository: "team/repo", Credential: "token"}}}})
	if err != nil {
		t.Fatal(err)
	}
	app := store.Application{ID: store.NewID(), Project: "tenant-a", Environment: "production", Name: "runners"}
	if err = db.SyncActions(context.Background(), app, a, 1); err != nil {
		t.Fatal(err)
	}
	pools, err := db.ActionsPools(context.Background(), app.ID)
	if err != nil {
		t.Fatal(err)
	}
	fake := &actionsFake{runners: map[int64]actions.Runner{}, pods: map[string]bool{}, configs: map[string]bool{}}
	return &Server{Store: db, actionsTestRuntime: fake, actionsClient: func(string) (runnerProvider, error) { return fake, nil }}, fake, pools[0], &allowed
}
func TestActionsDrainingExpiryAndRemovalRetainCleanup(t *testing.T) {
	s, f, p, allowed := actionsHarness(t)
	ctx := context.Background()
	target := actionsTarget(p)
	reconcile := func() {
		t.Helper()
		if e := s.reconcileActionsPool(ctx, target, p); e != nil {
			t.Fatal(e)
		}
	}
	reconcile()
	if len(f.runners) != 1 || len(f.pods) != 1 {
		t.Fatal("pool not created")
	}
	r := f.runners[1]
	r.Busy = true
	f.runners[1] = r
	*allowed = false
	reconcile()
	if len(f.runners) != 1 || f.next != 1 {
		t.Fatal("busy runner replaced after grant expiry")
	}
	r.Busy = false
	f.runners[1] = r
	reconcile()
	if len(f.runners) != 0 || len(f.pods) != 0 {
		t.Fatal("expired pool did not drain")
	}
	*allowed = true
	reconcile()
	p.Removed = true
	f.deleteFails = true
	if s.reconcileActionsPool(ctx, target, p) == nil {
		t.Fatal("provider cleanup failure hidden")
	}
	slots, e := s.Store.ActionsSlots(ctx, p.ApplicationID, p.Service)
	if e != nil || len(slots) != 1 || slots[0].Phase != "cleanup" {
		t.Fatal("cleanup intent lost", e)
	}
	f.deleteFails = false
	reconcile()
	slots, e = s.Store.ActionsSlots(ctx, p.ApplicationID, p.Service)
	if e != nil || len(slots) != 0 || len(f.runners) != 0 || len(f.configs) != 0 {
		t.Fatal("cleanup leaked resources", e)
	}
}
func TestActionsInterruptedRegistrationAndMissingJITRecover(t *testing.T) {
	for _, failure := range []string{"provider-response", "JIT-save"} {
		t.Run(failure, func(t *testing.T) {
			s, f, p, _ := actionsHarness(t)
			ctx := context.Background()
			if failure == "provider-response" {
				f.registerLost = true
			} else {
				f.saveLost = true
			}
			if s.reconcileActionsPool(ctx, actionsTarget(p), p) == nil {
				t.Fatal("injected failure ignored")
			}
			if len(f.runners) != 1 {
				t.Fatal("fixture did not persist remote registration")
			}
			for i := 0; i < 3; i++ {
				if e := s.reconcileActionsPool(ctx, actionsTarget(p), p); e != nil {
					t.Fatal(e)
				}
			}
			slots, e := s.Store.ActionsSlots(ctx, p.ApplicationID, p.Service)
			if e != nil || len(slots) != 1 || len(f.runners) != 1 || len(f.pods) != 1 || f.next != 2 {
				t.Fatal("restart recovery duplicated or stranded registration", e, slots)
			}
		})
	}
}
func TestActionsRevisionDrainCountsAgainstCapacity(t *testing.T) {
	s, f, p, _ := actionsHarness(t)
	ctx := context.Background()
	if err := s.reconcileActionsPool(ctx, actionsTarget(p), p); err != nil {
		t.Fatal(err)
	}
	r := f.runners[1]
	r.Busy = true
	f.runners[1] = r
	p.Config.RestartNonce = "new-revision"
	if err := s.reconcileActionsPool(ctx, actionsTarget(p), p); err != nil {
		t.Fatal(err)
	}
	if f.next != 1 {
		t.Fatal("busy old revision not counted against capacity")
	}
	r.Busy = false
	f.runners[1] = r
	if err := s.reconcileActionsPool(ctx, actionsTarget(p), p); err != nil {
		t.Fatal(err)
	}
	if f.next != 2 || len(f.runners) != 1 {
		t.Fatal("drained revision not replaced")
	}
}
func TestActionsLostClaimNeverRegisters(t *testing.T) {
	s, f, p, _ := actionsHarness(t)
	ctx := context.Background()
	claim, err := s.Store.ClaimActions(ctx, p.ApplicationID)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	other, e := s.Store.ClaimActions(ctx, p.ApplicationID)
	if e != nil || other != nil {
		t.Fatal("concurrent controller acquired lock", e)
	}
	target := actionsTarget(p)
	target.BeforeStep = claim.Check
	claim.Release()
	if err = s.reconcileActionsPool(ctx, target, p); !errors.Is(err, store.ErrClaimLost) || f.next != 0 {
		t.Fatal("lost lease allowed registration", err)
	}
	pools, err := s.Store.ActionsPools(ctx, "other-tenant")
	if err != nil || len(pools) != 0 {
		t.Fatal("cross-tenant inventory leaked", err)
	}
}
