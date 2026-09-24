package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"k8s.io/apimachinery/pkg/types"
)

type resizeRuntime struct {
	*cluster.Client
	helperGone                bool
	missing                   bool
	resumes, deploys, deletes int
}

func (f *resizeRuntime) PrepareVolumeResize(context.Context, cluster.Target, spec.Application, spec.VolumeResize, *cluster.VolumeResizeJournal) (bool, error) {
	return false, errors.New("copy failed")
}
func (f *resizeRuntime) RemoveResizeHelper(context.Context, cluster.Target, *cluster.VolumeResizeJournal) (bool, error) {
	return f.helperGone, nil
}
func (f *resizeRuntime) CheckResizeClaim(_ context.Context, _ cluster.Target, _ string, _ types.UID, allowMissing bool) error {
	if f.missing && !allowMissing {
		return errors.New("volume disappeared")
	}
	return nil
}
func (f *resizeRuntime) ResumeResizeServices(context.Context, cluster.Target, spec.VolumeResize) error {
	f.resumes++
	return nil
}
func (f *resizeRuntime) DeleteVolumes(context.Context, cluster.Target, []string) error {
	f.deletes++
	return nil
}
func (f *resizeRuntime) Observe(context.Context, cluster.Target) (cluster.Observation, error) {
	return cluster.Observation{Status: "healthy"}, nil
}
func (f *resizeRuntime) Deploy(ctx context.Context, t cluster.Target, _ func(cluster.Event)) (cluster.Observation, error) {
	if err := t.BeforeStep(ctx); err != nil {
		return cluster.Observation{}, err
	}
	f.deploys++
	return cluster.Observation{Status: "healthy"}, nil
}

func resizeWorkerFixture(t *testing.T) (*store.Store, store.Principal, store.VolumeResize) {
	t.Helper()
	db, p := workerDatabase(t)
	ctx := context.Background()
	app, err := spec.Normalize(spec.Application{Name: "resize-worker", Services: map[string]spec.Service{"db": {Image: volumeTestImage, Volume: &spec.Volume{MountPath: "/data", SizeGiB: 2}}}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Accept(ctx, p, "demo", "development", app, 0, "resize-fixture", app)
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.Claim(ctx)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	if err = c.Finish(ctx, "succeeded", "", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	c.Release()
	r, err := db.StartVolumeResize(ctx, p, d.ApplicationID, "db-data", 1, 1, "resize-worker", store.JSON(cluster.VolumeResizeJournal{SourceUID: "source", SourcePV: "pv", SourcePVUID: "pv-uid", TargetUID: "target"}))
	if err != nil {
		t.Fatal(err)
	}
	return db, p, r
}

const volumeTestImage = "python@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestResizeFailureStopsHelperBeforeResumingOriginal(t *testing.T) {
	for _, gone := range []bool{false, true} {
		t.Run(map[bool]string{false: "helper-running", true: "helper-gone"}[gone], func(t *testing.T) {
			db, _, r := resizeWorkerFixture(t)
			runtime := &resizeRuntime{helperGone: gone}
			w := Worker{Store: db, Cluster: runtime}
			w.resizeVolume(context.Background())
			if (runtime.resumes == 1) != gone {
				t.Fatal("resumed while helper could still copy", runtime.resumes)
			}
			result, err := db.VolumeResize(context.Background(), r.ID)
			if err != nil || result.Error == "" || result.Phase != "copying" {
				t.Fatal("failure not durable", result, err)
			}
			if runtime.deploys != 0 || runtime.deletes != 0 {
				t.Fatal("failure changed application data")
			}
		})
	}
}
func TestResizeCancellationPreservesMissingSourceAndReclaimsOnlyStaging(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "source-present", true: "source-missing"}[missing], func(t *testing.T) {
			db, p, r := resizeWorkerFixture(t)
			ctx := context.Background()
			if err := db.ResizeAction(ctx, p, r.ApplicationID, r.ID, "cancel", ""); err != nil {
				t.Fatal(err)
			}
			runtime := &resizeRuntime{helperGone: true, missing: missing}
			w := Worker{Store: db, Cluster: runtime}
			w.resizeVolume(ctx)
			result, err := db.VolumeResize(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if missing {
				if runtime.resumes != 0 || runtime.deletes != 0 || result.Error == "" {
					t.Fatal("missing source was ignored")
				}
			} else {
				if result.Phase != "cancelled" || runtime.deletes != 1 || runtime.resumes != 1 {
					t.Fatal("cancel did not reclaim staging", result)
				}
			}
		})
	}
}
func TestResizeCutoverNeverRecreatesMissingVerifiedTarget(t *testing.T) {
	db, _, r := resizeWorkerFixture(t)
	ctx := context.Background()
	c, err := db.ClaimVolumeResize(ctx)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	if err = c.Save(ctx, "copying", cluster.VolumeResizeJournal{SourceUID: "source", TargetUID: "target", Verified: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err = c.BeginCutover(ctx); err != nil {
		t.Fatal(err)
	}
	c.Release()
	runtime := &resizeRuntime{missing: true}
	w := Worker{Store: db, Cluster: runtime}
	w.resizeVolume(ctx)
	result, err := db.VolumeResize(ctx, r.ID)
	if err != nil || result.Error == "" || result.Phase != "switching" {
		t.Fatal("missing target not stopped", result, err)
	}
	if runtime.deploys != 0 || runtime.resumes != 0 || runtime.deletes != 0 {
		t.Fatal("missing verified volume caused a destructive fallback")
	}
}
