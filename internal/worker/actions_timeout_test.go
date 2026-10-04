package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

type drainSourceFixture struct {
	base      store.Deployment
	slots     map[string][]store.ActionsSlot
	baseError error
	slotError error
	reads     int
}

func (f *drainSourceFixture) RuntimeBase(_ context.Context, application string, revision int64) (store.Deployment, error) {
	f.reads++
	if application != "fixture" || revision != 1 {
		return store.Deployment{}, errors.New("wrong immutable base")
	}
	return f.base, f.baseError
}

func (f *drainSourceFixture) ActionsSlots(_ context.Context, application, service string) ([]store.ActionsSlot, error) {
	f.reads++
	if application != "fixture" {
		return nil, errors.New("wrong slot scope")
	}
	return f.slots[service], f.slotError
}

func drainApplication(t *testing.T) spec.Application {
	t.Helper()
	app, err := spec.Normalize(spec.Application{Name: "drain", Services: map[string]spec.Service{
		"runner": {Actions: &spec.Actions{Repository: "team/repo", Credential: "token", TimeoutMinutes: 5}},
		"other":  {Actions: &spec.Actions{Repository: "team/another", Credential: "token", TimeoutMinutes: 5}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func copyDrainApplication(t *testing.T, app spec.Application) spec.Application {
	t.Helper()
	copy, err := spec.Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	return copy
}

func setDrainSuspended(app *spec.Application, name string, suspended bool) {
	service := app.Services[name]
	service.Suspended = suspended
	app.Services[name] = service
}

func drainDeployments(t *testing.T) (store.Deployment, store.Deployment) {
	t.Helper()
	before := drainApplication(t)
	after := copyDrainApplication(t, before)
	setDrainSuspended(&after, "runner", true)
	beforeResolved := copyDrainApplication(t, before)
	afterResolved := copyDrainApplication(t, after)
	return store.Deployment{ApplicationID: "fixture", Revision: 1, Status: "succeeded", Spec: before, ResolvedSpec: &beforeResolved},
		store.Deployment{ApplicationID: "fixture", Revision: 2, Spec: after, ResolvedSpec: &afterResolved}
}

func TestActionsDrainAllowanceRequiresPureImmutableSuspension(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*store.Deployment, *store.Deployment)
		want   time.Duration
	}{
		{name: "pure stop with another active pool", want: 5*time.Minute + 30*time.Second},
		{name: "failed resolved predecessor", change: func(base, _ *store.Deployment) { base.Status = "failed" }, want: 5*time.Minute + 30*time.Second},
		{name: "successful recovery predecessor", change: func(base, _ *store.Deployment) {
			recovered := copyDrainApplication(t, *base.ResolvedSpec)
			base.Status, base.RecoveryState, base.RecoverySpec = "failed", "succeeded", &recovered
			base.Spec.Env = map[string]string{"FAILED": "revision"}
			base.ResolvedSpec.Env = map[string]string{"FAILED": "revision"}
		}, want: 5*time.Minute + 30*time.Second},
		{name: "already suspended", change: func(base, _ *store.Deployment) {
			setDrainSuspended(&base.Spec, "runner", true)
			setDrainSuspended(base.ResolvedSpec, "runner", true)
		}},
		{name: "another pool changed", change: func(_, next *store.Deployment) {
			service := next.Spec.Services["other"]
			service.Replicas++
			next.Spec.Services["other"] = service
		}},
		{name: "resolved image change", change: func(_, next *store.Deployment) {
			service := next.ResolvedSpec.Services["other"]
			parts := strings.SplitN(service.Image, "@", 2)
			service.Image = strings.SplitN(parts[0], ":", 2)[0] + "@" + parts[1]
			next.ResolvedSpec.Services["other"] = service
		}},
		{name: "timeout change", change: func(_, next *store.Deployment) { next.Spec.Services["runner"].Actions.TimeoutMinutes = 6 }},
		{name: "replica change", change: func(_, next *store.Deployment) {
			service := next.Spec.Services["runner"]
			service.Replicas++
			next.Spec.Services["runner"] = service
		}},
		{name: "application change", change: func(_, next *store.Deployment) { next.Spec.Env = map[string]string{"NEW": "value"} }},
		{name: "missing resolved input", change: func(_, next *store.Deployment) { next.ResolvedSpec = nil }},
		{name: "already cancelled", change: func(_, next *store.Deployment) { next.CancelRequested = true }},
		{name: "different immutable revision", change: func(base, _ *store.Deployment) { base.Revision = 0 }},
		{name: "different immutable application", change: func(base, _ *store.Deployment) { base.ApplicationID = "another" }},
		{name: "cancelled predecessor", change: func(base, _ *store.Deployment) { base.Status = "cancelled" }},
		{name: "resumed recovery", change: func(_, next *store.Deployment) { next.RecoveryState = "running" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, next := drainDeployments(t)
			if test.change != nil {
				test.change(&base, &next)
			}
			source := &drainSourceFixture{base: base}
			got, err := actionsDrainAllowance(context.Background(), source, next)
			if err != nil || got != test.want {
				t.Fatalf("allowance = %v, %v; want %v", got, err, test.want)
			}
			if !actionsDrainCandidate(next) && source.reads != 0 {
				t.Fatal("ordinary deployment read Actions drain metadata")
			}
		})
	}
}

func TestActionsDrainAllowanceUsesAllRetainedLifetimes(t *testing.T) {
	base, next := drainDeployments(t)
	older := copyDrainApplication(t, base.Spec).Services["runner"]
	older.Actions.TimeoutMinutes = 120
	source := &drainSourceFixture{base: base, slots: map[string][]store.ActionsSlot{
		"runner": {{ApplicationID: "fixture", Service: "runner", Config: older}},
	}}
	got, err := actionsDrainAllowance(context.Background(), source, next)
	if err != nil || got != 120*time.Minute+30*time.Second {
		t.Fatal("old busy runner timeout was shortened", got, err)
	}
	// A second suspended pool does not trigger an allowance itself, but it must
	// finish too when the same application is now stopping another pool.
	second := copyDrainApplication(t, base.Spec).Services["runner"]
	second.Suspended, second.Actions.TimeoutMinutes = true, 180
	for _, app := range []*spec.Application{&base.Spec, base.ResolvedSpec, &next.Spec, next.ResolvedSpec} {
		app.Services["second"] = second
	}
	source.base = base
	got, err = actionsDrainAllowance(context.Background(), source, next)
	if err != nil || got != 180*time.Minute+30*time.Second {
		t.Fatal("application drain did not include the other suspended pool", got, err)
	}
	for _, invalid := range []spec.Service{
		{Actions: &spec.Actions{Provider: actions.ProviderBitbucket, TimeoutMinutes: 360}},
		{Actions: &spec.Actions{TimeoutMinutes: 361}},
		{},
	} {
		source.slots["runner"][0].Config = invalid
		got, err = actionsDrainAllowance(context.Background(), source, next)
		if err != nil || got != 0 {
			t.Fatal("invalid retained lifetime granted an extension", got, err)
		}
	}
}

func TestActionsDrainLifetimeBounds(t *testing.T) {
	for _, test := range []struct {
		provider actions.Provider
		minutes  int64
		want     time.Duration
	}{
		{minutes: 0, want: 60*time.Minute + 30*time.Second},
		{minutes: 5, want: 5*time.Minute + 30*time.Second},
		{minutes: 360, want: 360*time.Minute + 30*time.Second},
		{provider: actions.ProviderGitLab, minutes: 360, want: 364 * time.Minute},
		{provider: actions.ProviderBitbucket, minutes: 360},
		{provider: "unknown", minutes: 60},
		{minutes: -1}, {minutes: 4}, {minutes: 361}, {minutes: 1<<63 - 1},
	} {
		got, ok := actionsDrainLifetime(spec.Service{Actions: &spec.Actions{Provider: test.provider, TimeoutMinutes: test.minutes}})
		if got != test.want || ok != (test.want != 0) {
			t.Fatalf("provider %q timeout %d: got %v/%v, want %v", test.provider, test.minutes, got, ok, test.want)
		}
	}
}

func TestActionsDrainMetadataFailureDoesNotGrantTime(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*drainSourceFixture)
		failed bool
	}{
		{name: "missing predecessor", change: func(f *drainSourceFixture) { f.baseError = pgx.ErrNoRows }},
		{name: "predecessor read failed", change: func(f *drainSourceFixture) { f.baseError = errors.New("database unavailable") }, failed: true},
		{name: "slot read failed", change: func(f *drainSourceFixture) { f.slotError = errors.New("database unavailable") }, failed: true},
		{name: "slot bound exceeded", change: func(f *drainSourceFixture) {
			f.slots = map[string][]store.ActionsSlot{"runner": make([]store.ActionsSlot, 21)}
		}, failed: true},
		{name: "foreign slot", change: func(f *drainSourceFixture) {
			f.slots = map[string][]store.ActionsSlot{"runner": {{ApplicationID: "another", Service: "runner"}}}
		}, failed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, next := drainDeployments(t)
			source := &drainSourceFixture{base: base}
			test.change(source)
			got, err := actionsDrainAllowance(context.Background(), source, next)
			if got != 0 || (err != nil) != test.failed {
				t.Fatal("failed metadata granted time", got, err)
			}
		})
	}
}

type drainRuntimeFixture struct {
	*cluster.Client
	deploy func(context.Context, cluster.Target) (cluster.Observation, error)
}

func (f *drainRuntimeFixture) Deploy(ctx context.Context, target cluster.Target, _ func(cluster.Event)) (cluster.Observation, error) {
	if err := target.BeforeStep(ctx); err != nil {
		return cluster.Observation{}, err
	}
	if f.deploy != nil {
		return f.deploy(ctx, target)
	}
	return cluster.Observation{Status: "healthy", Revision: target.Revision}, nil
}

func workerDrainFixture(t *testing.T) (*Worker, *drainRuntimeFixture, store.Principal, store.Deployment) {
	t.Helper()
	return workerDeadlineFixture(t, drainApplication(t))
}

func workerDeadlineFixture(t *testing.T, app spec.Application) (*Worker, *drainRuntimeFixture, store.Principal, store.Deployment) {
	t.Helper()
	db, principal := workerDatabase(t)
	db.ManagedCloud = true
	db.ActionsAccess = func(context.Context, string, string) error { return nil }
	db.ValidateDeployment = func(context.Context, store.Application, spec.Application) error { return nil }
	runtime := &drainRuntimeFixture{}
	w := &Worker{Store: db, Cluster: runtime, Timeout: 2 * time.Second}
	first, err := db.Accept(context.Background(), principal, "demo", "development", app, 0, "drain-first", app)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.Claim(context.Background())
	if err != nil || claim == nil {
		t.Fatal("initial claim", err)
	}
	w.run(context.Background(), claim)
	first, err = db.Deployment(context.Background(), first.ID)
	if err != nil || first.Status != "succeeded" {
		t.Fatal("initial deployment", first, err)
	}
	return w, runtime, principal, first
}

func acceptWorkerDrain(t *testing.T, w *Worker, principal store.Principal, first store.Deployment, ordinaryChange bool) (store.Deployment, *store.Claim) {
	t.Helper()
	next := copyDrainApplication(t, first.Spec)
	setDrainSuspended(&next, "runner", true)
	if ordinaryChange {
		service := next.Services["other"]
		service.Replicas++
		next.Services["other"] = service
	}
	d, err := w.Store.Accept(context.Background(), principal, "demo", "development", next, first.Revision, "drain-second", next)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := w.Store.Claim(context.Background())
	if err != nil || claim == nil {
		t.Fatal("stop claim", err)
	}
	return d, claim
}

func TestWorkerActionsStopOutlivesOrdinaryDeadline(t *testing.T) {
	w, runtime, principal, first := workerDrainFixture(t)
	d, claim := acceptWorkerDrain(t, w, principal, first, false)
	var budget time.Duration
	runtime.deploy = func(ctx context.Context, target cluster.Target) (cluster.Observation, error) {
		deadline, _ := ctx.Deadline()
		budget = time.Until(deadline)
		timer := time.NewTimer(w.Timeout + 100*time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return cluster.Observation{}, ctx.Err()
		case <-timer.C:
		}
		return cluster.Observation{Status: "healthy"}, target.BeforeStep(ctx)
	}
	w.run(context.Background(), claim)
	result, err := w.Store.Deployment(context.Background(), d.ID)
	if err != nil || result.Status != "succeeded" || budget < 5*time.Minute {
		t.Fatal("busy stop was truncated by ordinary deadline", result, budget, err)
	}
}

func TestWorkerActionsStopDoesNotExtendOtherChangesOrRecovery(t *testing.T) {
	for _, ordinaryChange := range []bool{true, false} {
		name := "recovery"
		if ordinaryChange {
			name = "unrelated change"
		}
		t.Run(name, func(t *testing.T) {
			w, runtime, principal, first := workerDrainFixture(t)
			d, claim := acceptWorkerDrain(t, w, principal, first, ordinaryChange)
			var budgets []time.Duration
			runtime.deploy = func(ctx context.Context, target cluster.Target) (cluster.Observation, error) {
				deadline, _ := ctx.Deadline()
				budgets = append(budgets, time.Until(deadline))
				if len(budgets) == 1 && !ordinaryChange {
					return cluster.Observation{}, errors.New("provider unavailable")
				}
				if len(budgets) == 2 && ordinaryChange {
					return cluster.Observation{Status: "healthy"}, nil
				}
				<-ctx.Done()
				return cluster.Observation{}, ctx.Err()
			}
			w.run(context.Background(), claim)
			result, err := w.Store.Deployment(context.Background(), d.ID)
			if err != nil || result.Status != "failed" || len(budgets) != 2 || budgets[1] > w.Timeout/2 {
				t.Fatal("ordinary recovery cap changed", result, budgets, err)
			}
			if ordinaryChange && (budgets[0] > w.Timeout*2/3 || result.RecoveryState != "succeeded") {
				t.Fatal("unrelated change received drain time", result, budgets)
			}
			if !ordinaryChange && result.RecoveryState != "failed" {
				t.Fatal("recovery did not stop at its original timeout", result)
			}
		})
	}
}

func TestWorkerOrdinaryDeploymentKeepsDeadline(t *testing.T) {
	app, err := spec.Normalize(spec.Application{Name: "ordinary", Services: map[string]spec.Service{
		"web": {Image: "python@sha256:" + strings.Repeat("a", 64)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	w, runtime, principal, first := workerDeadlineFixture(t, app)
	next := copyDrainApplication(t, first.Spec)
	service := next.Services["web"]
	service.Env = map[string]string{"RELEASE": "next"}
	next.Services["web"] = service
	d, err := w.Store.Accept(context.Background(), principal, "demo", "development", next, first.Revision, "ordinary-next", next)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := w.Store.Claim(context.Background())
	if err != nil || claim == nil {
		t.Fatal("ordinary claim", err)
	}
	var budgets []time.Duration
	runtime.deploy = func(ctx context.Context, _ cluster.Target) (cluster.Observation, error) {
		deadline, _ := ctx.Deadline()
		budgets = append(budgets, time.Until(deadline))
		if len(budgets) == 1 {
			<-ctx.Done()
			return cluster.Observation{}, ctx.Err()
		}
		return cluster.Observation{Status: "healthy"}, nil
	}
	w.run(context.Background(), claim)
	result, err := w.Store.Deployment(context.Background(), d.ID)
	if err != nil || result.Status != "failed" || result.RecoveryState != "succeeded" || len(budgets) != 2 || budgets[0] > w.Timeout*2/3 || budgets[1] > w.Timeout/2 {
		t.Fatal("ordinary deployment budgets changed", result, budgets, err)
	}
}

func TestWorkerActionsStopPreservesInterruptionFences(t *testing.T) {
	for _, interruption := range []string{"cancel", "revoke", "parent", "claim"} {
		t.Run(interruption, func(t *testing.T) {
			w, runtime, principal, first := workerDrainFixture(t)
			d, claim := acceptWorkerDrain(t, w, principal, first, false)
			entered, done := make(chan struct{}), make(chan struct{})
			runtime.deploy = func(ctx context.Context, _ cluster.Target) (cluster.Observation, error) {
				close(entered)
				<-ctx.Done()
				return cluster.Observation{}, ctx.Err()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { defer close(done); w.run(ctx, claim) }()
			select {
			case <-entered:
			case <-done:
				t.Fatal("stop did not begin")
			case <-time.After(5 * time.Second):
				t.Fatal("stop did not begin within its test bound")
			}
			var err error
			switch interruption {
			case "cancel":
				_, err = w.Store.Pool.Exec(context.Background(), "UPDATE deployments SET cancel_requested=true WHERE id=$1", d.ID)
			case "revoke":
				_, err = w.Store.Pool.Exec(context.Background(), "UPDATE api_keys SET revoked_at=now() WHERE id=$1", principal.KeyID)
			case "parent":
				cancel()
			case "claim":
				claim.Release()
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("stop ignored its interruption fence")
			}
			result, err := w.Store.Deployment(context.Background(), d.ID)
			want := "cancelled"
			if interruption == "parent" || interruption == "claim" {
				want = "running"
			}
			if err != nil || result.Status != want {
				t.Fatal("wrong interrupted state", result, err)
			}
			if want == "running" {
				runtime.deploy = nil
				reclaimed, err := w.Store.Claim(context.Background())
				if err != nil || reclaimed == nil {
					t.Fatal("interrupted stop cannot be reclaimed", err)
				}
				w.run(context.Background(), reclaimed)
				result, err = w.Store.Deployment(context.Background(), d.ID)
				if err != nil || result.Status != "succeeded" {
					t.Fatal("reclaimed stop did not finish", result, err)
				}
			}
		})
	}
}

func TestWorkerActionsStopBoundsMetadataPreparation(t *testing.T) {
	w, runtime, principal, first := workerDrainFixture(t)
	w.Timeout = 15 * time.Second
	d, claim := acceptWorkerDrain(t, w, principal, first, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lock, err := w.Store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	// This isolated database lock blocks RuntimeBase without blocking the
	// credential check, proving the added prelude has its own short ceiling.
	if _, err = lock.Exec(ctx, "LOCK TABLE deployments IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	deployed := make(chan struct{}, 1)
	runtime.deploy = func(context.Context, cluster.Target) (cluster.Observation, error) {
		deployed <- struct{}{}
		return cluster.Observation{Status: "healthy"}, nil
	}
	done := make(chan struct{})
	go func() { defer close(done); w.run(ctx, claim) }()
	select {
	case <-done:
	case <-deployed:
		t.Fatal("deployment began without immutable stop metadata")
	case <-time.After(actionsDrainPreparationTimeout + 2*time.Second):
		t.Fatal("metadata preparation used the full operation timeout")
	}
	if err = lock.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := w.Store.Deployment(context.Background(), d.ID)
	if err != nil || result.Status != "running" {
		t.Fatal("metadata timeout did not leave stop resumable", result, err)
	}
}
