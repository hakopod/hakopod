package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

type kubernetesPatchSettingsView struct {
	store.KubernetesPatchSettings
	Revision int64 `json:"revision"`
}

type kubernetesPatchSettingsInput struct {
	store.KubernetesPatchSettings
	ExpectedRevision int64 `json:"expected_revision"`
}

func (s *Server) getKubernetesPatchSettings(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	v, rev, err := s.Store.KubernetesPatchSettings(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, kubernetesPatchSettingsView{v, rev})
}

func (s *Server) putKubernetesPatchSettings(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var in kubernetesPatchSettingsInput
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision < 0 {
		problem(w, 400, "invalid_request", "expected_revision must be zero for first-time settings or the revision you loaded")
		return
	}
	rev, err := s.Store.PutKubernetesPatchSettings(r.Context(), in.KubernetesPatchSettings, in.ExpectedRevision)
	if err != nil {
		failure(w, err)
		return
	}
	// The setting is saved; an audit failure must not report it as lost.
	_, _ = s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'settings.kubernetes_patches','installation',$3)", who(r).ID, who(r).KeyID, store.JSON(map[string]bool{"enabled": in.Enabled}))
	write(w, 200, kubernetesPatchSettingsView{in.KubernetesPatchSettings, rev})
}

// applicationManifests renders what a deployment of the current revision would
// apply for each long-running service, patches included. Nothing is applied.
func (s *Server) applicationManifests(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	names := spec.Names(a.Spec)
	if service := r.URL.Query().Get("service"); service != "" {
		if _, ok := a.Spec.Services[service]; !ok {
			problem(w, 404, "not_found", "service not found")
			return
		}
		names = []string{service}
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	// Render the accepted, digest-pinned revision when it has been resolved.
	app := a.Spec
	var resolved *spec.Application
	err := s.Store.Pool.QueryRow(r.Context(), "SELECT resolved_spec FROM deployments WHERE application_id=$1 AND revision=$2", a.ID, a.Revision).Scan(&resolved)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		failure(w, err)
		return
	}
	if resolved != nil {
		app = *resolved
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	target := cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: app, Revision: a.Revision}
	out := []byte{}
	for _, name := range names {
		svc := app.Services[name]
		if svc.Job != nil || svc.Session != nil || svc.Actions != nil {
			if len(names) == 1 {
				problem(w, 400, "invalid_request", "manifests are rendered only for long-running services")
				return
			}
			continue
		}
		rendered, err := s.Cluster.RenderManifests(ctx, target, name)
		var refused *cluster.KubernetesPatchError
		if errors.As(err, &refused) {
			problem(w, 400, "kubernetes_patch_refused", err.Error())
			return
		}
		if err != nil {
			problem(w, 503, "unavailable", "the generated manifests are temporarily unavailable; retry with backoff")
			return
		}
		if len(out) > 0 {
			out = append(out, "---\n"...)
		}
		out = append(out, rendered...)
	}
	write(w, 200, map[string]any{"revision": a.Revision, "manifests": string(out)})
}
