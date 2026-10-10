package api

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

func (s *Server) databaseExplorerConnections(w http.ResponseWriter, r *http.Request) {
	// Scope is part of the request contract. A stale browser preference must not
	// silently redirect an explorer tab into another project or environment.
	values, queryErr := url.ParseQuery(r.URL.RawQuery)
	project, environment := values.Get("project"), values.Get("environment")
	if queryErr != nil || len(values) != 2 || len(values["project"]) != 1 || len(values["environment"]) != 1 || !validScope(project, environment) {
		problem(w, 400, "invalid_scope", "Supply one project and one environment.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	p := who(r)
	target := database.Resource{Project: project, Environment: environment}
	if !p.AllowsDatabase(project, environment, false) || !databaseQueryAllowed(p, target, true) {
		problem(w, 404, "not_found", "resource not found")
		return
	}
	items, err := s.Store.Databases(ctx, p, project, environment)
	if err != nil {
		failure(w, err)
		return
	}
	result := database.ExplorerCatalog{SchemaVersion: 1, Project: project, Environment: environment, Items: []database.ExplorerConnection{}}
	now := time.Now().UTC()
	for _, item := range items {
		if database.ExplorerSupported(item.Spec.Engine) {
			result.Items = append(result.Items, database.ExplorerSource(item, now, databaseQueryAllowed(p, item, false)))
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, result)
}
