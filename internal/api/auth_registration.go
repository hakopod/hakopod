package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

func authMailResponseFloor(start time.Time) {
	if wait := 150*time.Millisecond - time.Since(start); wait > 0 {
		time.Sleep(wait)
	}
}

func (s *Server) signupOpen(w http.ResponseWriter, r *http.Request) bool {
	needed, err := s.Store.SetupRequired(r.Context())
	if err != nil {
		authFailure(w, err)
		return false
	}
	if !s.Auth.PublicSignupEnabled() || needed {
		problem(w, 403, "signup_disabled", "account registration is disabled; use an invitation from your administrator")
		return false
	}
	return true
}
func (s *Server) authRegister(w http.ResponseWriter, r *http.Request) {
	if !s.signupOpen(w, r) {
		return
	}
	if !s.Auth.SMTPAllowDelivery || s.Auth.SMTPAddress == "" {
		problem(w, 409, "email_not_configured", "email registration is unavailable until the operator configures email delivery")
		return
	}
	var in struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	started := time.Now()
	token, err := s.Store.BeginRegistration(r.Context(), in.Name, in.Email, in.Password)
	if errors.Is(err, store.ErrInput) {
		authFailure(w, err)
		return
	}
	if token != "" {
		address := strings.TrimSuffix(s.Auth.PublicURL, "/") + "/login/verify#token=" + url.QueryEscape(token)
		s.startAuthMail(r.Context(), in.Email, "Verify your Hakopod email", "Finish creating your account within 15 minutes:\n\n"+address+"\n\nIf you did not request this, ignore this message.", token, "registration")
	}
	authMailResponseFloor(started)
	write(w, 202, map[string]bool{"accepted": true})
}
func (s *Server) authVerifyRegistration(w http.ResponseWriter, r *http.Request) {
	if !s.signupOpen(w, r) {
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &in) {
		return
	}
	id, err := s.Store.CompleteRegistration(r.Context(), in.Token)
	if err != nil {
		authFailure(w, err)
		return
	}
	session, err := s.Store.NewSession(r.Context(), id, "browser", "", "", nil)
	if err != nil {
		authFailure(w, err)
		return
	}
	s.sessionResponse(w, r, session)
}
func (s *Server) authForgotPassword(w http.ResponseWriter, r *http.Request) {
	if !s.Auth.SMTPAllowDelivery || s.Auth.SMTPAddress == "" {
		problem(w, 409, "email_not_configured", "password recovery is unavailable until the operator configures email delivery")
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &in) {
		return
	}
	started := time.Now()
	token, err := s.Store.BeginPasswordReset(r.Context(), in.Email)
	if errors.Is(err, store.ErrInput) {
		authFailure(w, err)
		return
	}
	if token != "" {
		address := strings.TrimSuffix(s.Auth.PublicURL, "/") + "/login/reset#token=" + url.QueryEscape(token)
		s.startAuthMail(r.Context(), in.Email, "Reset your Hakopod password", "Choose a new password within 15 minutes:\n\n"+address+"\n\nYour two-factor authentication stays enabled. If you did not request this, ignore this message.", token, "password-reset")
	}
	authMailResponseFloor(started)
	write(w, 202, map[string]bool{"accepted": true})
}
func (s *Server) authResetPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Store.ResetPassword(r.Context(), in.Token, in.Password); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]bool{"reset": true})
}
func (s *Server) authInspectInvite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := s.Store.InviteDetails(r.Context(), in.Token)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, v)
}
func (s *Server) authOnboarding(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.Onboarding(r.Context(), who(r))
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, v)
}
func (s *Server) authCompleteOnboarding(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Choice   string `json:"choice"`
		InviteID string `json:"invite_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Store.CompleteOnboarding(r.Context(), who(r), in.Choice, in.InviteID); err != nil {
		authFailure(w, err)
		return
	}
	session, err := s.Store.NewSession(r.Context(), who(r).ID, "browser", "", "", nil)
	if err != nil {
		authFailure(w, err)
		return
	}
	s.sessionResponse(w, r, session)
}
