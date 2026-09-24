package api

import (
	"github.com/hakopod/hakopod/internal/store"
	"net/http"
)

func (s *Server) customRoles(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		roles, err := s.Store.CustomRoles(r.Context(), who(r))
		if err != nil {
			authFailure(w, err)
			return
		}
		write(w, 200, map[string]any{"items": roles})
		return
	}
	var in struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
		Revision    int64    `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if r.Method == "DELETE" {
		if err := s.Store.DeleteCustomRole(r.Context(), who(r), r.PathValue("role"), in.Revision); err != nil {
			authFailure(w, err)
			return
		}
		write(w, 200, map[string]bool{"deleted": true})
		return
	}
	role, err := s.Store.SaveCustomRole(r.Context(), who(r), r.PathValue("role"), in.Name, in.Permissions, in.Revision)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, role)
}
func (s *Server) organizationSecurity(w http.ResponseWriter, r *http.Request) {
	var result store.OrganizationSecurity
	var err error
	if r.Method == "GET" {
		result, err = s.Store.OrganizationSecurity(r.Context(), who(r))
	} else {
		var in struct {
			Required bool  `json:"require_mfa"`
			Revision int64 `json:"expected_revision"`
		}
		if !decode(w, r, &in) {
			return
		}
		result, err = s.Store.SetOrganizationSecurity(r.Context(), who(r), in.Required, in.Revision)
	}
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, result)
}

// Restricted sessions retain only the routes needed to enroll a factor, verify
// it, or sign out. Workload, team and installation mutations remain unavailable.
func mfaRecoveryRoute(method, path string) bool {
	switch method + " " + path {
	case "GET /api/v1/me", "GET /api/v1/auth/security", "GET /api/v1/auth/profile", "GET /api/v1/auth/sessions", "GET /api/v1/settings/appearance", "GET /api/v1/license",
		"POST /api/v1/auth/logout", "POST /api/v1/auth/mfa/totp/start", "POST /api/v1/auth/mfa/totp/confirm", "POST /api/v1/auth/mfa/verify",
		"POST /api/v1/auth/passkeys/register/start", "POST /api/v1/auth/passkeys/register/finish":
		return true
	}
	return false
}

func (s *Server) authMFAVerify(w http.ResponseWriter, r *http.Request) {
	if !human(w, r) {
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	var secret []byte
	var step int64
	if err := s.Store.Pool.QueryRow(r.Context(), "SELECT totp_secret,totp_step FROM identities WHERE id=$1 AND NOT disabled AND (locked_until IS NULL OR locked_until<now())", who(r).ID).Scan(&secret, &step); err != nil {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	if err := s.verifyMFA(r.Context(), who(r).ID, secret, step, in.Code); err != nil {
		authFailure(w, err)
		return
	}
	if _, err := s.Store.Pool.Exec(r.Context(), "UPDATE api_keys SET mfa_verified=true WHERE id=$1 AND revoked_at IS NULL", who(r).KeyID); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]bool{"verified": true})
}
