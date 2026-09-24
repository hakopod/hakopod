package worker

import (
	"context"
	"fmt"
	"github.com/hakopod/hakopod/internal/cluster"
	"time"
)

func (w *Worker) cleanupServiceVolumes(ctx context.Context) {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	claim, err := w.Store.ClaimServiceVolumeCleanup(bounded)
	if err != nil || claim == nil {
		return
	}
	defer claim.Release()
	runtime, ok := w.Cluster.(interface {
		DeleteVolumes(context.Context, cluster.Target, []string) error
	})
	if !ok {
		err = fmt.Errorf("runtime does not support volume cleanup")
	} else {
		err = runtime.DeleteVolumes(bounded, cluster.Target{ApplicationID: claim.App.ID, Project: claim.App.Project, Environment: claim.App.Environment, Spec: claim.App.Spec, BeforeStep: claim.Check}, claim.Claims)
	}
	result, done := context.WithTimeout(ctx, 3*time.Second)
	defer done()
	message := ""
	if err != nil {
		message = err.Error()
	}
	_ = claim.Finish(result, message)
}
