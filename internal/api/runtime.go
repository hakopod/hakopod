package api

import (
	"github.com/hakopod/hakopod/internal/cluster"
	"net/http"
)

func (s *Server) registerRuntimeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/nodes/{name}/cordon", s.cordonNode)
	mux.HandleFunc("POST /api/v1/nodes/{name}/drain", s.drainNode)
	mux.HandleFunc("GET /api/v1/nodes/enrollments", s.nodeEnrollments)
	mux.HandleFunc("POST /api/v1/nodes/enrollments", s.createNodeEnrollment)
	mux.HandleFunc("DELETE /api/v1/nodes/enrollments/{id}", s.revokeNodeEnrollment)
	mux.HandleFunc("GET /api/v1/applications/{id}/services/{service}/tls", s.serviceTLS)
	mux.HandleFunc("POST /api/v1/applications/{id}/services/{service}/tls", s.attachTLS)
	mux.HandleFunc("GET /api/v1/tls/issuers", s.tlsIssuers)
	mux.HandleFunc("POST /api/v1/tls/issuers", s.createTLSIssuer)
	mux.HandleFunc("GET /api/v1/registries", s.registries)
	mux.HandleFunc("POST /api/v1/registries", s.putRegistry)
	mux.HandleFunc("PUT /api/v1/registries/{name}", s.putRegistry)
	mux.HandleFunc("DELETE /api/v1/registries/{name}", s.deleteRegistry)
	mux.HandleFunc("POST /api/v1/registries/{name}/sync", s.syncRegistry)
	mux.HandleFunc("GET /api/v1/applications/{id}/services/{service}/runtime", s.serviceRuntime)
	mux.HandleFunc("POST /api/v1/applications/{id}/services/{service}/restart", s.restartService)
	mux.HandleFunc("POST /api/v1/applications/{id}/services/{service}/scale", s.scaleService)
	mux.HandleFunc("POST /api/v1/applications/{id}/services/{service}/stop", s.stopService)
	mux.HandleFunc("POST /api/v1/applications/{id}/services/{service}/resume", s.resumeService)
}

func (s *Server) serviceRuntime(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	service := r.PathValue("service")
	if _, ok = a.Spec.Services[service]; !ok {
		problem(w, 404, "not_found", "service not found")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	value, err := s.Cluster.ServiceRuntime(r.Context(), cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec, Revision: a.Revision}, service)
	if err != nil {
		problem(w, 503, "unavailable", "service observations are temporarily unavailable")
		return
	}
	write(w, 200, value)
}
