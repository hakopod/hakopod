package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
)

type preApplyRuntime struct {
	*cluster.Client
	calls int
}

func (f *preApplyRuntime) Deploy(ctx context.Context, target cluster.Target, emit func(cluster.Event)) (cluster.Observation, error) {
	if err := target.BeforeStep(ctx); err != nil {
		return cluster.Observation{}, err
	}
	f.calls++
	if target.Spec.Services["web"].Env["REVISION"] == "bad" {
		// Cluster code marks a failure before it applies anything as NotApplied.
		return cluster.Observation{}, cluster.NotApplied(errors.New("could not check managed database orders (db1) health: Kubernetes API client throttled"))
	}
	return cluster.Observation{Revision: target.Revision, ObservedAt: time.Now(), Status: "healthy", Services: []cluster.ServiceStatus{}}, nil
}

func TestWorkerSkipsRecoveryAndKeepsObservationWhenNothingWasApplied(t *testing.T) {
	db, p := workerDatabase(t)
	ctx := context.Background()
	app, _ := spec.Normalize(spec.Application{Name: "preapply", Services: map[string]spec.Service{"web": {Image: "python@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Env: map[string]string{"REVISION": "good"}}}})
	runtime := &preApplyRuntime{}
	w := Worker{Store: db, Cluster: runtime, Timeout: time.Minute}
	run := func(a spec.Application, rev int64, key string, before func(applicationID string)) (string, string) {
		d, err := db.Accept(ctx, p, "demo", "development", a, rev, key, a)
		if err != nil {
			t.Fatal(err)
		}
		if before != nil {
			before(d.ApplicationID)
		}
		claim, err := db.Claim(ctx)
		if err != nil || claim == nil {
			t.Fatal(err)
		}
		w.run(ctx, claim)
		return d.ID, d.ApplicationID
	}
	run(app, 0, "preapply-first", nil)
	bad, _ := spec.Normalize(app)
	s := bad.Services["web"]
	s.Env["REVISION"] = "bad"
	bad.Services["web"] = s
	runtime.calls = 0
	id, applicationID := run(bad, 1, "preapply-second", func(applicationID string) {
		// The resync stores an observation of the running release.
		if _, err := db.Pool.Exec(ctx, `UPDATE applications SET observed='{"status":"healthy","services":[]}' WHERE id=$1`, applicationID); err != nil {
			t.Fatal(err)
		}
	})
	result, err := db.Deployment(ctx, id)
	if err != nil || result.Status != "failed" || result.RecoveryState != "" || runtime.calls != 1 {
		t.Fatalf("pre-apply failure: %+v, %v, %d deploy calls", result, err, runtime.calls)
	}
	var observed, status string
	if err = db.Pool.QueryRow(ctx, "SELECT COALESCE(observed->>'status',''),status FROM applications WHERE id=$1", applicationID).Scan(&observed, &status); err != nil {
		t.Fatal(err)
	}
	if observed != "healthy" || status != "failed" {
		t.Fatalf("observation %q and status %q after a failure that changed nothing", observed, status)
	}
}
