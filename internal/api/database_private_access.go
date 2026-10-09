package api

import (
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

func (s *Server) databasePrivateAccess(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), false)
	if err != nil {
		failure(w, err)
		return
	}
	var in database.PrivateAccessInput
	if !decodeLimited(w, r, &in, 2048) {
		return
	}
	guide, err := database.PrivateAccess(d, in, s.Store.ManagedCloud, time.Now().UTC())
	if err != nil {
		problem(w, http.StatusBadRequest, "invalid_private_access", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, guide)
}
