package api

import (
	"net/http"

	"github.com/hakopod/hakopod/internal/store"
)

type appLogSettingsView struct {
	store.AppLogSettings
	Revision int64 `json:"revision"`
}

type appLogSettingsInput struct {
	store.AppLogSettings
	ExpectedRevision int64 `json:"expected_revision"`
}

func (s *Server) getAppLogSettings(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	v, rev, err := s.Store.AppLogSettings(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, appLogSettingsView{v, rev})
}

func (s *Server) putAppLogSettings(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var in appLogSettingsInput
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision < 0 {
		problem(w, 400, "invalid_request", "expected_revision must be zero for first-time settings or the revision you loaded")
		return
	}
	rev, err := s.Store.PutAppLogSettings(r.Context(), in.AppLogSettings, in.ExpectedRevision)
	if err != nil {
		failure(w, err)
		return
	}
	// The settings are saved; an audit failure must not report them as lost.
	_, _ = s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'settings.app_logs','installation')", who(r).ID, who(r).KeyID)
	write(w, 200, appLogSettingsView{in.AppLogSettings, rev})
}
