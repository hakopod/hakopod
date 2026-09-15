package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/oauth2"
)

// Source OAuth is separate from account sign-in. The dashboard callback submits
// code/state through its authenticated API proxy; only the same browser session
// that began authorization may complete it. No provider token reaches the browser.
type gitOAuthPending struct {
	ConnectionID string `json:"connection_id"`
	KeyID        string `json:"key_id"`
	Revision     int64  `json:"revision"`
	Verifier     string `json:"verifier"`
	RedirectURL  string `json:"redirect_url"`
}

func (s *Server) gitOAuthOrigin() string {
	if s.gitlabAPIURL != "" {
		return strings.TrimSuffix(s.gitlabAPIURL, "/api/v4")
	}
	return "https://gitlab.com"
}
func (s *Server) gitOAuthClient() *http.Client {
	if s.gitlabHTTP != nil {
		return s.gitlabHTTP
	}
	return &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (s *Server) gitOAuthRedirect(id string) (string, error) {
	u, err := url.Parse(s.Auth.PublicURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("Configure the dashboard public URL before authorizing GitLab")
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", errors.New("GitLab authorization requires HTTPS, except on a loopback development dashboard")
	}
	return strings.TrimSuffix(s.Auth.PublicURL, "/") + "/settings/git/callback", nil
}
func (s *Server) startGitOAuth(w http.ResponseWriter, r *http.Request) {
	if !gitManager(w, r) {
		return
	}
	if !gitInteractive(r) {
		authFailure(w, store.ErrForbidden)
		return
	}
	c, err := s.readGitConnection(r.Context(), r.PathValue("id"))
	if err != nil {
		authFailure(w, err)
		return
	}
	if c.AuthKind != "gitlab_oauth" || !c.Enabled {
		problem(w, 409, "oauth_unavailable", "Select an enabled GitLab OAuth connection")
		return
	}
	v, err := s.decodeGitCredentials(c)
	if err != nil {
		failure(w, err)
		return
	}
	redirect, err := s.gitOAuthRedirect(c.ID)
	if err != nil {
		problem(w, 409, "oauth_callback_unavailable", err.Error())
		return
	}
	verifier := oauth2.GenerateVerifier()
	pending := gitOAuthPending{ConnectionID: c.ID, KeyID: gitSession(r), Revision: c.Revision, Verifier: verifier, RedirectURL: redirect}
	state, err := s.Store.NewChallenge(r.Context(), "git-source-oauth", pending, 5*time.Minute)
	if err != nil {
		authFailure(w, err)
		return
	}
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {v.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "state": {state}, "scope": {v.Scopes}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	write(w, 200, map[string]any{"authorization_url": s.gitOAuthOrigin() + "/oauth/authorize?" + q.Encode(), "callback_url": redirect, "expires_at": time.Now().Add(5 * time.Minute)})
}
func (s *Server) completeGitOAuth(w http.ResponseWriter, r *http.Request) {
	if !gitManager(w, r) {
		return
	}
	if !gitInteractive(r) {
		authFailure(w, store.ErrForbidden)
		return
	}
	var in struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if !decodeGitInput(w, r, &in) {
		return
	}
	if len(in.Code) == 0 || len(in.Code) > 4096 || len(in.State) > 512 {
		problem(w, 400, "oauth_authorization_invalid", "Restart GitLab authorization")
		return
	}
	challenge, err := s.Store.ConsumeChallenge(r.Context(), in.State, "git-source-oauth")
	if err != nil {
		authFailure(w, err)
		return
	}
	var pending gitOAuthPending
	if json.Unmarshal(challenge.Data, &pending) != nil || (r.PathValue("id") != "" && pending.ConnectionID != r.PathValue("id")) || pending.KeyID != gitSession(r) {
		authFailure(w, store.ErrForbidden)
		return
	}
	c, err := s.readGitConnection(r.Context(), pending.ConnectionID)
	if err != nil {
		authFailure(w, err)
		return
	}
	if !c.Enabled || c.AuthKind != "gitlab_oauth" || c.Revision != pending.Revision {
		problem(w, 409, "git_connection_changed", "Git connection changed; restart authorization")
		return
	}
	v, err := s.decodeGitCredentials(c)
	if err != nil {
		failure(w, err)
		return
	}
	next, err := s.exchangeGitOAuth(r.Context(), v, url.Values{"grant_type": {"authorization_code"}, "code": {in.Code}, "code_verifier": {pending.Verifier}, "redirect_uri": {pending.RedirectURL}})
	if err != nil {
		problem(w, 400, "gitlab_authorization_failed", "GitLab authorization failed; verify the app callback and permissions, then reconnect")
		return
	}
	subject, account, err := s.gitOAuthIdentity(r.Context(), next.Token)
	if err != nil {
		problem(w, 400, "gitlab_identity_unavailable", "GitLab account identity could not be verified")
		return
	}
	if c.SubjectID != 0 && c.SubjectID != subject {
		problem(w, 409, "gitlab_account_changed", "Create a new connection to authorize a different GitLab account")
		return
	}
	encrypted, err := s.encodeGitCredentials(c.ID, next)
	if err != nil {
		failure(w, err)
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), "UPDATE git_connections SET credentials=$2,subject_id=$3,account=$4,revision=revision+1,credential_generation=credential_generation+1,refresh_state='ready',updated_at=now() WHERE id=$1 AND revision=$5 AND enabled", c.ID, encrypted, subject, account, pending.Revision)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		problem(w, 409, "gitlab_account_connected", "This GitLab account already has a connection for this OAuth App; use that connection")
		return
	}
	if err == nil && tag.RowsAffected() != 1 {
		err = store.ErrConflict
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'git.oauth.authorize',$3)", who(r).ID, who(r).KeyID, c.ID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		authFailure(w, err)
		return
	}
	c, err = s.readGitConnection(r.Context(), c.ID)
	if err == nil {
		c, err = s.describeGitConnection(r.Context(), c)
	}
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, c)
}
func (s *Server) exchangeGitOAuth(ctx context.Context, old gitConnectionCredentials, values url.Values) (gitConnectionCredentials, error) {
	values.Set("client_id", old.ClientID)
	values.Set("client_secret", old.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, "POST", s.gitOAuthOrigin()+"/oauth/token", strings.NewReader(values.Encode()))
	if err != nil {
		return old, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := s.gitOAuthClient().Do(req)
	if err != nil {
		return old, errors.New("GitLab token exchange did not complete")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return old, fmt.Errorf("GitLab token exchange returned HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return old, errors.New("GitLab token response exceeded supported bounds")
	}
	var token struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Type    string `json:"token_type"`
		Expires int64  `json:"expires_in"`
		Scope   string `json:"scope"`
	}
	if json.Unmarshal(raw, &token) != nil || !strings.EqualFold(token.Type, "bearer") || token.Access == "" || token.Refresh == "" || len(token.Access) > 16384 || len(token.Refresh) > 16384 || token.Expires <= 0 || token.Expires > 366*24*60*60 || strings.ContainsAny(token.Access+token.Refresh, "\r\n\x00") {
		return old, errors.New("GitLab returned an invalid token response")
	}
	if token.Scope != "" && !gitScopeAllows(token.Scope, old.Scopes) {
		return old, errors.New("GitLab did not grant the requested source permissions")
	}
	old.Token = token.Access
	old.RefreshToken = token.Refresh
	old.ExpiresAt = time.Now().Add(time.Duration(token.Expires) * time.Second)
	return old, nil
}
func gitScopeAllows(actual, want string) bool {
	for _, scope := range strings.Fields(actual) {
		if scope == "api" || scope == want {
			return true
		}
	}
	return false
}
func (s *Server) gitOAuthIdentity(ctx context.Context, token string) (int64, string, error) {
	base := "https://gitlab.com/api/v4"
	if s.gitlabAPIURL != "" {
		base = s.gitlabAPIURL
	}
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/user", nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := s.gitOAuthClient().Do(req)
	if err != nil {
		return 0, "", errors.New("GitLab identity request did not complete")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return 0, "", store.ErrUnauthorized
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return 0, "", store.ErrUnauthorized
	}
	var user struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if json.Unmarshal(raw, &user) != nil || user.ID < 1 || len(user.Username) == 0 || len(user.Username) > 255 || strings.ContainsAny(user.Username, "\r\n\x00") {
		return 0, "", store.ErrUnauthorized
	}
	return user.ID, user.Username, nil
}

// One session-level advisory lock serializes token rotation across server
// processes. Persisting 'refreshing' before the remote exchange makes a crash or
// lost rotation response require reauthorization, rather than replaying a token
// that GitLab may already have consumed. No provider call holds a DB transaction.
func (s *Server) gitOAuthToken(ctx context.Context, id string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	conn, err := s.Store.Pool.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock(hashtextextended($1,724891024))", id); err != nil {
		return "", err
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer done()
		if _, err := conn.Exec(cleanup, "SELECT pg_advisory_unlock(hashtextextended($1,724891024))", id); err != nil {
			_ = conn.Conn().Close(cleanup)
		}
	}()
	c, err := scanGitConnection(conn.QueryRow(ctx, "SELECT "+gitConnectionColumns+" FROM git_connections WHERE id=$1", id))
	if err != nil {
		return "", err
	}
	if !c.Enabled || c.AuthKind != "gitlab_oauth" {
		return "", store.ErrForbidden
	}
	v, err := s.decodeGitCredentials(c)
	if err != nil {
		return "", err
	}
	if c.RefreshState != "ready" || v.Token == "" || v.RefreshToken == "" {
		return "", fmt.Errorf("%w: reconnect this GitLab account", store.ErrUnauthorized)
	}
	if v.ExpiresAt.After(time.Now().Add(time.Minute)) {
		return v.Token, nil
	}
	tag, err := conn.Exec(ctx, "UPDATE git_connections SET refresh_state='refreshing' WHERE id=$1 AND revision=$2 AND credential_generation=$3 AND enabled", id, c.Revision, c.CredentialGeneration)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", store.ErrConflict
	}
	next, err := s.exchangeGitOAuth(ctx, v, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {v.RefreshToken}})
	cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer done()
	if err != nil {
		_, _ = conn.Exec(cleanup, "UPDATE git_connections SET refresh_state='reauthorize' WHERE id=$1 AND credential_generation=$2 AND refresh_state='refreshing'", id, c.CredentialGeneration)
		return "", fmt.Errorf("%w: reconnect this GitLab account after token refresh failed", store.ErrUnauthorized)
	}
	encrypted, err := s.encodeGitCredentials(id, next)
	if err != nil {
		return "", err
	}
	tag, err = conn.Exec(cleanup, "UPDATE git_connections SET credentials=$2,credential_generation=credential_generation+1,refresh_state='ready',updated_at=now() WHERE id=$1 AND credential_generation=$3 AND revision=$4 AND refresh_state='refreshing' AND enabled", id, encrypted, c.CredentialGeneration, c.Revision)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", store.ErrConflict
	}
	return next.Token, nil
}
