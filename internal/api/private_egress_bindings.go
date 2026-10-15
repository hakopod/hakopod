package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/hakopod/hakopod/internal/cluster"
)

func (s *Server) registerPrivateEgressRoutes(routes *http.ServeMux) {
	routes.HandleFunc("GET /api/v1/admin/private-egress-bindings", s.listPrivateEgressBindings)
	routes.HandleFunc("GET /api/v1/admin/private-egress-bindings/{name}", s.getPrivateEgressBinding)
	routes.HandleFunc("PUT /api/v1/admin/private-egress-bindings/{name}", s.putPrivateEgressBinding)
	routes.HandleFunc("DELETE /api/v1/admin/private-egress-bindings/{name}", s.deletePrivateEgressBinding)
	routes.HandleFunc("POST /api/v1/admin/private-egress-bindings/discover", s.discoverPrivateEgress)
	routes.HandleFunc("GET /api/v1/private-egress-bindings", s.scopedPrivateEgressBindings)
}

// scopedPrivateEgressBindings lets deployment readers see which approved
// destinations exist for their project and environment, so a plan can name one.
func (s *Server) scopedPrivateEgressBindings(w http.ResponseWriter, r *http.Request) {
	p, e := scope(r)
	application := r.URL.Query().Get("application")
	if !validScope(p, e) || !who(r).Allows("deployments:read", p, e, application) {
		problem(w, 403, "forbidden", "select an accessible project and environment")
		return
	}
	file, db := []cluster.PrivateEgressBinding{}, []cluster.PrivateEgressBinding{}
	var err error
	if s.Cluster != nil {
		file, db, err = s.Cluster.PrivateEgressBindings(r.Context())
	} else {
		var raw []json.RawMessage
		if raw, err = s.Store.PrivateEgressBindingConfigs(r.Context()); err == nil {
			db, err = cluster.DecodePrivateEgressBindings(raw)
		}
	}
	if err != nil {
		failure(w, err)
		return
	}
	type item struct {
		cluster.PrivateEgressBinding
		ManagedBy string `json:"managed_by"`
	}
	items := []item{}
	for source, bindings := range map[string][]cluster.PrivateEgressBinding{"file": file, "database": db} {
		for _, b := range bindings {
			if b.Project == p && b.Environment == e && (application == "" || b.Application == application) {
				items = append(items, item{b, source})
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	write(w, 200, map[string]any{"items": items})
}

func (s *Server) discoverPrivateEgress(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var input struct {
		Region string `json:"region"`
	}
	if !decode(w, r, &input) {
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	proposals, err := s.Cluster.DiscoverPrivateEgress(r.Context(), input.Region)
	if err != nil {
		problem(w, 422, "private_egress_discovery_failed", err.Error())
		return
	}
	write(w, 200, map[string]any{"proposals": proposals})
}

type privateEgressInput struct{ error }

// validatePrivateEgress checks the complete database set together with the
// read-only file bindings, so limits and name collisions span both sources.
func (s *Server) validatePrivateEgress(raw []json.RawMessage) error {
	db, err := cluster.DecodePrivateEgressBindings(raw)
	if err == nil {
		if s.Cluster != nil {
			err = s.Cluster.ValidatePrivateEgressCandidate(db)
		} else {
			err = cluster.ValidatePrivateEgressBindings(db)
		}
	}
	if err != nil {
		return privateEgressInput{err}
	}
	return nil
}

func privateEgressFailure(w http.ResponseWriter, err error) {
	var input privateEgressInput
	if errors.As(err, &input) {
		problem(w, 400, "invalid_private_egress_binding", input.Error())
		return
	}
	failure(w, err)
}

func (s *Server) listPrivateEgressBindings(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	items, err := s.Store.PrivateEgressBindings(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	file := []cluster.PrivateEgressBinding{}
	if s.Cluster != nil {
		file, _, _ = s.Cluster.PrivateEgressBindings(r.Context())
	}
	write(w, 200, map[string]any{"items": items, "managed_by_file": file})
}

func (s *Server) getPrivateEgressBinding(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	item, err := s.Store.PrivateEgressBinding(r.Context(), r.PathValue("name"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, item)
}

func (s *Server) putPrivateEgressBinding(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var input struct {
		cluster.PrivateEgressBinding
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Name != "" && input.Name != r.PathValue("name") {
		problem(w, 400, "invalid_private_egress_binding", "binding name must match the URL")
		return
	}
	input.Name = r.PathValue("name")
	config, err := json.Marshal(input.PrivateEgressBinding)
	if err != nil {
		failure(w, err)
		return
	}
	item, err := s.Store.PutPrivateEgressBinding(r.Context(), who(r), input.Name, config, input.ExpectedRevision, s.validatePrivateEgress)
	if err != nil {
		privateEgressFailure(w, err)
		return
	}
	status := http.StatusOK
	if input.ExpectedRevision == 0 {
		status = http.StatusCreated
	}
	write(w, status, item)
}

func (s *Server) deletePrivateEgressBinding(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var input struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := s.Store.DeletePrivateEgressBinding(r.Context(), who(r), r.PathValue("name"), input.ExpectedRevision); err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
