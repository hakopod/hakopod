// Package auth embeds Hakopod's existing authentication and PostgreSQL store.
// It runs no reconciler and requires no Kubernetes connection.
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Config = api.AuthConfig

var ErrUnauthorized = store.ErrUnauthorized

type User struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Verified bool   `json:"verified"`
	Operator bool   `json:"operator"`
}

type Service struct {
	runtime *cluster.Client
	store   *store.Store
	handler http.Handler
	control http.Handler
	config  Config
}

// Open uses the canonical engine schema in a dedicated control-service database.
// Never point this at a connected customer's installation database.
func Open(ctx context.Context, databaseURL string, config Config) (*Service, error) {
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err = db.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	server := &api.Server{Store: db, Auth: config}
	full := server.Handler()
	service := &Service{store: db, control: full, config: config}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/auth/account" {
			token := ""
			if cookie, err := r.Cookie("hakopod_session"); err == nil {
				token = cookie.Value
			}
			if raw := r.Header.Get("Authorization"); raw != "" {
				token = strings.TrimPrefix(raw, "Bearer ")
			}
			user, err := service.Verify(r.Context(), token)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				w.WriteHeader(401)
				json.NewEncoder(w).Encode(map[string]string{"error": "Sign in to continue"})
				return
			}
			json.NewEncoder(w).Encode(user)
			return
		}
		if !identityRoute(r) {
			http.NotFound(w, r)
			return
		}
		full.ServeHTTP(w, r)
	})
	service.handler = handler
	return service, nil
}

// AccountHandler exposes only canonical account/profile/appearance routes.
func (s *Service) AccountHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if identityRoute(r) || r.URL.Path == "/api/v1/auth/profile" || r.URL.Path == "/api/v1/settings/appearance" || (r.URL.Path == "/api/v1/license" && r.Method == "GET") {
			s.control.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}
func (s *Service) Close()                { s.store.Close() }
func (s *Service) Pool() *pgxpool.Pool   { return s.store.Pool }
func (s *Service) Handler() http.Handler { return s.handler }
func (s *Service) Verify(ctx context.Context, token string) (User, error) {
	p, err := s.store.Authenticate(ctx, token)
	if err != nil || p.CredentialType != "browser" || p.Email == "" {
		return User{}, ErrUnauthorized
	}
	u := User{ID: p.ID, Email: p.Email, Name: p.Name}
	if err = s.store.Pool.QueryRow(ctx, "SELECT email_verified FROM identities WHERE id=$1 AND NOT disabled", p.ID).Scan(&u.Verified); err != nil {
		return User{}, ErrUnauthorized
	}
	u.Operator = u.Verified && p.IsSuperAdmin()
	return u, nil
}

// Only identity routes are exported. Workload, team, license, and installation
// administration remain with their respective product, even for an auth owner.
func identityRoute(r *http.Request) bool {
	path := r.URL.Path
	switch r.Method + " " + path {
	case "GET /api/v1/auth/status", "POST /api/v1/auth/setup", "POST /api/v1/auth/login",
		"POST /api/v1/auth/register", "POST /api/v1/auth/register/verify",
		"POST /api/v1/auth/password/forgot", "POST /api/v1/auth/password/reset",
		"POST /api/v1/auth/mfa/complete", "POST /api/v1/auth/logout",
		"GET /api/v1/auth/security", "GET /api/v1/auth/sessions",
		"POST /api/v1/auth/mfa/totp/start", "POST /api/v1/auth/mfa/totp/confirm", "POST /api/v1/auth/mfa/totp/disable",
		"POST /api/v1/auth/passkeys/login/start", "POST /api/v1/auth/passkeys/login/finish",
		"POST /api/v1/auth/passkeys/register/start", "POST /api/v1/auth/passkeys/register/finish":
		return true
	}
	if r.Method == "GET" {
		for _, p := range []string{"github", "gitlab", "google", "oidc"} {
			if path == "/api/v1/auth/oauth/"+p+"/start" || path == "/api/v1/auth/oauth/"+p+"/callback" {
				return true
			}
		}
	}
	if r.Method == "DELETE" {
		for _, prefix := range []string{"/api/v1/auth/sessions/", "/api/v1/auth/passkeys/"} {
			id := strings.TrimPrefix(path, prefix)
			if id != path && id != "" && !strings.Contains(id, "/") {
				return true
			}
		}
	}
	return false
}
