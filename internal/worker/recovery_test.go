package worker

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"k8s.io/client-go/tools/clientcmd"
)

func workerDatabase(t *testing.T) (*store.Store, store.Principal) {
	t.Helper()
	dsn := os.Getenv("HAKOPOD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "hakopod_worker_" + store.NewID()
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
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
		bounded, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, _ = conn.Exec(bounded, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		conn.Close(bounded)
	})
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	token, err := db.Bootstrap(ctx, "worker-fixture")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	return db, p
}

type fixtureRuntime struct {
	*cluster.Client
	calls []spec.Application
	fail  bool
}

func (f *fixtureRuntime) Deploy(ctx context.Context, target cluster.Target, emit func(cluster.Event)) (cluster.Observation, error) {
	if err := target.BeforeStep(ctx); err != nil {
		return cluster.Observation{}, err
	}
	f.calls = append(f.calls, target.Spec)
	o := cluster.Observation{Revision: target.Revision, ObservedAt: time.Now(), Status: "healthy", Services: []cluster.ServiceStatus{}}
	if f.fail && target.Spec.Services["web"].Env["REVISION"] == "bad" {
		o.Status = "failed"
		return o, errors.New("readiness failed")
	}
	return o, nil
}
func TestWorkerRecoveryAndRestartDoNotRetryFailedRelease(t *testing.T) {
	db, p := workerDatabase(t)
	ctx := context.Background()
	app, _ := spec.Normalize(spec.Application{Name: "recovery", Services: map[string]spec.Service{"web": {Image: "python@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Env: map[string]string{"REVISION": "good"}}}})
	runtime := &fixtureRuntime{fail: true}
	w := Worker{Store: db, Cluster: runtime, Timeout: time.Minute}
	deploy := func(a spec.Application, rev int64, key string) store.Deployment {
		d, err := db.Accept(ctx, p, "demo", "development", a, rev, key, a)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := db.Claim(ctx)
		if err != nil || claim == nil {
			t.Fatal(err)
		}
		w.run(ctx, claim)
		result, err := db.Deployment(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := deploy(app, 0, "recovery-first")
	if first.Status != "succeeded" {
		t.Fatal(first)
	}
	bad, _ := spec.Normalize(app)
	s := bad.Services["web"]
	s.Env["REVISION"] = "bad"
	bad.Services["web"] = s
	result := deploy(bad, 1, "recovery-second")
	if result.Status != "failed" || result.RecoveryState != "succeeded" || len(runtime.calls) != 3 {
		t.Fatal("recovery not completed", result, len(runtime.calls))
	}
	// Stop the process after durable recovery starts, then reclaim it.
	d, err := db.Accept(ctx, p, "demo", "development", bad, 2, "recovery-restart", bad)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if err = claim.BeginRecovery(ctx, &first, "readiness failed", ""); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	runtime.calls = nil
	claim, err = db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	w.run(ctx, claim)
	result, err = db.Deployment(ctx, d.ID)
	if err != nil || result.RecoveryState != "succeeded" || len(runtime.calls) != 1 || runtime.calls[0].Services["web"].Env["REVISION"] != "good" {
		t.Fatal("restart retried failed release", result, err)
	}
	for index, interrupt := range []string{"cancel", "revoke"} {
		d, err = db.Accept(ctx, p, "demo", "development", bad, int64(3+index), "recovery-"+interrupt, bad)
		if err != nil {
			t.Fatal(err)
		}
		claim, err = db.Claim(ctx)
		if err != nil || claim == nil {
			t.Fatal(err)
		}
		if err = claim.BeginRecovery(ctx, &first, "readiness failed", ""); err != nil {
			t.Fatal(err)
		}
		claim.Release()
		if interrupt == "cancel" {
			_, err = db.Pool.Exec(ctx, "UPDATE deployments SET cancel_requested=true WHERE id=$1", d.ID)
		} else {
			_, err = db.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", p.KeyID)
		}
		if err != nil {
			t.Fatal(err)
		}
		claim, err = db.Claim(ctx)
		if err != nil || claim == nil {
			t.Fatal(err)
		}
		runtime.calls = nil
		w.run(ctx, claim)
		result, err = db.Deployment(ctx, d.ID)
		if err != nil || result.Status != "cancelled" || result.RecoveryState != "failed" || len(runtime.calls) != 0 {
			t.Fatal("interrupted recovery did not stop", interrupt, result, err)
		}
	}
}
func TestLiveWorkerReleaseRecovery(t *testing.T) {
	if os.Getenv("HAKOPOD_RECOVERY_TEST") != "1" {
		t.Skip("opt-in real release recovery acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires k3d-hakopod-dev")
	}
	db, p := workerDatabase(t)
	c, err := cluster.New(path, cluster.Options{RolloutTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w := Worker{Store: db, Cluster: c, Timeout: 2 * time.Minute}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	sample, _ := spec.Showcase()
	app, _ := spec.Normalize(spec.Application{Name: "recovery-live", Services: map[string]spec.Service{"web": {Image: sample.Services["api"].Image, Port: 8080, Command: []string{"python", "-m", "http.server", "8080"}}}})
	first, err := db.Accept(ctx, p, "demo", "development", app, 0, "live-recovery-first", app)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		_ = c.DeletePreview(clean, cluster.Target{ApplicationID: first.ApplicationID, Project: "demo", Environment: "development", Spec: app})
	}()
	run := func(id string) store.Deployment {
		claim, e := db.Claim(ctx)
		if e != nil || claim == nil {
			t.Fatal(e)
		}
		w.run(ctx, claim)
		d, e := db.Deployment(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	if d := run(first.ID); d.Status != "succeeded" {
		t.Fatal(d.Error)
	}
	bad, _ := spec.Normalize(app)
	s := bad.Services["web"]
	s.Command = []string{"python", "-c", "raise SystemExit(1)"}
	bad.Services["web"] = s
	next, err := db.Accept(ctx, p, "demo", "development", bad, 1, "live-recovery-bad", bad)
	if err != nil {
		t.Fatal(err)
	}
	result := run(next.ID)
	if result.Status != "failed" || result.RecoveryState != "succeeded" {
		t.Fatal(result.Error, result.RecoveryError)
	}
	observed, err := c.Observe(ctx, cluster.Target{ApplicationID: first.ApplicationID, Revision: 2, Project: "demo", Environment: "development", Spec: app})
	if err != nil || observed.Status != "healthy" {
		t.Fatal(observed, err)
	}
	t.Log("failed revision remained failed; prior digest-pinned workload is healthy")
}
