package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/requestlog"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) registerRequestRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/requests", s.requests)
	mux.HandleFunc("GET /api/v1/applications/{id}/services/{service}/requests/routing", s.requestRouting)
}
func (s *Server) requests(w http.ResponseWriter, r *http.Request) {
	p := who(r)
	if !slices.Contains(p.Permissions, "admin") && !slices.Contains(p.Permissions, "logs:read") {
		problem(w, 403, "forbidden", "Request logs require logs:read permission.")
		return
	}
	q := r.URL.Query()
	in := store.RequestQuery{Project: q.Get("project"), Environment: q.Get("environment"), ApplicationID: q.Get("application_id"), Service: q.Get("service"), Method: q.Get("method"), Search: q.Get("search"), Cursor: q.Get("cursor")}
	for name, out := range map[string]*int{"status": &in.Status, "since_seconds": &in.SinceSeconds, "limit": &in.Limit} {
		if raw := q.Get(name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				problem(w, 400, "invalid_request_query", "Use valid numeric request filters.")
				return
			}
			*out = n
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := s.Store.Requests(ctx, who(r), in)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, result)
}
func (s *Server) requestRouting(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "logs:read")
	if !ok {
		return
	}
	service := r.PathValue("service")
	if _, ok = a.Spec.Services[service]; !ok {
		problem(w, 404, "service_not_found", "Service does not exist.")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "cluster_unavailable", "Routing observations are unavailable.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := s.Cluster.RequestRouting(ctx, cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec, Revision: a.Revision}, service)
	if err != nil {
		problem(w, 503, "routing_unavailable", "Could not read this service's current ingress and endpoints. Check cluster connectivity and the deployment status.")
		return
	}
	write(w, 200, result)
}
func (s *Server) RunRequests(ctx context.Context) {
	if s.Store == nil || s.Cluster == nil {
		return
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		s.collectRequests(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (s *Server) collectRequests(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	conn, err := s.Store.Pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	var locked bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended('hakopod.requests.collector',0))").Scan(&locked); err != nil || !locked {
		return
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if _, e := conn.Exec(cleanup, "SELECT pg_advisory_unlock(hashtextextended('hakopod.requests.collector',0))"); e != nil {
			conn.Conn().Close(cleanup)
		}
	}()
	state, message := "collecting", "Collecting completed HTTP requests from the managed ingress."
	if err = s.Cluster.ConfigureRequestLogs(ctx); err != nil {
		s.Store.SaveRequests(ctx, nil, nil, "unavailable", err.Error(), 0)
		return
	}
	cursors, err := s.Store.RequestCursors(ctx)
	if err != nil {
		return
	}
	batch, err := s.Cluster.CollectRequests(ctx, cursors)
	if err != nil {
		s.Store.SaveRequests(ctx, nil, nil, "unavailable", "Ingress logs could not be read. Check the controller and Kubernetes log permissions.", 0)
		return
	}
	bindings, truncated, err := s.requestBindings(ctx)
	if err != nil {
		return
	}
	if truncated {
		batch.Gaps++
	}
	for i := range batch.Entries {
		e := &batch.Entries[i]
		if b, ok := bindings[e.Backend]; ok {
			e.ApplicationID, e.Application, e.Project, e.Environment, e.Service = b.ApplicationID, b.Application, b.Project, b.Environment, b.Service
		}
		e.Host = strings.ToLower(e.Host)
	}
	if batch.Gaps > 0 {
		state = "limited"
		message = "Some ingress logs were unavailable, malformed or exceeded collection limits. This window may be incomplete."
	}
	if err = s.Store.SaveRequests(ctx, batch.Entries, batch.Cursors, state, message, batch.Gaps); err != nil {
		slog.Warn("request collection could not persist batch")
	}
}

// Binding is by the exact generated backend, never a client-controlled Host.
// Unknown/default backends remain operator-only. Read only routing metadata.
func (s *Server) requestBindings(ctx context.Context) (map[string]requestlog.Entry, bool, error) {
	rows, err := s.Store.Pool.Query(ctx, "SELECT id,name,project,environment,jsonb_object_agg(s.key,jsonb_build_object('port',s.value->'port','ports',s.value->'ports','serverless',s.value->'serverless')) FROM applications a CROSS JOIN LATERAL jsonb_each(a.spec->'services') s GROUP BY id ORDER BY id LIMIT 1001")
	if err != nil {
		return nil, false, err
	}
	bindings := map[string]requestlog.Entry{}
	count := 0
	truncated := false
	for rows.Next() {
		var id, name, project, env string
		var data []byte
		if err = rows.Scan(&id, &name, &project, &env, &data); err != nil {
			break
		}
		count++
		if count > 1000 {
			truncated = true
			break
		}
		var services map[string]spec.Service
		if err = json.Unmarshal(data, &services); err != nil {
			break
		}
		for service, svc := range services {
			if svc.Serverless != nil {
				bindings[cluster.Namespace(id)+"_svc_"+cluster.ActivationServiceName(service)+"_http"] = requestlog.Entry{ApplicationID: id, Application: name, Project: project, Environment: env, Service: service}
			}
			for _, port := range spec.ServicePorts(svc) {
				key := cluster.Namespace(id) + "_svc_" + service + "_" + port.Name
				bindings[key] = requestlog.Entry{ApplicationID: id, Application: name, Project: project, Environment: env, Service: service}
				if port.Name == "" {
					bindings[cluster.Namespace(id)+"_svc_"+service+"_"+strconv.Itoa(int(port.Port))] = bindings[key]
				}
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	return bindings, truncated, nil
}
