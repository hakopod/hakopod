package api

import (
	"net/http"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) registerPreviewRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/applications/{id}/previews", s.listPreviews)
	m.HandleFunc("POST /api/v1/applications/{id}/previews", s.createPreview)
	m.HandleFunc("GET /api/v1/previews/{preview}", s.preview)
	m.HandleFunc("DELETE /api/v1/previews/{preview}", s.deletePreview)
}
func (s *Server) listPreviews(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > 64 {
		problem(w, 400, "invalid_cursor", "Invalid preview cursor")
		return
	}
	items, err := s.Store.ApplicationPreviews(r.Context(), a.ID, cursor)
	if err != nil {
		failure(w, err)
		return
	}
	next := ""
	if len(items) > 50 {
		items = items[:50]
		next = items[49].ID
	}
	write(w, 200, map[string]any{"items": items, "next_cursor": next})
}
func (s *Server) createPreview(w http.ResponseWriter, r *http.Request) {
	parent, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in struct {
		Name                   string            `json:"name"`
		Branch                 string            `json:"branch"`
		TTLHours               int               `json:"ttl_hours"`
		ExpectedParentRevision *int64            `json:"expected_parent_revision"`
		TOML                   string            `json:"toml"`
		Spec                   *spec.Application `json:"spec"`
		DiscardOnExpiry        bool              `json:"discard_on_expiry"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !in.DiscardOnExpiry || in.ExpectedParentRevision == nil {
		problem(w, 400, "review_required", "Review the parent revision and acknowledge that preview data is deleted at expiry")
		return
	}
	var next spec.Application
	var err error
	if (in.TOML == "") == (in.Spec == nil) {
		problem(w, 400, "invalid_request", "Supply exactly one preview TOML or spec")
		return
	}
	if in.Spec != nil {
		next = *in.Spec
	} else {
		next, err = spec.Parse([]byte(in.TOML))
		if err != nil {
			problem(w, 400, "invalid_spec", err.Error())
			return
		}
	}
	d, err := s.Store.AcceptPreview(r.Context(), who(r), parent, next, store.InitialPreview{Name: in.Name, Branch: in.Branch, ParentRevision: *in.ExpectedParentRevision, TTLHours: in.TTLHours}, r.Header.Get("Idempotency-Key"))
	if err != nil {
		authFailure(w, err)
		return
	}
	// The preview ID is part of the deterministic application name, and both
	// records were committed atomically with the initial durable deployment.
	p, err := s.Store.Preview(r.Context(), d.Spec.Name[len("preview-"):])
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, map[string]any{"preview": p, "deployment": d})
}
func (s *Server) authorizedPreview(w http.ResponseWriter, r *http.Request) (store.Preview, bool) {
	p, err := s.Store.Preview(r.Context(), r.PathValue("preview"))
	if err != nil {
		failure(w, err)
		return p, false
	}
	if !who(r).Allows("deployments:read", p.Project, p.Environment, "") {
		authFailure(w, store.ErrForbidden)
		return p, false
	}
	return p, true
}
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authorizedPreview(w, r)
	if ok {
		write(w, 200, p)
	}
}
func (s *Server) deletePreview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authorizedPreview(w, r)
	if !ok {
		return
	}
	var in struct {
		Confirmation string `json:"confirmation"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Store.RequestPreviewDeletion(r.Context(), who(r), p.ID, in.Confirmation); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 202, map[string]string{"status": "deleting"})
}
