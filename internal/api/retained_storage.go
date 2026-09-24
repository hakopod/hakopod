package api

import (
	"net/http"
)

func (s *Server) listRetainedStorage(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.RetainedApplications(r.Context(), who(r), r.URL.Query().Get("project"), r.URL.Query().Get("environment"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items})
}
func (s *Server) deleteRetainedStorage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ConfirmName string `json:"confirm_name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Store.RequestRetainedCleanup(r.Context(), who(r), r.PathValue("id"), in.ConfirmName); err != nil {
		failure(w, err)
		return
	}
	write(w, 202, map[string]string{"status": "deleting"})
}
