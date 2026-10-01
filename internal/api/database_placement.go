package api

import "net/http"

func (s *Server) databasePlacementNodes(w http.ResponseWriter, r *http.Request) {
	project, environment := scope(r)
	if !validScope(project, environment) {
		problem(w, 400, "invalid_request", "select a project and environment")
		return
	}
	if !who(r).AllowsDatabase(project, environment, false) {
		problem(w, 403, "forbidden", "database read permission is required")
		return
	}
	if len(r.URL.Query()) != 2 || len(r.URL.Query()["project"]) != 1 || len(r.URL.Query()["environment"]) != 1 {
		problem(w, 400, "invalid_request", "provide one project and environment")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "database placement is unavailable")
		return
	}
	items, err := s.Cluster.DatabasePlacementNodes(r.Context(), project, environment)
	if err != nil {
		problem(w, 503, "database_placement_unavailable", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, 200, map[string]any{"items": items, "limit": 48})
}
