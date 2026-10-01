package api

import (
	"context"
	"fmt"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/externaldatabase"
	"github.com/hakopod/hakopod/internal/spec"
)

func (s *Server) resolveExternalDatabaseConnections(ctx context.Context, project, environment string, app spec.Application) (map[string]map[string]cluster.DatabaseConnection, error) {
	resources, err := s.Store.ResolveExternalDatabaseBindings(ctx, project, environment, app)
	if err != nil {
		return nil, err
	}
	connections := map[string]cluster.DatabaseConnection{}
	for id, d := range resources {
		credentials, err := externaldatabase.OpenCredentials(s.authEncryptionKey(), d)
		if err != nil {
			return nil, err
		}
		observation := externaldatabase.DefaultProber.Observe(ctx, d, credentials)
		if observation.Status != "ready" || !observation.TLSVerified || !observation.QueryVerified {
			return nil, fmt.Errorf("external database is not reachable through verified TLS")
		}
		url, err := externaldatabase.ConnectionURL(d.Spec, credentials)
		if err != nil {
			return nil, err
		}
		connections[id] = cluster.DatabaseConnection{URL: url, Port: int32(d.Spec.Port), ExternalIPs: observation.VerifiedIPs}
	}
	out := map[string]map[string]cluster.DatabaseConnection{}
	for name, svc := range app.Services {
		for variable, b := range svc.Bindings {
			if b.ExternalDatabase != "" {
				if out[name] == nil {
					out[name] = map[string]cluster.DatabaseConnection{}
				}
				out[name][variable] = connections[b.ExternalDatabase]
			}
		}
	}
	return out, nil
}
