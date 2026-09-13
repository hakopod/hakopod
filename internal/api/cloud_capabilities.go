package api

import (
	"net/http"
	"regexp"
)

var cloudProjectName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

func (s *Server) cloudCapabilities(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	environment := r.URL.Query().Get("environment")
	if !cloudProjectName.MatchString(project) || !cloudProjectName.MatchString(environment) || !who(r).Allows("deployments:read", project, environment, "") {
		problem(w, 403, "forbidden", "project/environment deployment read permission is required")
		return
	}
	if s.Cluster == nil || s.Store.ValidateDeployment == nil {
		problem(w, 503, "unavailable", "Kubernetes is unavailable")
		return
	}
	if !s.Cluster.CloudMode() {
		problem(w, 409, "self_hosted", "this installation is not in managed-cloud mode")
		return
	}
	result, err := s.Cluster.CloudCapabilities(r.Context())
	if err != nil {
		problem(w, 503, "unavailable", "node capacity is unavailable")
		return
	}
	write(w, 200, result)
}
