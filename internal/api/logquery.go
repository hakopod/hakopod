package api

import (
	"context"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/logquery"
)

func (s *Server) queryLogs(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "logs:read")
	if !ok {
		return
	}
	var options cluster.LogQueryOptions
	if !decode(w, r, &options) {
		return
	}
	if _, ok = a.Spec.Services[options.Service]; !ok {
		problem(w, 400, "invalid_service", "select an application service")
		return
	}
	if err := options.Validate(); err != nil {
		problem(w, 400, "invalid_query_options", err.Error())
		return
	}
	match, err := logquery.Compile(options.Query)
	if err != nil {
		problem(w, 400, "invalid_log_query", err.Error())
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "cluster_unavailable", "Kubernetes is unavailable")
		return
	}
	if !s.streamSlot(w) {
		return
	}
	defer func() { <-s.streams }()
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	go s.guardStream(ctx, cancel, who(r).KeyID, a, "logs:read")
	result, err := s.Cluster.QueryLogs(ctx, cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec, Revision: a.Revision}, options, match)
	if err != nil {
		problem(w, 503, "logs_unavailable", "No readable logs for the selected pods and containers; inspect runtime events or narrow the selection")
		return
	}
	// Do not return buffered results after the reader lost authority.
	p, err := s.Store.KeyPrincipal(ctx, who(r).KeyID)
	if err != nil || !p.Allows("logs:read", a.Project, a.Environment, a.Name) {
		problem(w, 403, "forbidden", "log access was revoked")
		return
	}
	writes := guardResponseWrites(ctx, w, 10*time.Second)
	defer writes.stop()
	if !writes.begin() {
		return
	}
	write(w, 200, result)
}
