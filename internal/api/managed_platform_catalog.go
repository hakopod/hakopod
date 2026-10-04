package api

import (
	"context"
	"net/http"
	"time"

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

func (p *NativeManagedPlatformPlanner) ManagedPlatformCatalog(ctx context.Context, principal store.Principal, project, environment string) (ManagedPlatformCatalog, error) {
	if !principal.AllowsManagedPlatform(project, environment, false) {
		return ManagedPlatformCatalog{}, store.ErrForbidden
	}
	nodes := p.CatalogNodes
	if p.CatalogCapacity != nil {
		capacity, err := p.CatalogCapacity(ctx, project, environment)
		if err != nil {
			return ManagedPlatformCatalog{}, err
		}
		if err = capacity.Validate(); err != nil {
			return ManagedPlatformCatalog{}, err
		}
		if capacity.StorageClass != p.ApprovedEncryptedStorageClass {
			return ManagedPlatformCatalog{}, store.ErrForbidden
		}
		nodes = capacity.Nodes
	}
	items := managedplatform.CatalogEntries()
	for index := range items {
		switch items[index].Kind {
		case "supabase":
			items[index].Capability = scopedPlatformCapability(ctx, "Supabase", managedplatform.SupabaseReleaseQualified(), items[index].Capability, p.ValidateSupabaseQualification)
		case "neon":
			items[index].Capability = scopedPlatformCapability(ctx, "Neon", managedplatform.NeonReleaseQualified(), items[index].Capability, p.ValidateNeonQualification)
		}
	}
	return ManagedPlatformCatalog{Project: project, Environment: environment, StorageClass: p.ApprovedEncryptedStorageClass,
		Nodes:            append([]managedplatform.CapacityNode{}, nodes...),
		SecretReferences: append([]managedplatform.SecretReference{}, p.CatalogSecrets[project][environment]...),
		Items:            items}, nil
}

func scopedPlatformCapability(ctx context.Context, name string, released bool, capability managedplatform.Capability, validate func(context.Context) error) managedplatform.Capability {
	capability, _ = reviewedPlatformCapability(ctx, name, released, capability, validate)
	return capability
}

func reviewedPlatformCapability(ctx context.Context, name string, released bool, capability managedplatform.Capability, validate func(context.Context) error) (managedplatform.Capability, error) {
	// A qualified release does not imply that this installation is approved.
	capability.Available, capability.ClusterQualified, capability.PublicQualified = false, false, false
	if validate == nil {
		capability.Reason = name + " requires a reviewed operator qualification binding"
		return capability, nil
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := validate(bounded); err != nil {
		capability.Reason = name + " operator qualification does not match this cluster"
		return capability, err
	}
	if released {
		capability.Available, capability.ClusterQualified = true, true
		capability.Reason = name + " is qualified for this cluster; public endpoints remain unavailable"
	}
	return capability, nil
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
