package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

// Staged changes: one shared draft per application collects edits until a
// person reviews and deploys them together. Staging runs the same validation
// as a plan; deploying a draft goes through the ordinary plan and accept path.
func (s *Server) registerDraftRoutes(routes *http.ServeMux) {
	routes.HandleFunc("GET /api/v1/applications/{id}/draft", s.getDraft)
	routes.HandleFunc("PUT /api/v1/applications/{id}/draft", s.stageDraft)
	routes.HandleFunc("DELETE /api/v1/applications/{id}/draft", s.discardDraft)
	routes.HandleFunc("POST /api/v1/applications/{id}/draft/plan", s.planDraft)
	routes.HandleFunc("POST /api/v1/applications/{id}/draft/deploy", s.deployDraft)
}

type draftView struct {
	ApplicationID   string             `json:"application_id"`
	Revision        int64              `json:"revision"`
	BaseRevision    int64              `json:"base_revision"`
	CurrentRevision int64              `json:"current_revision"`
	Stale           bool               `json:"stale"`
	Conflicts       []string           `json:"conflicts"`
	Spec            spec.Application   `json:"spec"`
	Changes         []spec.Change      `json:"changes"`
	Entries         []store.DraftEntry `json:"entries"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
}

// currentDraft returns the draft carried forward onto the application's current
// revision. A draft that no longer changes anything is removed.
func (s *Server) currentDraft(ctx context.Context, app store.Application) (draftView, store.Draft, bool, error) {
	d, err := s.Store.Draft(ctx, app.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return draftView{}, d, false, nil
	}
	if err != nil {
		return draftView{}, d, false, err
	}
	next, conflicts := d.Spec, []string{}
	if d.BaseRevision != app.Revision {
		if next, conflicts, err = spec.Rebase(d.BaseSpec, app.Spec, d.Spec); err != nil {
			return draftView{}, d, false, err
		}
		if conflicts == nil {
			conflicts = []string{}
		}
	}
	changes := spec.Diff(&app.Spec, next)
	if len(changes) == 0 && len(conflicts) == 0 {
		// Everything staged is already live, or was undone: nothing is pending.
		if err := s.Store.DiscardDraft(ctx, app.ID, d.Revision); err != nil && !errors.Is(err, store.ErrConflict) {
			return draftView{}, d, false, err
		}
		return draftView{}, d, false, nil
	}
	return draftView{
		ApplicationID: app.ID, Revision: d.Revision, BaseRevision: d.BaseRevision, CurrentRevision: app.Revision,
		Stale: d.BaseRevision != app.Revision, Conflicts: conflicts, Spec: next, Changes: changes,
		Entries: d.Entries, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}, d, true, nil
}

func (s *Server) getDraft(w http.ResponseWriter, r *http.Request) {
	app, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	view, _, exists, err := s.currentDraft(r.Context(), app)
	if err != nil {
		failure(w, err)
		return
	}
	if !exists {
		problem(w, 404, "no_draft", "No changes are staged for this application.")
		return
	}
	write(w, 200, view)
}

type draftInput struct {
	input
	ExpectedDraftRevision int64 `json:"expected_draft_revision"`
}

func (s *Server) stageDraft(w http.ResponseWriter, r *http.Request) {
	app, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in draftInput
	if !decode(w, r, &in) {
		return
	}
	if len(in.EnvFiles) != 0 || len(in.DeleteServiceVolumes) != 0 {
		problem(w, 400, "not_stageable", "Changes that upload env file values or delete volumes cannot be staged. Deploy them now.")
		return
	}
	in.Project, in.Environment, in.ExpectedRevision = app.Project, app.Environment, nil
	next, existing, ok := s.prepare(w, r, in.input, "deployments:write")
	if !ok {
		return
	}
	if existing == nil || existing.ID != app.ID {
		problem(w, 400, "invalid_spec", "The staged configuration must keep this application's name.")
		return
	}
	view, draft, exists, err := s.currentDraft(r.Context(), app)
	if err != nil {
		failure(w, err)
		return
	}
	if exists && len(view.Conflicts) > 0 {
		problem(w, 409, "draft_conflict", "Staged changes conflict with a newer deployment. Discard them or deploy the newer configuration first.")
		return
	}
	// An out-of-date draft that merged cleanly is re-based: the edit was made
	// against the merged view, so the current revision is its new base.
	baseRevision, base, previous := app.Revision, app.Spec, app.Spec
	if exists {
		previous = view.Spec
		if !view.Stale {
			baseRevision, base = draft.BaseRevision, draft.BaseSpec
		}
	}
	expected := in.ExpectedDraftRevision
	if !exists && expected != 0 && draft.Revision == expected {
		// The draft the caller saw was cleared because it became a no-op.
		expected = 0
	}
	changes := spec.Diff(&previous, next)
	if len(changes) == 0 && exists {
		write(w, 200, view)
		return
	}
	if _, err := s.Store.StageDraft(r.Context(), who(r), app, baseRevision, base, next, in.Provenance, expected, changes); err != nil {
		failure(w, err)
		return
	}
	view, _, exists, err = s.currentDraft(r.Context(), app)
	if err != nil {
		failure(w, err)
		return
	}
	if !exists {
		problem(w, 404, "no_draft", "The staged configuration matches the current revision; nothing is pending.")
		return
	}
	write(w, 200, view)
}

func (s *Server) discardDraft(w http.ResponseWriter, r *http.Request) {
	app, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in draftDeployInput
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedDraftRevision < 1 {
		problem(w, 400, "missing_revision", "expected_draft_revision is required")
		return
	}
	if err := s.Store.DiscardDraft(r.Context(), app.ID, in.ExpectedDraftRevision); err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]bool{"discarded": true})
}

type draftDeployInput struct {
	ExpectedDraftRevision int64  `json:"expected_draft_revision"`
	ExpectedRevision      *int64 `json:"expected_revision,omitempty"`
}

// readyDraft loads the draft for planning or deploying and rejects one that
// changed since the caller reviewed it or that conflicts with a newer release.
func (s *Server) readyDraft(w http.ResponseWriter, r *http.Request, app store.Application, expected int64) (draftView, store.Draft, bool) {
	view, draft, exists, err := s.currentDraft(r.Context(), app)
	if err != nil {
		failure(w, err)
		return view, draft, false
	}
	if !exists {
		problem(w, 404, "no_draft", "No changes are staged for this application.")
		return view, draft, false
	}
	if view.Revision != expected {
		problem(w, 409, "draft_changed", "The staged changes were updated. Review them again.")
		return view, draft, false
	}
	if len(view.Conflicts) > 0 {
		problem(w, 409, "draft_conflict", "Staged changes conflict with a newer deployment. Discard them or deploy the newer configuration first.")
		return view, draft, false
	}
	return view, draft, true
}

func (s *Server) planDraft(w http.ResponseWriter, r *http.Request) {
	app, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in draftDeployInput
	if !decode(w, r, &in) {
		return
	}
	view, draft, ok := s.readyDraft(w, r, app, in.ExpectedDraftRevision)
	if !ok {
		return
	}
	next := view.Spec
	s.planInput(w, r, input{Project: app.Project, Environment: app.Environment, Spec: &next, Provenance: draft.Provenance},
		map[string]any{"draft_revision": view.Revision})
}

func (s *Server) deployDraft(w http.ResponseWriter, r *http.Request) {
	app, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in draftDeployInput
	if !decode(w, r, &in) {
		return
	}
	// A retried request whose draft was already deployed returns that deployment.
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		var id string
		err := s.Store.Pool.QueryRow(r.Context(), "SELECT id FROM deployments WHERE identity_id=$1 AND idempotency_key=$2 AND application_id=$3", who(r).ID, key, app.ID).Scan(&id)
		if err == nil {
			d, err := s.Store.Deployment(r.Context(), id)
			if err != nil {
				failure(w, err)
				return
			}
			write(w, 202, d)
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			failure(w, err)
			return
		}
	}
	view, draft, ok := s.readyDraft(w, r, app, in.ExpectedDraftRevision)
	if !ok {
		return
	}
	next := view.Spec
	d, ok := s.deployInput(w, r, input{Project: app.Project, Environment: app.Environment, Spec: &next, Provenance: draft.Provenance, ExpectedRevision: in.ExpectedRevision})
	if !ok {
		return
	}
	// The release is accepted; a draft staged again in the meantime stays for review.
	if err := s.Store.DiscardDraft(r.Context(), app.ID, view.Revision); err != nil && !errors.Is(err, store.ErrConflict) {
		failure(w, err)
		return
	}
	write(w, 202, d)
}
