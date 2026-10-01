package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

// ManagedPlatformPlanner is trusted server configuration. It resolves pinned
// images and secret references; clients cannot supply runtime credentials or
// qualification evidence.
type ManagedPlatformPlanner interface {
	PlanManagedPlatform(context.Context, store.Principal, store.ManagedPlatform, int64, string) (managedplatform.Plan, error)
	SealManagedPlatformSnapshot(context.Context, store.Principal, store.ManagedPlatform, managedplatform.Plan, int64, string) ([]byte, error)
}

type managedPlatformIntent struct {
	ID               string                      `json:"id"`
	Project          string                      `json:"project"`
	Environment      string                      `json:"environment"`
	ExpectedRevision int64                       `json:"expected_revision"`
	Kind             string                      `json:"kind"`
	Spec             managedplatform.Spec        `json:"spec"`
	Review           store.ManagedPlatformReview `json:"review"`
	ConfirmName      string                      `json:"confirm_name"`
}

func (s *Server) registerManagedPlatformRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/managed-platforms", s.managedPlatforms)
	mux.HandleFunc("GET /api/v1/managed-platforms/{id}", s.managedPlatform)
	mux.HandleFunc("GET /api/v1/managed-platforms/{id}/operations", s.managedPlatformOperations)
	mux.HandleFunc("GET /api/v1/managed-platform-operations/{id}", s.managedPlatformOperation)
	mux.HandleFunc("POST /api/v1/managed-platforms/reviews", s.reviewManagedPlatform)
	mux.HandleFunc("POST /api/v1/managed-platforms/operations", s.acceptManagedPlatform)
	s.registerPlatformRecoveryRoutes(mux)
}

func (s *Server) managedPlatforms(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	project, environment := query.Get("project"), query.Get("environment")
	if query.Has("project") || query.Has("environment") {
		if len(query["project"]) != 1 || len(query["environment"]) != 1 || !validScope(project, environment) {
			problem(w, 400, "invalid_request", "provide both a project and environment, or omit both")
			return
		}
	}
	if (project == "") != (environment == "") {
		problem(w, 400, "invalid_request", "provide both a project and environment, or omit both")
		return
	}
	items, err := s.Store.ManagedPlatforms(r.Context(), who(r), project, environment)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items})
}
func (s *Server) managedPlatform(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.ManagedPlatform(r.Context(), who(r), r.PathValue("id"), false)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, item)
}
func (s *Server) managedPlatformOperations(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ManagedPlatformOperations(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items})
}
func (s *Server) managedPlatformOperation(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.ManagedPlatformOperation(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, item)
}

func (s *Server) reviewManagedPlatform(w http.ResponseWriter, r *http.Request) {
	var in managedPlatformIntent
	if !decode(w, r, &in) {
		return
	}
	if s.ManagedPlatformPlanner == nil {
		problem(w, 503, "managed_platform_unavailable", "managed platform planning is not configured")
		return
	}
	if !validScope(in.Project, in.Environment) || in.ExpectedRevision < 0 || in.Kind != "create" && in.Kind != "update" && in.Kind != "delete" {
		problem(w, 400, "invalid_request", "provide a valid scope, revision and operation kind")
		return
	}
	principal := who(r)
	if !principal.AllowsManagedPlatform(in.Project, in.Environment, true) {
		failure(w, store.ErrForbidden)
		return
	}
	if s.Store != nil && s.Store.ManagedCloud && in.Kind != "delete" {
		problem(w, 503, "managed_platform_capacity_unavailable", "managed platform changes remain unavailable until durable capacity admission is configured")
		return
	}
	item := store.ManagedPlatform{ID: in.ID, Project: in.Project, Environment: in.Environment, Spec: in.Spec}
	if in.Kind == "create" {
		if item.ID != "" || in.ExpectedRevision != 0 {
			problem(w, 409, "conflict", "new managed platforms require revision zero and no id")
			return
		}
		item.ID = store.NewID()
	} else {
		current, err := s.Store.ManagedPlatform(r.Context(), principal, in.ID, true)
		if err != nil {
			failure(w, err)
			return
		}
		if current.Project != in.Project || current.Environment != in.Environment || current.Revision != in.ExpectedRevision || in.ConfirmName != current.Spec.Name || in.Spec.Name != current.Spec.Name {
			problem(w, 409, "conflict", "review the current managed platform revision")
			return
		}
		item.Project, item.Environment = current.Project, current.Environment
		if in.Kind == "delete" {
			item.Spec = current.Spec
		}
	}
	plan, err := s.ManagedPlatformPlanner.PlanManagedPlatform(r.Context(), principal, item, in.ExpectedRevision, in.Kind)
	if err != nil {
		failure(w, err)
		return
	}
	if in.Kind != "delete" && (!plan.Capability.Available || !plan.Capability.ClusterQualified) {
		write(w, 200, map[string]any{"platform": item, "plan": plan, "review": nil, "blocked": true})
		return
	}
	review, err := s.Store.SaveManagedPlatformReview(r.Context(), principal, item, plan, in.ExpectedRevision, in.Kind)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"platform": item, "plan": plan, "review": review, "blocked": false})
}

func (s *Server) acceptManagedPlatform(w http.ResponseWriter, r *http.Request) {
	var in managedPlatformIntent
	if !decode(w, r, &in) {
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if len(idem) < 8 || len(idem) > 128 {
		problem(w, 400, "invalid_request", "provide an Idempotency-Key of 8 to 128 characters")
		return
	}
	if s.ManagedPlatformPlanner == nil {
		problem(w, 503, "managed_platform_unavailable", "managed platform planning is not configured")
		return
	}
	if !validScope(in.Project, in.Environment) || in.ExpectedRevision < 0 || in.Kind != "create" && in.Kind != "update" && in.Kind != "delete" {
		problem(w, 400, "invalid_request", "provide a valid scope, revision and operation kind")
		return
	}
	principal := who(r)
	if !principal.AllowsManagedPlatform(in.Project, in.Environment, true) {
		failure(w, store.ErrForbidden)
		return
	}
	if s.Store != nil && s.Store.ManagedCloud && in.Kind != "delete" {
		problem(w, 503, "managed_platform_capacity_unavailable", "managed platform changes remain unavailable until durable capacity admission is configured")
		return
	}
	item := store.ManagedPlatform{ID: in.ID, Project: in.Project, Environment: in.Environment, Spec: in.Spec}
	replayPlan, err := s.ManagedPlatformPlanner.PlanManagedPlatform(r.Context(), principal, item, in.ExpectedRevision, in.Kind)
	if err != nil {
		failure(w, err)
		return
	}
	replay, replayErr := s.Store.ManagedPlatformOperationReplay(r.Context(), principal, item, replayPlan, in.ExpectedRevision, idem, in.Kind)
	if replayErr == nil {
		write(w, http.StatusAccepted, replay)
		return
	}
	if !errors.Is(replayErr, pgx.ErrNoRows) {
		failure(w, replayErr)
		return
	}
	if in.Kind != "create" {
		current, getErr := s.Store.ManagedPlatform(r.Context(), principal, in.ID, true)
		if getErr != nil {
			failure(w, getErr)
			return
		}
		if current.Project != in.Project || current.Environment != in.Environment || current.Revision != in.ExpectedRevision || in.ConfirmName != current.Spec.Name || in.Spec.Name != current.Spec.Name || in.Kind == "delete" && !reflect.DeepEqual(in.Spec, current.Spec) {
			problem(w, 409, "conflict", "review and confirm the current managed platform")
			return
		}
		if in.Kind == "delete" {
			item.Spec = current.Spec
		}
	}
	plan, err := s.ManagedPlatformPlanner.PlanManagedPlatform(r.Context(), principal, item, in.ExpectedRevision, in.Kind)
	if err != nil {
		failure(w, err)
		return
	}
	if in.Kind != "delete" && (!plan.Capability.Available || !plan.Capability.ClusterQualified) {
		problem(w, 503, "managed_platform_unavailable", plan.Capability.Reason)
		return
	}
	if err = s.Store.ValidateManagedPlatformReview(r.Context(), principal, item, plan, in.Review, in.ExpectedRevision, in.Kind); err != nil {
		failure(w, err)
		return
	}
	sealed, err := s.ManagedPlatformPlanner.SealManagedPlatformSnapshot(r.Context(), principal, item, plan, in.ExpectedRevision, in.Kind)
	if err != nil {
		problem(w, 503, "managed_platform_unavailable", "managed platform runtime inputs could not be sealed")
		return
	}
	operation, err := s.Store.AcceptManagedPlatform(r.Context(), principal, item, plan, sealed, in.Review, in.ExpectedRevision, idem, in.Kind)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusAccepted, operation)
}

type ManagedPlatformRuntime interface {
	ReconcileManagedPlatform(context.Context, *store.Store, store.ManagedPlatformOperation) error
}
