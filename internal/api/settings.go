package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

type appearance struct {
	AccentColor string `json:"accent_color"`
}

func (s *Server) registerSettingsRoutes(routes *http.ServeMux) {
	routes.HandleFunc("GET /api/v1/settings/appearance", s.getAppearance)
	routes.HandleFunc("PATCH /api/v1/settings/appearance", s.setAppearance)
}

func (s *Server) getAppearance(w http.ResponseWriter, r *http.Request) {
	value := appearance{AccentColor: "#D8FF45"}
	err := s.Store.Pool.QueryRow(r.Context(), "SELECT value FROM installation_settings WHERE name='appearance'").Scan(&value)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		failure(w, err)
		return
	}
	write(w, 200, value)
}

func (s *Server) setAppearance(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var value appearance
	if !decode(w, r, &value) {
		return
	}
	if !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(value.AccentColor) {
		problem(w, 400, "invalid_color", "accent_color must be a six-digit hex color")
		return
	}
	value.AccentColor = strings.ToLower(value.AccentColor)
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), "INSERT INTO installation_settings(name,value) VALUES('appearance',$1) ON CONFLICT(name) DO UPDATE SET value=EXCLUDED.value,revision=installation_settings.revision+1,updated_at=now()", store.JSON(value))
	if err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'settings.appearance','installation')", who(r).ID, who(r).KeyID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, value)
}
