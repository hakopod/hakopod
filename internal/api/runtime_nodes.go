package api

import (
	"context"
	"net/http"
	"time"
)

func (s *Server) nodeAction(w http.ResponseWriter, r *http.Request, drain bool) {
	if !admin(w, r) {
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	var in struct {
		ExpectedResourceVersion string `json:"expected_resource_version"`
		Unschedulable           *bool  `json:"unschedulable,omitempty"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedResourceVersion == "" || len(in.ExpectedResourceVersion) > 128 || !drain && in.Unschedulable == nil || drain && in.Unschedulable != nil {
		problem(w, 400, "invalid_request", "provide expected_resource_version and, for cordon, unschedulable")
		return
	}
	release, err := s.Store.LockNodeActions(r.Context(), who(r))
	if err != nil {
		failure(w, err)
		return
	}
	defer release()
	action := "node.cordon"
	if drain {
		action = "node.drain"
	}
	name := r.PathValue("name")
	if err := s.Store.RuntimeAudit(r.Context(), who(r), action+".requested", name, map[string]any{"resource_version": in.ExpectedResourceVersion, "unschedulable": in.Unschedulable}); err != nil {
		failure(w, err)
		return
	}
	if drain {
		value, err := s.Cluster.DrainNode(r.Context(), name, in.ExpectedResourceVersion)
		if err != nil {
			problem(w, 409, "node_action_blocked", err.Error())
			return
		}
		if err = s.Store.RuntimeAudit(r.Context(), who(r), action+".observed", name, value); err != nil {
			failure(w, err)
			return
		}
		write(w, 200, value)
		return
	}
	value, err := s.Cluster.CordonNode(r.Context(), name, in.ExpectedResourceVersion, *in.Unschedulable)
	if err != nil {
		problem(w, 409, "node_action_blocked", err.Error())
		return
	}
	if err = s.Store.RuntimeAudit(r.Context(), who(r), action+".observed", name, value); err != nil {
		failure(w, err)
		return
	}
	write(w, 200, value)
}
func (s *Server) cordonNode(w http.ResponseWriter, r *http.Request) { s.nodeAction(w, r, false) }
func (s *Server) drainNode(w http.ResponseWriter, r *http.Request)  { s.nodeAction(w, r, true) }
func (s *Server) nodeEnrollments(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	value, err := s.Cluster.Enrollments(r.Context())
	if err != nil {
		problem(w, 503, "unavailable", "node enrollment state is unavailable")
		return
	}
	write(w, 200, value)
}
func (s *Server) createNodeEnrollment(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	var in struct {
		TTLMinutes int `json:"ttl_minutes"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.TTLMinutes == 0 {
		in.TTLMinutes = 30
	}
	if in.TTLMinutes < 5 || in.TTLMinutes > 60 {
		problem(w, 400, "invalid_request", "ttl_minutes must be between 5 and 60")
		return
	}
	if err := s.Store.RuntimeAudit(r.Context(), who(r), "node.enrollment.requested", "cluster", map[string]any{"ttl_minutes": in.TTLMinutes}); err != nil {
		failure(w, err)
		return
	}
	if err := s.Store.PruneExpiredEnrollments(r.Context(), who(r)); err != nil {
		failure(w, err)
		return
	}
	value, err := s.Cluster.CreateEnrollment(r.Context(), time.Duration(in.TTLMinutes)*time.Minute)
	if err != nil {
		problem(w, 409, "enrollment_unavailable", err.Error())
		return
	}
	if _, err = s.Store.PutRuntimeResource(r.Context(), who(r), "enrollment", "", "", value.ID, 0, map[string]any{"expires_at": value.ExpiresAt, "created_at": value.CreatedAt, "server": value.Server}); err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = s.Cluster.RevokeEnrollment(ctx, value.ID)
		cancel()
		failure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, 201, value)
}
func (s *Server) revokeNodeEnrollment(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	id := r.PathValue("id")
	if err := s.Store.RuntimeAudit(r.Context(), who(r), "node.enrollment.revoke-requested", id, map[string]any{}); err != nil {
		failure(w, err)
		return
	}
	if err := s.Cluster.RevokeEnrollment(r.Context(), id); err != nil {
		problem(w, 409, "enrollment_revoke_failed", err.Error())
		return
	}
	if value, err := s.Store.RuntimeResource(r.Context(), "enrollment", "", "", id); err == nil {
		if err = s.Store.DeleteRuntimeResource(r.Context(), who(r), "enrollment", "", "", id, value.Revision); err != nil {
			failure(w, err)
			return
		}
	}
	write(w, 200, map[string]bool{"revoked": true})
}
