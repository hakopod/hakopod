package api

import (
	"errors"
	"github.com/hakopod/hakopod/internal/store"
	"net/http"
)

func (s *Server) registerProfileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/auth/profile", s.getProfile)
	mux.HandleFunc("PATCH /api/v1/auth/profile", s.updateProfile)
	mux.HandleFunc("PUT /api/v1/teams/{id}/members/{user}/username", s.setTeamUsername)
}
func profileError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrInput) {
		problem(w, 400, "invalid_profile", err.Error())
	} else {
		failure(w, err)
	}
}
func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	result, err := s.Store.Profile(r.Context(), who(r))
	if err != nil {
		profileError(w, err)
		return
	}
	write(w, 200, result)
}
func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request) {
	var in store.ProfileInput
	if !decode(w, r, &in) {
		return
	}
	result, err := s.Store.UpdateProfile(r.Context(), who(r), in)
	if err != nil {
		profileError(w, err)
		return
	}
	write(w, 200, result)
}
func (s *Server) setTeamUsername(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
	}
	if !decode(w, r, &in) {
		return
	}
	result, err := s.Store.SetTeamUsername(r.Context(), who(r), r.PathValue("id"), r.PathValue("user"), in.Username)
	if err != nil {
		profileError(w, err)
		return
	}
	write(w, 200, result)
}
