package api

import (
	"crypto/subtle"
	"errors"
	"github.com/hakopod/hakopod/internal/store"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type AuthConfig struct {
	PublicURL          string
	SetupSecret        string
	EncryptionKey      string
	GitHubClientID     string
	GitHubClientSecret string
	GoogleClientID     string
	GoogleClientSecret string
	// Provider endpoints are overridden only by trusted in-process integration tests.
	GitHubAuthURL, GitHubTokenURL, GitHubAPIURL       string
	GoogleAuthURL, GoogleTokenURL, GoogleUserInfoURL  string
	SMTPAddress, SMTPUsername, SMTPPassword, SMTPFrom string
	SMTPAllowInsecure                                 bool
	SMTPAllowDelivery                                 bool
}

func (s *Server) authenticateToken(r *http.Request) (string, error) {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if !strings.HasPrefix(auth, "Bearer ") {
			return "", store.ErrUnauthorized
		}
		return strings.TrimPrefix(auth, "Bearer "), nil
	}
	cookie, err := r.Cookie("hakopod_session")
	if err != nil || !strings.HasPrefix(cookie.Value, "hs_") {
		return "", store.ErrUnauthorized
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
		expected := strings.TrimSuffix(s.Auth.PublicURL, "/")
		if expected == "" || r.Header.Get("Origin") != expected {
			return "", store.ErrForbidden
		}
	}
	return cookie.Value, nil
}
func (s *Server) authPublic(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !s.rate("auth:" + ip) {
			w.Header().Set("Retry-After", "5")
			problem(w, 429, "rate_limit", "too many authentication attempts")
			return
		}
		if r.Method == http.MethodPost {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				problem(w, 415, "invalid_request", "Content-Type must be application/json")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" && origin != strings.TrimSuffix(s.Auth.PublicURL, "/") {
				problem(w, 403, "forbidden", "request origin is not allowed")
				return
			}
		}
		next(w, r)
	}
}
func authFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInput):
		problem(w, 400, "invalid_request", err.Error())
	case errors.Is(err, store.ErrBusy):
		w.Header().Set("Retry-After", "3")
		problem(w, 429, "auth_busy", err.Error())
	case errors.Is(err, store.ErrMFARequired):
		problem(w, 401, "mfa_required", err.Error())
	case errors.Is(err, store.ErrPending):
		problem(w, 400, "authorization_pending", "approve this sign-in in your browser")
	case errors.Is(err, store.ErrSlowDown):
		problem(w, 400, "slow_down", "wait at least five seconds before polling again")
	case errors.Is(err, store.ErrDenied):
		problem(w, 403, "access_denied", "the browser user denied this sign-in")
	default:
		failure(w, err)
	}
}
func (s *Server) registerAuthRoutes(public, protected *http.ServeMux) {
	public.HandleFunc("GET /api/v1/auth/status", s.authPublic(s.authStatus))
	public.HandleFunc("POST /api/v1/auth/setup", s.authPublic(s.authSetup))
	public.HandleFunc("POST /api/v1/auth/login", s.authPublic(s.authLogin))
	public.HandleFunc("POST /api/v1/auth/mfa/complete", s.authPublic(s.authMFAComplete))
	public.HandleFunc("POST /api/v1/auth/invites/accept", s.authPublic(s.authAcceptInvite))
	public.HandleFunc("POST /api/v1/auth/device/start", s.authPublic(s.authDeviceStart))
	public.HandleFunc("POST /api/v1/auth/device/token", s.authPublic(s.authDeviceToken))
	public.HandleFunc("GET /api/v1/auth/oauth/{provider}/start", s.authPublic(s.authOAuthStart))
	public.HandleFunc("GET /api/v1/auth/oauth/{provider}/callback", s.authPublic(s.authOAuthCallback))
	public.HandleFunc("POST /api/v1/auth/passkeys/login/start", s.authPublic(s.authPasskeyLoginStart))
	public.HandleFunc("POST /api/v1/auth/passkeys/login/finish", s.authPublic(s.authPasskeyLoginFinish))
	protected.HandleFunc("POST /api/v1/auth/logout", s.authLogout)
	protected.HandleFunc("GET /api/v1/auth/sessions", s.authSessions)
	protected.HandleFunc("DELETE /api/v1/auth/sessions/{id}", s.authRevokeSession)
	protected.HandleFunc("GET /api/v1/auth/security", s.authSecurity)
	protected.HandleFunc("POST /api/v1/auth/mfa/totp/start", s.authTOTPStart)
	protected.HandleFunc("POST /api/v1/auth/mfa/totp/confirm", s.authTOTPConfirm)
	protected.HandleFunc("POST /api/v1/auth/mfa/totp/disable", s.authTOTPDisable)
	protected.HandleFunc("POST /api/v1/auth/passkeys/register/start", s.authPasskeyRegisterStart)
	protected.HandleFunc("POST /api/v1/auth/passkeys/register/finish", s.authPasskeyRegisterFinish)
	protected.HandleFunc("DELETE /api/v1/auth/passkeys/{id}", s.authPasskeyDelete)
	protected.HandleFunc("GET /api/v1/auth/device", s.authDeviceDetails)
	protected.HandleFunc("POST /api/v1/auth/device/approve", s.authDeviceApprove)
	protected.HandleFunc("GET /api/v1/users", s.authUsers)
	protected.HandleFunc("PATCH /api/v1/users/{id}", s.authUpdateUser)
	protected.HandleFunc("GET /api/v1/teams", s.authTeams)
	protected.HandleFunc("POST /api/v1/teams", s.authCreateTeam)
	protected.HandleFunc("GET /api/v1/teams/{id}/members", s.authTeamMembers)
	protected.HandleFunc("PUT /api/v1/teams/{id}/members/{user}", s.authSetTeamMember)
	protected.HandleFunc("POST /api/v1/teams/{id}/invites", s.authCreateInvite)
	protected.HandleFunc("POST /api/v1/projects/{project}/invites", s.authCreateInvite)
	protected.HandleFunc("GET /api/v1/projects/{project}/members", s.authProjectMembers)
	protected.HandleFunc("PUT /api/v1/projects/{project}/members", s.authSetProjectMember)
}
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	needed, err := s.Store.SetupRequired(r.Context())
	if err != nil {
		authFailure(w, err)
		return
	}
	providers := []string{}
	if s.Auth.GitHubClientID != "" && s.Auth.GitHubClientSecret != "" {
		providers = append(providers, "github")
	}
	if s.Auth.GoogleClientID != "" && s.Auth.GoogleClientSecret != "" {
		providers = append(providers, "google")
	}
	_, passkeyErr := s.webAuthn()
	write(w, 200, map[string]any{"setup_required": needed, "password": true, "providers": providers, "passkeys": passkeyErr == nil, "totp": len(s.authEncryptionKey()) == 32, "email_delivery": s.Auth.SMTPAllowDelivery && s.Auth.SMTPAddress != ""})
}
func (s *Server) sessionResponse(w http.ResponseWriter, r *http.Request, session store.Session) {
	http.SetCookie(w, &http.Cookie{Name: "hakopod_session", Value: session.Token, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.Auth.PublicURL, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: int(time.Until(session.ExpiresAt).Seconds()), Expires: session.ExpiresAt})
	write(w, 200, session)
}
func (s *Server) authSetup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string `json:"name"`
		Email      string `json:"email"`
		Password   string `json:"password"`
		SetupToken string `json:"setup_token"`
	}
	if !decode(w, r, &in) {
		return
	}
	allowed := len(s.Auth.SetupSecret) >= 24 && subtle.ConstantTimeCompare([]byte(in.SetupToken), []byte(s.Auth.SetupSecret)) == 1
	existing := ""
	if raw := r.Header.Get("Authorization"); strings.HasPrefix(raw, "Bearer ") {
		p, err := s.Store.Authenticate(r.Context(), strings.TrimPrefix(raw, "Bearer "))
		if err == nil && p.IsAdmin() {
			allowed = true
			existing = p.ID
		}
	}
	if !allowed {
		authFailure(w, store.ErrForbidden)
		return
	}
	p, err := s.Store.SetupOwner(r.Context(), in.Name, in.Email, in.Password, existing)
	if err != nil {
		authFailure(w, err)
		return
	}
	session, err := s.Store.NewSession(r.Context(), p.ID, "browser", "", "", nil)
	if err != nil {
		authFailure(w, err)
		return
	}
	s.sessionResponse(w, r, session)
}
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	p, err := s.Store.CheckPassword(r.Context(), in.Email, in.Password)
	if err != nil {
		authFailure(w, err)
		return
	}
	if len(p.TOTPSecret) > 0 {
		if in.Code == "" {
			authFailure(w, store.ErrMFARequired)
			return
		}
		if err = s.verifyMFA(r.Context(), p.ID, p.TOTPSecret, p.TOTPStep, in.Code); err != nil {
			authFailure(w, err)
			return
		}
	}
	if err = s.Store.SuccessfulLogin(r.Context(), p.ID); err != nil {
		authFailure(w, err)
		return
	}
	session, err := s.Store.NewSession(r.Context(), p.ID, "browser", "", "", nil)
	if err != nil {
		authFailure(w, err)
		return
	}
	s.sessionResponse(w, r, session)
}
func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.RevokeSession(r.Context(), who(r), ""); err != nil {
		authFailure(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "hakopod_session", Value: "", Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.Auth.PublicURL, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: -1})
	write(w, 200, map[string]bool{"logged_out": true})
}
func (s *Server) authSessions(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.Sessions(r.Context(), who(r))
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": v})
}
func (s *Server) authRevokeSession(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.RevokeSession(r.Context(), who(r), r.PathValue("id")); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]bool{"revoked": true})
}
func (s *Server) authUsers(w http.ResponseWriter, r *http.Request) {
	if !human(w, r) || !admin(w, r) {
		return
	}
	v, err := s.Store.Users(r.Context())
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": v})
}
func (s *Server) authUpdateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Disabled bool `json:"disabled"`
		Admin    bool `json:"admin"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Store.UpdateUser(r.Context(), who(r), r.PathValue("id"), in.Disabled, in.Admin); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]bool{"updated": true})
}
func (s *Server) authTeams(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.Teams(r.Context(), who(r))
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": v})
}
func (s *Server) authCreateTeam(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := s.Store.CreateTeam(r.Context(), who(r), in.Name)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 201, v)
}
func (s *Server) authTeamMembers(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.TeamMembers(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": v})
}
func (s *Server) authSetTeamMember(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Role string `json:"role"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Store.SetTeamMember(r.Context(), who(r), r.PathValue("id"), r.PathValue("user"), in.Role); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]bool{"updated": true})
}
func (s *Server) authProjectMembers(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.ProjectMembers(r.Context(), who(r), r.PathValue("project"))
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": v})
}
func (s *Server) authSetProjectMember(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IdentityID string `json:"identity_id"`
		TeamID     string `json:"team_id"`
		Role       string `json:"role"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Store.SetProjectMember(r.Context(), who(r), r.PathValue("project"), in.IdentityID, in.TeamID, in.Role); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]bool{"updated": true})
}
func (s *Server) authCreateInvite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email   string `json:"email"`
		Role    string `json:"role"`
		Project string `json:"project"`
		Deliver bool   `json:"deliver"`
	}
	if !decode(w, r, &in) {
		return
	}
	project := r.PathValue("project")
	if project == "" {
		project = in.Project
	}
	if in.Deliver && (!s.Auth.SMTPAllowDelivery || s.Auth.SMTPAddress == "") {
		problem(w, 409, "email_not_configured", "email delivery is disabled; create a copyable invitation link instead")
		return
	}
	v, token, err := s.Store.CreateInvite(r.Context(), who(r), in.Email, r.PathValue("id"), project, in.Role)
	if err != nil {
		authFailure(w, err)
		return
	}
	inviteURL := strings.TrimSuffix(s.Auth.PublicURL, "/") + "/login/invite?token=" + url.QueryEscape(token)
	if in.Deliver {
		if err = s.sendAuthMail(r.Context(), v.Email, "Your Hakopod invitation", "You were invited to Hakopod. Accept before "+v.ExpiresAt.Format(time.RFC3339)+":\n\n"+inviteURL); err != nil {
			problem(w, 503, "email_failed", "invitation created but delivery failed; use the invitation list to issue a new link")
			return
		}
	}
	write(w, 201, map[string]any{"invite": v, "invite_url": inviteURL, "delivered": in.Deliver})
}
func (s *Server) authAcceptInvite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	var p *store.Principal
	if raw, err := s.authenticateToken(r); err == nil {
		current, err := s.Store.Authenticate(r.Context(), raw)
		if err != nil {
			authFailure(w, err)
			return
		}
		p = &current
	}
	id, err := s.Store.AcceptInvite(r.Context(), in.Token, in.Name, in.Password, p)
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
func (s *Server) authDeviceStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project     string   `json:"project"`
		Environment string   `json:"environment"`
		Permissions []string `json:"permissions"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := s.Store.StartDevice(r.Context(), in.Project, in.Environment, in.Permissions)
	if err != nil {
		authFailure(w, err)
		return
	}
	base := strings.TrimSuffix(s.Auth.PublicURL, "/") + "/login/device"
	write(w, 200, map[string]any{"device_code": v.DeviceCode, "user_code": v.UserCode, "expires_in": v.ExpiresIn, "interval": v.Interval, "verification_uri": base, "verification_uri_complete": base + "?user_code=" + url.QueryEscape(v.UserCode)})
}
func (s *Server) authDeviceToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceCode string `json:"device_code"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := s.Store.PollDevice(r.Context(), in.DeviceCode)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"token": v.Token, "access_token": v.Token, "token_type": "Bearer", "expires_at": v.ExpiresAt, "expires_in": int(time.Until(v.ExpiresAt).Seconds()), "user": v.User})
}
func (s *Server) authDeviceDetails(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.DeviceDetails(r.Context(), who(r), r.URL.Query().Get("user_code"))
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, v)
}
func (s *Server) authDeviceApprove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserCode string `json:"user_code"`
		Approve  bool   `json:"approve"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Store.ApproveDevice(r.Context(), who(r), in.UserCode, in.Approve); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]bool{"approved": in.Approve})
}
