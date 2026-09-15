package api

import (
	"net/http"
)

type renameInput struct {
	Name     string `json:"display_name"`
	Revision int64  `json:"expected_metadata_revision"`
}

func (s *Server) renameApplication(w http.ResponseWriter, r *http.Request) {
	var in renameInput
	if !decode(w, r, &in) {
		return
	}
	a, err := s.Store.RenameApplication(r.Context(), who(r), r.PathValue("id"), r.PathValue("service"), in.Name, in.Revision)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, a)
}
func (s *Server) renameProject(w http.ResponseWriter, r *http.Request) {
	var in renameInput
	if !decode(w, r, &in) {
		return
	}
	err := s.Store.RenameProject(r.Context(), who(r), r.PathValue("id"), in.Name, in.Revision)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]string{"status": "renamed"})
}
