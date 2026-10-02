package api

import (
	"context"
	"net/http"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
)

type ManagedPlatformCatalog struct {
	Project          string                            `json:"project"`
	Environment      string                            `json:"environment"`
	StorageClass     string                            `json:"storage_class"`
	Nodes            []managedplatform.CapacityNode    `json:"nodes"`
	SecretReferences []managedplatform.SecretReference `json:"secret_references"`
	Items            []managedplatform.CatalogEntry    `json:"items"`
}

type ManagedPlatformCatalogPlanner interface {
	ManagedPlatformCatalog(context.Context, store.Principal, string, string) (ManagedPlatformCatalog, error)
}

func (p *NativeManagedPlatformPlanner) ManagedPlatformCatalog(_ context.Context, principal store.Principal, project, environment string) (ManagedPlatformCatalog, error) {
	if !principal.AllowsManagedPlatform(project, environment, false) {
		return ManagedPlatformCatalog{}, store.ErrForbidden
	}
	return ManagedPlatformCatalog{Project: project, Environment: environment, StorageClass: p.ApprovedEncryptedStorageClass,
		Nodes:            append([]managedplatform.CapacityNode{}, p.CatalogNodes...),
		SecretReferences: append([]managedplatform.SecretReference{}, p.CatalogSecrets[project][environment]...),
		Items:            managedplatform.CatalogEntries()}, nil
}

func (s *Server) managedPlatformCatalog(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	project, environment := query.Get("project"), query.Get("environment")
	if len(query) != 2 || len(query["project"]) != 1 || len(query["environment"]) != 1 || !validScope(project, environment) {
		problem(w, http.StatusBadRequest, "invalid_request", "provide one project and environment")
		return
	}
	principal := who(r)
	if !principal.AllowsManagedPlatform(project, environment, false) {
		failure(w, store.ErrForbidden)
		return
	}
	planner, ok := s.ManagedPlatformPlanner.(ManagedPlatformCatalogPlanner)
	if !ok || s.Store == nil || s.Store.Pool == nil {
		problem(w, http.StatusServiceUnavailable, "managed_platform_unavailable", "managed platform configuration is unavailable")
		return
	}
	if err := s.Store.ValidateManagedPlatformScope(r.Context(), principal, project, environment); err != nil {
		failure(w, err)
		return
	}
	catalog, err := planner.ManagedPlatformCatalog(r.Context(), principal, project, environment)
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, catalog)
}
