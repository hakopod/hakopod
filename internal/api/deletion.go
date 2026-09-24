package api

import (
	"context"
	"net/http"
	"time"
)

func (s *Server) registerDeletionRoutes(routes *http.ServeMux) {
	routes.HandleFunc("POST /api/v1/deployments/{id}/volume-cleanup", s.retryServiceVolumeCleanup)
	routes.HandleFunc("GET /api/v1/storage/retained", s.listRetainedStorage)
	routes.HandleFunc("DELETE /api/v1/storage/retained/{id}", s.deleteRetainedStorage)
	routes.HandleFunc("DELETE /api/v1/projects/{id}", s.deleteProject)
	routes.HandleFunc("DELETE /api/v1/applications/{id}", s.deleteApplication)
}
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var in struct {
		ConfirmName string `json:"confirm_name"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Store.DeleteEmptyProject(ctx, who(r), r.PathValue("id"), in.ConfirmName); err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]string{"status": "deleted"})
}
func (s *Server) deleteApplication(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeleteData       bool   `json:"delete_data"`
		ConfirmName      string `json:"confirm_name"`
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil {
		problem(w, 400, "review_required", "provide the reviewed application revision")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Store.DeleteEmptyApplication(ctx, who(r), r.PathValue("id"), *in.ExpectedRevision, in.ConfirmName, in.DeleteData); err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]string{"status": "deleted"})
}

func (s *Server) retryServiceVolumeCleanup(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.RetryServiceVolumeCleanup(r.Context(), who(r), r.PathValue("id")); err != nil {
		failure(w, err)
		return
	}
	write(w, 202, map[string]string{"status": "reclaiming"})
}
