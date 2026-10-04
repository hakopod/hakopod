package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/nativeacceptance"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/hakopod/hakopod/internal/store"
)

type platformRecoveryRequest struct {
	platformbackup.Intent
	Review            platformbackup.Review `json:"review"`
	ConfirmTargetName string                `json:"confirm_target_name"`
}

func (s *Server) platformRecoveryQualified(ctx context.Context, project, environment, kind string) bool {
	native := nativeacceptance.Recovery(project, environment, kind)
	released := kind == "supabase" && s.ManagedPlatformRecoveryQualified || kind == "neon" && s.ManagedNeonRecoveryQualified
	if !native && (!released || s.ValidateManagedPlatformRecovery == nil) {
		return false
	}
	if s.ValidateManagedPlatformRecovery != nil {
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := s.ValidateManagedPlatformRecovery(bounded, kind); err != nil {
			return false
		}
	}
	return true
}

func (s *Server) validatePlatformRecoveryExecution(ctx context.Context, op platformbackup.Operation, kind string) error {
	if !s.platformRecoveryQualified(ctx, op.Project, op.Environment, kind) {
		return fmt.Errorf("managed platform recovery qualification is unavailable or changed")
	}
	return nil
}

func (s *Server) registerPlatformRecoveryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/managed-platforms/{id}/recovery-operations", s.platformRecoveryOperations)
	mux.HandleFunc("GET /api/v1/managed-platform-recovery-operations/{id}", s.platformRecoveryOperation)
	mux.HandleFunc("POST /api/v1/managed-platform-recovery/reviews", s.reviewPlatformRecovery)
	mux.HandleFunc("POST /api/v1/managed-platform-recovery/operations", s.acceptPlatformRecovery)
	mux.HandleFunc("POST /api/v1/managed-platform-recovery-operations/{id}/cancel", s.cancelPlatformRecovery)
}

func (s *Server) platformRecoveryOperations(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.PlatformRecoveryOperations(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items})
}
func (s *Server) platformRecoveryOperation(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.PlatformRecoveryOperation(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, item)
}

func (s *Server) authorizePlatformRecoveryRequest(w http.ResponseWriter, r *http.Request, in platformRecoveryRequest) (store.Principal, bool) {
	p := who(r)
	if err := in.Intent.Validate(); err != nil {
		problem(w, 400, "invalid_request", "provide a valid managed platform recovery source and destination")
		return p, false
	}
	if !p.AllowsManagedPlatform(in.Project, in.Environment, true) {
		failure(w, store.ErrForbidden)
		return p, false
	}
	return p, true
}

func (s *Server) validatePlatformRecoveryRequest(w http.ResponseWriter, r *http.Request, in platformRecoveryRequest) (store.Principal, bool) {
	p, ok := s.authorizePlatformRecoveryRequest(w, r, in)
	if !ok {
		return p, false
	}
	source, err := s.Store.ManagedPlatform(r.Context(), p, in.SourcePlatformID, true)
	if err != nil {
		failure(w, err)
		return p, false
	}
	if source.Project != in.Project || source.Environment != in.Environment || source.Revision != in.ExpectedSourceRevision || source.Spec.Kind != "supabase" && source.Spec.Kind != "neon" {
		problem(w, 409, "conflict", "review the current source platform revision")
		return p, false
	}
	if !s.platformRecoveryQualified(r.Context(), in.Project, in.Environment, source.Spec.Kind) {
		problem(w, 503, "managed_platform_recovery_unavailable", source.Spec.Kind+" recovery requires a qualified runtime and current operator storage approval")
		return p, false
	}
	if in.Kind == "restore" {
		target, err := s.Store.ManagedPlatform(r.Context(), p, in.TargetPlatformID, true)
		if err != nil {
			failure(w, err)
			return p, false
		}
		if target.Project != in.Project || target.Environment != in.Environment || target.Revision != in.ExpectedTargetRevision || target.Spec.Kind != source.Spec.Kind || target.Spec.Name != in.ConfirmTargetName {
			problem(w, 409, "conflict", "review and confirm a separate target platform of the same kind")
			return p, false
		}
	}
	return p, true
}

func (s *Server) reviewPlatformRecovery(w http.ResponseWriter, r *http.Request) {
	var in platformRecoveryRequest
	if !decode(w, r, &in) {
		return
	}
	p, ok := s.authorizePlatformRecoveryRequest(w, r, in)
	if !ok {
		return
	}
	if s.Store != nil && s.Store.ManagedCloud && (s.Store.ManagedPlatformCapacityBudget == nil || s.Store.AdmitManagedPlatform == nil) {
		problem(w, 503, "managed_platform_capacity_unavailable", "managed platform recovery remains unavailable until durable capacity admission is configured")
		return
	}
	if !s.platformRecoveryQualified(r.Context(), in.Project, in.Environment, "supabase") && !s.platformRecoveryQualified(r.Context(), in.Project, in.Environment, "neon") {
		problem(w, 503, "managed_platform_recovery_unavailable", "managed platform recovery remains unavailable until native backup and restore acceptance passes")
		return
	}
	p, ok = s.validatePlatformRecoveryRequest(w, r, in)
	if !ok {
		return
	}
	review, err := s.Store.SavePlatformRecoveryReview(r.Context(), p, in.Intent)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, review)
}
func (s *Server) acceptPlatformRecovery(w http.ResponseWriter, r *http.Request) {
	var in platformRecoveryRequest
	if !decode(w, r, &in) {
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if len(idem) < 8 || len(idem) > 128 {
		problem(w, 400, "invalid_request", "provide an Idempotency-Key of 8 to 128 characters")
		return
	}
	p, ok := s.authorizePlatformRecoveryRequest(w, r, in)
	if !ok {
		return
	}
	if s.Store != nil && s.Store.ManagedCloud && (s.Store.ManagedPlatformCapacityBudget == nil || s.Store.AdmitManagedPlatform == nil) {
		problem(w, 503, "managed_platform_capacity_unavailable", "managed platform recovery remains unavailable until durable capacity admission is configured")
		return
	}
	if !s.platformRecoveryQualified(r.Context(), in.Project, in.Environment, "supabase") && !s.platformRecoveryQualified(r.Context(), in.Project, in.Environment, "neon") {
		problem(w, 503, "managed_platform_recovery_unavailable", "managed platform recovery remains unavailable until native backup and restore acceptance passes")
		return
	}
	p, ok = s.validatePlatformRecoveryRequest(w, r, in)
	if !ok {
		return
	}
	op, err := s.Store.AcceptPlatformRecovery(r.Context(), p, in.Intent, in.Review, idem)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusAccepted, op)
}
func (s *Server) cancelPlatformRecovery(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.CancelPlatformRecovery(r.Context(), who(r), r.PathValue("id")); err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusAccepted, map[string]bool{"cancel_requested": true})
}
