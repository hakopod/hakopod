package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

func (s *Server) oracleSwitchoverPlan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TargetMember string `json:"target_member"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.TargetMember == "" || len(in.TargetMember) > 253 {
		problem(w, 400, "invalid_request", "choose a physical standby member")
		return
	}
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "database_controller_unavailable", "The database controller is unavailable.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	plan, err := s.Cluster.ReviewOracleSwitchover(ctx, d, in.TargetMember)
	if err != nil {
		problem(w, 409, "database_switchover_unavailable", err.Error())
		return
	}
	id, err := s.Store.SaveDatabaseReview(ctx, who(r), d, "switchover", plan, plan.ExpiresAt)
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, map[string]any{"id": id, "plan": plan, "warnings": []string{"Existing connections will close while the primary role changes. Applications must reconnect.", "This is a graceful switchover. Every database member must remain healthy; forced failover is unavailable."}})
}

func (s *Server) oracleSwitchover(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ReviewID         string `json:"review_id"`
		ExpectedRevision int64  `json:"expected_revision"`
		ConfirmName      string `json:"confirm_name"`
	}
	if !decode(w, r, &in) {
		return
	}
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	if in.ConfirmName != d.Spec.Name {
		problem(w, 400, "invalid_request", "confirm_name must match the database name; connections will close during switchover")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "database_controller_unavailable", "The database controller is unavailable.")
		return
	}
	if err = s.Cluster.DatabaseControllerAvailable(r.Context(), d.Spec); err != nil {
		problem(w, 503, "database_controller_unavailable", err.Error())
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	op, err := s.Store.AcceptOracleSwitchover(r.Context(), who(r), d.ID, in.ExpectedRevision, in.ReviewID, idem)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusAccepted, op)
}

func (s *Server) reconcileOracleSwitchover(ctx context.Context, d database.Resource, op database.Operation, before func() error, finish func(string, string, string, database.Observation)) {
	if op.Switchover == nil || op.Kind != "switchover" {
		finish("failed", "review", "The Oracle switchover review is missing.", d.Observation)
		return
	}
	if op.Phase == "accepted" && !op.Switchover.ExpiresAt.After(time.Now()) {
		finish("failed", "review", "The switchover review expired before it started. Review the current topology again.", d.Observation)
		return
	}
	if err := s.Cluster.RequestOracleSwitchover(ctx, d, *op.Switchover, before); err != nil {
		if errors.Is(err, database.ErrOracleSwitchoverReviewChanged) {
			finish("failed", "review", "The Oracle topology changed before the request was submitted. Review the current topology again.", d.Observation)
			return
		}
		if errors.Is(err, database.ErrOracleSwitchoverFailed) {
			finish("failed", "switchover", "An unresolved Oracle request no longer matches the current topology. Routing remains closed for recovery review.", d.Observation)
			return
		}
		// The request may already have reached Kubernetes before a timeout.
		// Retry its durable token; never invent a new operation or reopen routing.
		finish("queued", "switchover-checking", "Waiting for the reviewed Oracle topology and broker request. Application routing stays closed after the request starts.", d.Observation)
		return
	}
	done, err := s.Cluster.OracleSwitchoverComplete(ctx, d, *op.Switchover, before)
	if errors.Is(err, database.ErrOracleSwitchoverFailed) {
		finish("failed", "switchover", "The Oracle broker could not complete the switchover. Routing remains closed until native recovery is reviewed.", d.Observation)
		return
	}
	if err != nil {
		finish("queued", "switchover-verifying", "Waiting for verified Oracle roles, replication and encrypted connections.", d.Observation)
		return
	}
	if !done {
		finish("queued", "switching-primary", "The Oracle broker is changing the primary role.", d.Observation)
		return
	}
	observed, err := s.Cluster.ObserveDatabase(ctx, d)
	if err != nil || observed.Status != "ready" {
		finish("queued", "switchover-verifying", "Waiting for the new primary endpoint to become ready.", d.Observation)
		return
	}
	if err = before(); err != nil {
		return
	}
	finish("succeeded", "ready", "", observed)
}

func (s *Server) oracleSwitchoverRetry(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OperationID      string `json:"operation_id"`
		ExpectedRevision int64  `json:"expected_revision"`
		ConfirmName      string `json:"confirm_name"`
	}
	if !decode(w, r, &in) {
		return
	}
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	if in.ConfirmName != d.Spec.Name {
		problem(w, 400, "invalid_request", "confirm_name must match the database name; retry resumes the same reviewed target")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "database_controller_unavailable", "The database controller is unavailable.")
		return
	}
	if err = s.Cluster.DatabaseControllerAvailable(r.Context(), d.Spec); err != nil {
		problem(w, 503, "database_controller_unavailable", err.Error())
		return
	}
	op, err := s.Store.RetryOracleSwitchover(r.Context(), who(r), d.ID, in.OperationID, in.ExpectedRevision)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusAccepted, op)
}
