// Package management owns the shared API and reconciliation lifecycle.
package management

import (
	"context"
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/hakopod/hakopod/internal/worker"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Start configures the canonical runtime and starts bounded workers. Cancel ctx
// before calling wait. Both distribution entry points use this lifecycle.
func Start(ctx context.Context, server *api.Server, domain string, rollout time.Duration) (http.Handler, func()) {
	db, kube := server.Store, server.Cluster
	db.ValidateDeployment = func(ctx context.Context, app store.Application, next spec.Application) error {
		return kube.ValidateDelivery(ctx, cluster.Target{ApplicationID: app.ID, Project: app.Project, Environment: app.Environment, Spec: next, Revision: app.Revision})
	}
	server.ConfigureSecretProviders()
	db.ProtectedDomains = []string{domain}
	if dashboard, err := url.Parse(server.Auth.PublicURL); err == nil {
		db.ProtectedDomains = append(db.ProtectedDomains, strings.ToLower(dashboard.Hostname()))
	}
	handler := server.Handler()
	worker := &worker.Worker{Store: db, Cluster: kube, Concurrency: 2, Timeout: rollout*3 + time.Minute}
	var wg sync.WaitGroup
	for _, run := range []func(context.Context){worker.Run, worker.Resync, server.RunSources, server.RunPlatform, server.RunBuilds, server.RunBackups, server.RunShowcase, server.RunAlarms} {
		wg.Add(1)
		go func(run func(context.Context)) { defer wg.Done(); run(ctx) }(run)
	}
	return handler, func() { server.CloseTerminals(); wg.Wait() }
}
