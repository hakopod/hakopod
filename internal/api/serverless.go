package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/idlehttp"
	"github.com/hakopod/hakopod/internal/serverless"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type serverlessRuntime struct{ *idlehttp.Service }

func (s *Server) ServerlessGateway() *serverless.Gateway {
	return serverless.New(&serverlessRuntime{&idlehttp.Service{Store: s.Store, Cluster: s.Cluster}}, s.Cluster.ServerlessSourceIP())
}
func (s *serverlessRuntime) Routes(ctx context.Context) ([]serverless.Route, error) {
	// Read only HTTP runtime fields. Secret values and application environment are
	// neither needed nor retained in the gateway's routing table.
	rows, err := s.Store.Pool.Query(ctx, `SELECT a.id,a.project,a.environment,a.revision,s.key,jsonb_build_object('serverless',s.value->'serverless','public',s.value->'public','port',s.value->'port','replicas',s.value->'replicas','suspended',s.value->'suspended'),a.spec->'domains' FROM applications a CROSS JOIN LATERAL jsonb_each(a.spec->'services') s WHERE a.status='healthy' AND s.value->'serverless' IS NOT NULL AND s.value->'serverless'<>'null'::jsonb ORDER BY a.id,s.key LIMIT 513`)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		t    cluster.Target
		name string
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		var svc spec.Service
		var data, domains []byte
		if err = rows.Scan(&c.t.ApplicationID, &c.t.Project, &c.t.Environment, &c.t.Revision, &c.name, &data, &domains); err != nil {
			break
		}
		if err = json.Unmarshal(data, &svc); err != nil {
			break
		}
		if len(domains) > 0 {
			if err = json.Unmarshal(domains, &c.t.Spec.Domains); err != nil {
				break
			}
		}
		if svc.Suspended {
			continue
		}
		c.t.Spec.Services = map[string]spec.Service{c.name: svc}
		candidates = append(candidates, c)
		if len(candidates) > 512 {
			err = errors.New("serverless service limit exceeded")
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []serverless.Route{}
	backends, err := s.Cluster.ServerlessBackends(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range candidates {
		hosts, e := s.Cluster.IdleHTTPHosts(ctx, c.t, c.name)
		if e != nil {
			return nil, e
		}
		backend, e := backends.Backend(c.t, c.name)
		if e != nil {
			// A missing/unowned workload must not take every other application's
			// public routes offline. It has no route until ownership is restored.
			slog.Debug("serverless backend unavailable", "application", c.t.ApplicationID, "service", c.name)
			continue
		}
		result = append(result, serverless.Route{Target: idlehttp.IdleHTTPService{ApplicationID: c.t.ApplicationID, Project: c.t.Project, Environment: c.t.Environment, Service: c.name, Revision: c.t.Revision, Hosts: hosts}, Backend: backend, Settings: *c.t.Spec.Services[c.name].Serverless})
	}
	return result, nil
}
func (s *serverlessRuntime) Validate(ctx context.Context, r serverless.Route) error {
	var valid bool
	err := s.Store.Pool.QueryRow(ctx, `SELECT revision=$2 AND status='healthy' AND spec->'services'->$3->'serverless' IS NOT NULL AND COALESCE((spec->'services'->$3->>'suspended')::boolean,false)=false FROM applications WHERE id=$1`, r.Target.ApplicationID, r.Target.Revision, r.Target.Service).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return idlehttp.ErrIdleIneligible
	}
	// API-managed Service changes first advance this durable revision/status.
	// Ownership and the stable ClusterIP are refreshed in Routes; never put a
	// Kubernetes API call on every hot HTTP request.

	return nil
}

func (s *Server) placementNodes(w http.ResponseWriter, r *http.Request) {
	project, environment := scope(r)
	application := r.URL.Query().Get("application")
	if !validScope(project, environment) || !who(r).Allows("deployments:write", project, environment, application) {
		failure(w, store.ErrForbidden)
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "cluster_unavailable", "Node placement is unavailable.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	target := cluster.Target{Project: project, Environment: environment, Spec: spec.Application{Name: application}}
	nodes, err := s.Cluster.PlacementNodes(ctx, target)
	if err != nil {
		problem(w, 503, "placement_unavailable", "Could not load the nodes available in this environment.")
		return
	}
	write(w, 200, map[string]any{"items": nodes, "serverless_available": s.Cluster.ServerlessEnabled()})
}
