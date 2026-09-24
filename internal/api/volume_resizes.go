package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) registerVolumeResizeRoutes(routes *http.ServeMux) {
	routes.HandleFunc("POST /api/v1/applications/{id}/volume-resizes/plan", s.planVolumeResize)
	routes.HandleFunc("POST /api/v1/applications/{id}/volume-resizes", s.startVolumeResize)
	routes.HandleFunc("GET /api/v1/applications/{id}/volume-resizes", s.listVolumeResizes)
	routes.HandleFunc("POST /api/v1/applications/{id}/volume-resizes/{resize}/{action}", s.volumeResizeAction)
}

type volumeResizeInput struct {
	Claim            string `json:"claim"`
	SizeGiB          int64  `json:"size_gib"`
	ExpectedRevision int64  `json:"expected_revision"`
	SourceUID        string `json:"source_uid,omitempty"`
	ConfirmDowntime  bool   `json:"confirm_downtime,omitempty"`
}

func (s *Server) authorizeVolumeResize(w http.ResponseWriter, r *http.Request) (store.Application, bool) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return a, false
	}
	p := who(r)
	if !p.CanManageApplication(a.Project, a.Environment, a.Name) {
		problem(w, 403, "forbidden", "Application management permission is required to resize volumes.")
		return a, false
	}
	if s.Store.AuthorizeRetainedCleanup != nil {
		if err := s.Store.AuthorizeRetainedCleanup(r.Context(), p, a.Project, a.Environment); err != nil {
			failure(w, err)
			return a, false
		}
	}
	return a, true
}
func (s *Server) resizePlan(w http.ResponseWriter, r *http.Request) (store.Application, volumeResizeInput, spec.VolumeResize, cluster.VolumeResizeJournal, bool) {
	var in volumeResizeInput
	var plan spec.VolumeResize
	var state cluster.VolumeResizeJournal
	if !decode(w, r, &in) {
		return store.Application{}, in, plan, state, false
	}
	a, ok := s.authorizeVolumeResize(w, r)
	if !ok {
		return a, in, plan, state, false
	}
	if in.ExpectedRevision != a.Revision {
		failure(w, store.ErrConflict)
		return a, in, plan, state, false
	}
	source, err := s.pinnedApplication(r.Context(), a)
	if err != nil {
		transferFailure(w, err)
		return a, in, plan, state, false
	}
	next, plan, err := spec.ResizeVolume(source, in.Claim, in.SizeGiB, store.ResizeID(who(r).ID, r.Header.Get("Idempotency-Key")))
	if err != nil {
		transferFailure(w, err)
		return a, in, plan, state, false
	}
	if !s.validateDeliveryPlan(w, r, a.Project, a.Environment, next, &a) {
		return a, in, plan, state, false
	}
	if s.Cluster == nil {
		problem(w, 503, "resize_unavailable", "This engine runtime does not support volume migration.")
		return a, in, plan, state, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	state, err = s.Cluster.InspectVolumeResize(ctx, cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Revision: a.Revision, Spec: source}, plan)
	if err != nil {
		transferFailure(w, err)
		return a, in, plan, state, false
	}
	return a, in, plan, state, true
}
func (s *Server) planVolumeResize(w http.ResponseWriter, r *http.Request) {
	a, _, plan, state, ok := s.resizePlan(w, r)
	if !ok {
		return
	}
	write(w, 200, map[string]any{"claim": plan.Claim, "old_gib": plan.OldGiB, "size_gib": plan.SizeGiB, "temporary_gib": plan.SizeGiB, "peak_gib": plan.SizeGiB + plan.OldGiB, "services": plan.Services, "source_uid": string(state.SourceUID), "expected_revision": a.Revision, "warnings": []string{"All listed services stop while the filesystem is checked and copied. Shrinking proceeds only if the data fits with free-space headroom.", "The original remains retained and charged after switching. Confirm its deletion after checking your application to reclaim that storage.", "The resized filesystem uses a new named volume. Keep the resulting volume and mount settings in your Git configuration before deploying it again."}})
}
func (s *Server) startVolumeResize(w http.ResponseWriter, r *http.Request) {
	if n := len(r.Header.Get("Idempotency-Key")); n < 8 || n > 128 {
		problem(w, 400, "invalid_request", "Idempotency-Key must contain 8–128 characters.")
		return
	}
	// Replay an accepted operation before inspecting a volume that maintenance
	// may already have stopped or replaced.
	id := store.ResizeID(who(r).ID, r.Header.Get("Idempotency-Key"))
	if prior, err := s.Store.VolumeResize(r.Context(), id); err == nil {
		var in volumeResizeInput
		if !decode(w, r, &in) {
			return
		}
		a, ok := s.authorizeVolumeResize(w, r)
		if !ok {
			return
		}
		var accepted cluster.VolumeResizeJournal
		if json.Unmarshal(prior.Runtime, &accepted) != nil || in.SourceUID == "" || in.SourceUID != string(accepted.SourceUID) {
			failure(w, store.ErrConflict)
			return
		}
		if prior.ApplicationID != a.ID || prior.Claim != in.Claim || prior.SizeGiB != in.SizeGiB || prior.ExpectedRevision != in.ExpectedRevision || !in.ConfirmDowntime {
			failure(w, store.ErrConflict)
			return
		}
		write(w, 202, prior)
		return
	}
	a, in, _, state, ok := s.resizePlan(w, r)
	if !ok {
		return
	}
	if !in.ConfirmDowntime || in.SourceUID == "" || in.SourceUID != string(state.SourceUID) {
		problem(w, 409, "review_required", "Review the current volume and confirm service downtime before resizing.")
		return
	}
	raw, _ := json.Marshal(state)
	result, err := s.Store.StartVolumeResize(r.Context(), who(r), a.ID, in.Claim, in.SizeGiB, in.ExpectedRevision, r.Header.Get("Idempotency-Key"), raw)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, result)
}
func (s *Server) listVolumeResizes(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	items, err := s.Store.VolumeResizes(r.Context(), a.ID)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items})
}
func (s *Server) volumeResizeAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ConfirmClaim string `json:"confirm_claim"`
	}
	if !decode(w, r, &in) {
		return
	}
	a, ok := s.authorizeVolumeResize(w, r)
	if !ok {
		return
	}
	if err := s.Store.ResizeAction(r.Context(), who(r), a.ID, r.PathValue("resize"), r.PathValue("action"), in.ConfirmClaim); err != nil {
		failure(w, err)
		return
	}
	write(w, 202, map[string]string{"status": "accepted"})
}
