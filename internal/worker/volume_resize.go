package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

type volumeResizeRuntime interface {
	ResumeResizeServices(context.Context, cluster.Target, spec.VolumeResize) error
	PrepareVolumeResize(context.Context, cluster.Target, spec.Application, spec.VolumeResize, *cluster.VolumeResizeJournal) (bool, error)
	RemoveResizeHelper(context.Context, cluster.Target, *cluster.VolumeResizeJournal) (bool, error)
	CheckResizeClaim(context.Context, cluster.Target, string, types.UID, bool) error
	DeleteVolumes(context.Context, cluster.Target, []string) error
}

func (w *Worker) resizeVolume(parent context.Context) {
	acquire, done := context.WithTimeout(parent, 10*time.Second)
	c, err := w.Store.ClaimVolumeResize(acquire)
	done()
	if err != nil || c == nil {
		return
	}
	defer c.Release()
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	r := c.Resize
	var state cluster.VolumeResizeJournal
	if err = json.Unmarshal(r.Runtime, &state); err != nil {
		return
	}
	fail := func(err error) {
		if parent.Err() != nil {
			return
		}
		save, end := context.WithTimeout(parent, 5*time.Second)
		defer end()
		_ = c.Save(save, c.Resize.Phase, state, err.Error())
	}
	if err = c.Check(ctx); err != nil {
		fail(err)
		return
	}
	runtime, ok := w.Cluster.(volumeResizeRuntime)
	if !ok {
		fail(fmt.Errorf("runtime does not support volume migration"))
		return
	}
	_, plan, err := spec.ResizeVolume(r.SourceSpec, r.Claim, r.SizeGiB, r.ID)
	if err != nil {
		fail(err)
		return
	}
	t := cluster.Target{ApplicationID: c.App.ID, Project: c.App.Project, Environment: c.App.Environment, OperationID: r.ID, Revision: c.App.Revision, Spec: r.SourceSpec, BeforeStep: c.Check}
	switch r.Phase {
	case "queued", "copying":
		if r.Phase == "queued" {
			if err = c.Save(ctx, "copying", state, ""); err != nil {
				return
			}
		}
		ready, e := runtime.PrepareVolumeResize(ctx, t, r.TargetSpec, plan, &state)
		if e != nil {
			if apierrors.IsConflict(e) {
				_ = c.Save(ctx, c.Resize.Phase, state, "")
				return
			}
			if !state.Verified && ctx.Err() == nil {
				gone, cleanupErr := runtime.RemoveResizeHelper(ctx, t, &state)
				if cleanupErr != nil || !gone {
					fail(fmt.Errorf("%v; copy helper must stop before original services can resume; retry or cancel maintenance", e))
					return
				}
				if identityErr := runtime.CheckResizeClaim(ctx, t, r.Claim, state.SourceUID, false); identityErr != nil {
					fail(identityErr)
					return
				}
				if resumeErr := runtime.ResumeResizeServices(ctx, t, plan); resumeErr != nil {
					e = fmt.Errorf("%v; original services could not resume: %w", e, resumeErr)
				}
			}
			fail(e)
			return
		}
		if err = c.Save(ctx, "copying", state, ""); err != nil {
			return
		}
		if !ready {
			return
		}
		if err = c.Check(ctx); err != nil {
			fail(err)
			return
		}
		if err = c.BeginCutover(ctx); err != nil {
			fail(err)
		}
	case "switching":
		if !state.Verified {
			fail(fmt.Errorf("verified copy is required before switching services"))
			return
		}
		if err = runtime.CheckResizeClaim(ctx, t, r.TargetClaim, state.TargetUID, false); err != nil {
			fail(err)
			return
		}
		targetGuard := t
		t.BeforeStep = func(step context.Context) error {
			return runtime.CheckResizeClaim(step, targetGuard, r.TargetClaim, state.TargetUID, false)
		}
		t.Spec = r.TargetSpec
		t.Previous = &r.SourceSpec
		observation, e := w.Cluster.Deploy(ctx, t, nil)
		if apierrors.IsConflict(e) {
			return
		}
		if parent.Err() != nil {
			return
		}
		message := ""
		if e != nil {
			message = e.Error()
		}
		save, end := context.WithTimeout(parent, 5*time.Second)
		defer end()
		_ = c.CutoverResult(save, observation, message)
	case "cancelling":
		if err = runtime.CheckResizeClaim(ctx, t, r.Claim, state.SourceUID, false); err != nil {
			fail(err)
			return
		}
		gone, e := runtime.RemoveResizeHelper(ctx, t, &state)
		if e != nil {
			fail(e)
			return
		}
		if !gone {
			_ = c.Save(ctx, r.Phase, state, "")
			return
		}
		if err = runtime.ResumeResizeServices(ctx, t, plan); err != nil {
			fail(err)
			return
		}
		observation, err := w.Cluster.Observe(ctx, t)
		if err != nil {
			fail(err)
			return
		}
		if observation.Status != "healthy" {
			return
		}
		if err = runtime.CheckResizeClaim(ctx, t, r.TargetClaim, state.TargetUID, true); err != nil {
			fail(err)
			return
		}
		cleanupTarget := t
		cleanupTarget.BeforeStep = func(step context.Context) error {
			return runtime.CheckResizeClaim(step, t, r.TargetClaim, state.TargetUID, true)
		}
		if err = runtime.DeleteVolumes(ctx, cleanupTarget, []string{r.TargetClaim}); err != nil {
			fail(err)
			return
		}
		if err = c.Check(ctx); err != nil {
			fail(err)
			return
		}
		if err = c.Reclaimed(ctx, r.TargetClaim, "cancelled"); err != nil {
			fail(err)
		}
	case "deleting_original":
		if !state.Verified {
			fail(fmt.Errorf("copy verification is missing; original remains retained"))
			return
		}
		var healthy bool
		if err = w.Store.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM deployments WHERE application_id=$1 AND revision=$2 AND status='succeeded')", c.App.ID, c.App.Revision).Scan(&healthy); err != nil {
			fail(err)
			return
		}
		if !healthy {
			fail(fmt.Errorf("wait for the current deployment to succeed before deleting the original"))
			return
		}
		t.Spec = c.App.Spec
		observation, e := w.Cluster.Observe(ctx, t)
		if e != nil {
			fail(e)
			return
		}
		if observation.Status != "healthy" && observation.Status != "empty" {
			fail(fmt.Errorf("current services are not healthy; original remains retained"))
			return
		}
		if err = runtime.CheckResizeClaim(ctx, t, r.Claim, state.SourceUID, true); err != nil {
			fail(err)
			return
		}
		cleanupTarget := t
		cleanupTarget.BeforeStep = func(step context.Context) error {
			return runtime.CheckResizeClaim(step, t, r.Claim, state.SourceUID, true)
		}
		if err = runtime.DeleteVolumes(ctx, cleanupTarget, []string{r.Claim}); err != nil {
			fail(err)
			return
		}
		if err = c.Check(ctx); err != nil {
			fail(err)
			return
		}
		if err = c.Reclaimed(ctx, r.Claim, "completed"); err != nil {
			fail(err)
		}
	}
}
