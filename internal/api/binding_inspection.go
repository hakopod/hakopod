package api

import (
	"context"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/bindingprobe"
	"github.com/hakopod/hakopod/internal/cluster"
)

var bindingInspectionSlots = make(chan struct{}, 8)

func (s *Server) inspectServiceBinding(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	service, variable := r.PathValue("service"), r.PathValue("variable")
	if _, exists := a.Spec.Services[service].Bindings[variable]; !exists {
		problem(w, 404, "binding_not_found", "This service has no binding with that variable name.")
		return
	}
	select {
	case bindingInspectionSlots <- struct{}{}:
	default:
		problem(w, 429, "binding_inspection_limit", "Eight binding inspections are active. Try again shortly.")
		return
	}
	defer func() { <-bindingInspectionSlots }()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	last, err := s.Store.LatestBindingTest(ctx, who(r), a.ID, service, variable)
	if err != nil {
		failure(w, err)
		return
	}
	result := bindingprobe.NewInspection(a.ID, service, variable, a.Revision, time.Now().UTC(), last)
	if s.Cluster != nil {
		result = s.Cluster.InspectServiceBinding(ctx, cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Revision: a.Revision, Spec: a.Spec}, service, variable, last)
	}
	current, err := s.Store.Application(ctx, a.ID)
	if err != nil {
		failure(w, err)
		return
	}
	if current.Revision != a.Revision {
		problem(w, 409, "revision_changed", "The application changed during inspection. Refresh its bindings.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, result)
}
