package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/hakopod/hakopod/internal/store"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type passkeyUser struct {
	ID, Email, Name string
	Credentials     []webauthn.Credential
	Original        map[string][]byte
}

func (u *passkeyUser) WebAuthnID() []byte                         { return []byte(u.ID) }
func (u *passkeyUser) WebAuthnName() string                       { return u.Email }
func (u *passkeyUser) WebAuthnDisplayName() string                { return u.Name }
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }
func (s *Server) webAuthn() (*webauthn.WebAuthn, error) {
	origin := strings.TrimSuffix(s.Auth.PublicURL, "/")
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" {
		return nil, store.ErrInput
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()))) {
		return nil, store.ErrInput
	}
	return webauthn.New(&webauthn.Config{RPID: u.Hostname(), RPDisplayName: "Hakopod", RPOrigins: []string{origin}, AuthenticatorSelection: protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired}})
}
func (s *Server) passkeyUser(ctx context.Context, id string) (*passkeyUser, error) {
	u := &passkeyUser{Original: map[string][]byte{}}
	if err := s.Store.Pool.QueryRow(ctx, "SELECT id,email,name FROM identities WHERE id=$1 AND email IS NOT NULL AND NOT disabled", id).Scan(&u.ID, &u.Email, &u.Name); err != nil {
		return nil, store.ErrUnauthorized
	}
	rows, err := s.Store.Pool.Query(ctx, "SELECT id,credential FROM passkeys WHERE identity_id=$1 ORDER BY id LIMIT 20", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var credential webauthn.Credential
		var raw []byte
		var key string
		if err = rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &credential); err != nil {
			return nil, err
		}
		u.Credentials = append(u.Credentials, credential)
		u.Original[key] = raw
	}
	return u, rows.Err()
}

type passkeyInfo struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func (s *Server) passkeyList(ctx context.Context, id string) ([]passkeyInfo, error) {
	rows, err := s.Store.Pool.Query(ctx, "SELECT id,name,created_at,last_used_at FROM passkeys WHERE identity_id=$1 ORDER BY created_at LIMIT 20", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []passkeyInfo{}
	for rows.Next() {
		var v passkeyInfo
		if err = rows.Scan(&v.ID, &v.Name, &v.CreatedAt, &v.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type pendingPasskey struct {
	IdentityID string               `json:"identity_id"`
	Name       string               `json:"name"`
	Session    webauthn.SessionData `json:"session"`
}

func (s *Server) reauthenticateHuman(ctx context.Context, p store.Principal, password, code string) error {
	v, err := s.Store.CheckPassword(ctx, p.Email, password)
	if err != nil {
		return err
	}
	if v.ID != p.ID {
		return store.ErrUnauthorized
	}
	if len(v.TOTPSecret) > 0 {
		if code == "" {
			return store.ErrMFARequired
		}
		return s.verifyMFA(ctx, v.ID, v.TOTPSecret, v.TOTPStep, code)
	}
	return nil
}
func (s *Server) authPasskeyRegisterStart(w http.ResponseWriter, r *http.Request) {
	if !human(w, r) {
		return
	}
	var in struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Name) < 1 || len(in.Name) > 100 {
		authFailure(w, store.ErrInput)
		return
	}
	if err := s.reauthenticateHuman(r.Context(), who(r), in.Password, in.Code); err != nil {
		authFailure(w, err)
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		problem(w, 409, "passkeys_not_configured", "passkeys require a configured HTTPS or local development public URL")
		return
	}
	user, err := s.passkeyUser(r.Context(), who(r).ID)
	if err != nil {
		authFailure(w, err)
		return
	}
	if len(user.Credentials) >= 20 {
		authFailure(w, fmt.Errorf("%w: at most 20 passkeys per account", store.ErrInput))
		return
	}
	options, session, err := wa.BeginRegistration(user)
	if err != nil {
		authFailure(w, store.ErrInput)
		return
	}
	challenge, err := s.Store.NewChallenge(r.Context(), "passkey-register", pendingPasskey{user.ID, in.Name, *session}, 5*time.Minute)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"challenge": challenge, "options": options})
}
func (s *Server) authPasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if !human(w, r) {
		return
	}
	var in struct {
		Challenge  string          `json:"challenge"`
		Credential json.RawMessage `json:"credential"`
	}
	if !decode(w, r, &in) {
		return
	}
	c, err := s.Store.ConsumeChallenge(r.Context(), in.Challenge, "passkey-register")
	if err != nil {
		authFailure(w, err)
		return
	}
	var pending pendingPasskey
	if json.Unmarshal(c.Data, &pending) != nil || pending.IdentityID != who(r).ID {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	user, err := s.passkeyUser(r.Context(), who(r).ID)
	if err != nil {
		authFailure(w, err)
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		authFailure(w, err)
		return
	}
	request := r.Clone(r.Context())
	request.Body = io.NopCloser(bytes.NewReader(in.Credential))
	credential, err := wa.FinishRegistration(user, pending.Session, request)
	if err != nil {
		problem(w, 400, "invalid_passkey", "the authenticator response could not be verified")
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		authFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var id string
	if err = tx.QueryRow(r.Context(), "SELECT id FROM identities WHERE id=$1 AND NOT disabled FOR UPDATE", user.ID).Scan(&id); err != nil {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	var count int
	if err = tx.QueryRow(r.Context(), "SELECT count(*) FROM passkeys WHERE identity_id=$1", user.ID).Scan(&count); err != nil {
		authFailure(w, err)
		return
	}
	if count >= 20 {
		authFailure(w, store.ErrBusy)
		return
	}
	key := base64.RawURLEncoding.EncodeToString(credential.ID)
	if _, err = tx.Exec(r.Context(), "INSERT INTO passkeys(id,identity_id,name,credential) VALUES($1,$2,$3,$4)", key, user.ID, pending.Name, store.JSON(credential)); err != nil {
		authFailure(w, store.ErrConflict)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 201, map[string]string{"id": key, "name": pending.Name})
}
func (s *Server) authPasskeyLoginStart(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		problem(w, 409, "passkeys_not_configured", "passkeys require a configured HTTPS or local development public URL")
		return
	}
	options, session, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		authFailure(w, err)
		return
	}
	challenge, err := s.Store.NewChallenge(r.Context(), "passkey-login", pendingPasskey{Session: *session}, 5*time.Minute)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"challenge": challenge, "options": options})
}
func (s *Server) authPasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Challenge  string          `json:"challenge"`
		Credential json.RawMessage `json:"credential"`
	}
	if !decode(w, r, &in) {
		return
	}
	c, err := s.Store.ConsumeChallenge(r.Context(), in.Challenge, "passkey-login")
	if err != nil {
		authFailure(w, err)
		return
	}
	var pending pendingPasskey
	if json.Unmarshal(c.Data, &pending) != nil {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		authFailure(w, err)
		return
	}
	request := r.Clone(r.Context())
	request.Body = io.NopCloser(bytes.NewReader(in.Credential))
	var loaded *passkeyUser
	_, credential, err := wa.FinishPasskeyLogin(func(rawID, handle []byte) (webauthn.User, error) {
		if len(handle) != 32 {
			return nil, store.ErrUnauthorized
		}
		user, err := s.passkeyUser(r.Context(), string(handle))
		if err != nil {
			return nil, err
		}
		if _, ok := user.Original[base64.RawURLEncoding.EncodeToString(rawID)]; !ok {
			return nil, store.ErrUnauthorized
		}
		loaded = user
		return user, nil
	}, pending.Session, request)
	if err != nil || credential == nil || loaded == nil || credential.Authenticator.CloneWarning {
		problem(w, 401, "invalid_passkey", "the passkey assertion could not be verified")
		return
	}
	key := base64.RawURLEncoding.EncodeToString(credential.ID)
	result, err := s.Store.Pool.Exec(r.Context(), "UPDATE passkeys SET credential=$2,last_used_at=now() WHERE id=$1 AND identity_id=$4 AND credential=$3", key, store.JSON(credential), loaded.Original[key], loaded.ID)
	if err != nil {
		authFailure(w, err)
		return
	}
	if result.RowsAffected() != 1 {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	session, err := s.Store.NewSession(r.Context(), loaded.ID, "browser", "", "", nil)
	if err != nil {
		authFailure(w, err)
		return
	}
	s.sessionResponse(w, r, session)
}
func (s *Server) authPasskeyDelete(w http.ResponseWriter, r *http.Request) {
	if !human(w, r) {
		return
	}
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.reauthenticateHuman(r.Context(), who(r), in.Password, in.Code); err != nil {
		authFailure(w, err)
		return
	}
	result, err := s.Store.Pool.Exec(r.Context(), "DELETE FROM passkeys WHERE id=$1 AND identity_id=$2", r.PathValue("id"), who(r).ID)
	if err != nil {
		authFailure(w, err)
		return
	}
	if result.RowsAffected() != 1 {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
