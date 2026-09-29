package api

import (
	"context"
	"net/http"
	"time"
)

func (s *Server) actionsProviderHold(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authorizedActionsPool(w, r, "deployments:read")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	state, err := s.Store.ActionsProviderHold(ctx, who(r), p.ApplicationID, p.Service)
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, state)
}

func (s *Server) actionsProviderHoldRelease(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authorizedActionsPool(w, r, "deployments:write")
	if !ok {
		return
	}
	var in struct {
		HoldID      string `json:"hold_id"`
		Acknowledge bool   `json:"acknowledge"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Store.ReleaseActionsProviderHold(ctx, who(r), p.ApplicationID, p.Service, in.HoldID, in.Acknowledge); err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, map[string]string{"status": "released", "hold_id": in.HoldID})
}
