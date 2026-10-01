package api

import (
	"context"
	"net/http"
	"time"
)

func (s *Server) databaseMetricHistory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	history, err := s.Store.DatabaseMetricHistory(ctx, who(r), r.PathValue("id"), r.URL.Query().Get("range"))
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, history)
}
