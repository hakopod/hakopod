package worker

import (
	"context"
	"github.com/hakopod/hakopod/internal/cluster"
	"time"
)

func (w *Worker) cleanupPreview(ctx context.Context) {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	claim, err := w.Store.ClaimPreviewCleanup(bounded)
	if err != nil || claim == nil {
		return
	}
	defer claim.Release()
	a := claim.App
	err = w.Cluster.DeletePreview(bounded, cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec, BeforeStep: claim.Check})
	result, cancelResult := context.WithTimeout(ctx, 3*time.Second)
	defer cancelResult()
	message := ""
	if err != nil {
		message = err.Error()
	}
	_ = claim.Finish(result, message)
}
