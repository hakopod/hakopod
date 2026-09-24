package worker

import (
	"context"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
)

func (w *Worker) cleanupRetained(ctx context.Context) {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	claim, err := w.Store.ClaimRetainedCleanup(bounded)
	if err != nil || claim == nil {
		return
	}
	defer claim.Release()
	data := claim.Data
	// The same ownership-checked namespace/PV deletion used for explicitly
	// disposable previews is authorized here by a separate, permanent-data consent.
	err = w.Cluster.DeletePreview(bounded, cluster.Target{ApplicationID: data.ApplicationID, Project: data.Project, Environment: data.Environment, Spec: spec.Application{Name: data.Name}, BeforeStep: claim.Check})
	result, done := context.WithTimeout(ctx, 3*time.Second)
	defer done()
	message := ""
	if err != nil {
		message = err.Error()
	}
	_ = claim.Finish(result, message)
}
