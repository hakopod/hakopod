package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
)

var gitHubAccountPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
var gitHubSlugPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,99}$`)
var gitHubCodePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,256}$`)

type gitAppPending struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Origin   string `json:"origin"`
}

func gitBrowserAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !gitManager(w, r) {
		return false
	}
	if !gitInteractive(r) {
		authFailure(w, store.ErrForbidden)
		return false
	}
	return true
}
func (s *Server) gitAppOrigin() (string, error) {
	u, err := url.Parse(s.Auth.PublicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("Configure a public HTTPS dashboard URL in Infrastructure > Setup before installing a GitHub App")
	}
	host := strings.ToLower(u.Hostname())
	ip := net.ParseIP(host)
	if host == "localhost" || !strings.Contains(host, ".") || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || (ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast())) {
		return "", errors.New("GitHub needs a publicly reachable HTTPS dashboard and webhook URL; localhost and private addresses cannot receive GitHub webhooks")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}
func gitAppProblem(w http.ResponseWriter, err error) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		problem(w, 409, "git_connection_conflict", "A connection with this name or GitHub App already exists")
		return
	}
	authFailure(w, err)
}
func (s *Server) gitProviderSetup(w http.ResponseWriter, r *http.Request) {
	if !gitManager(w, r) {
		return
	}
	origin, githubErr := s.gitAppOrigin()
	callback, gitlabErr := s.gitOAuthRedirect("")
	out := map[string]any{"github_available": githubErr == nil, "public_url": origin, "gitlab_callback_url": callback}
	if githubErr != nil {
		out["github_notice"] = githubErr.Error()
	}
	if gitlabErr != nil {
		out["gitlab_notice"] = gitlabErr.Error()
	}
	write(w, 200, out)
}
func (s *Server) startGitHubManifest(w http.ResponseWriter, r *http.Request) {
	if !gitBrowserAdmin(w, r) {
		return
	}
	var in struct {
		Name         string `json:"name"`
		Organization string `json:"organization"`
		Builds       bool   `json:"builds"`
	}
	if !decodeGitInput(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Organization = strings.TrimSpace(in.Organization)
	if in.Name == "" || len(in.Name) > 80 || strings.ContainsAny(in.Name, "\r\n\x00") || (in.Organization != "" && !gitHubAccountPattern.MatchString(in.Organization)) {
		problem(w, 400, "invalid_git_connection", "Enter a connection name and a valid GitHub organization login, or leave organization empty for your personal account")
		return
	}
	origin, err := s.gitAppOrigin()
	if err != nil {
		problem(w, 409, "github_public_url_required", err.Error())
		return
	}
	id := store.NewID()
	v := gitConnectionCredentials{Manifest: true, SetupOrigin: origin, OwnerLogin: in.Organization, Builds: in.Builds}
	sealed, err := s.encodeGitCredentials(id, v)
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
	_, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(724891023)")
	var count int
	if err == nil {
		err = tx.QueryRow(r.Context(), "SELECT count(*) FROM git_connections WHERE project=$1 AND environment=$2", who(r).Project, who(r).Environment).Scan(&count)
	}
	if err == nil && count >= 100 {
		err = store.ErrBusy
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO git_connections(id,name,provider,auth_kind,enabled,credentials,project,environment) VALUES($1,$2,'github','github_app',false,$3,$4,$5)", id, in.Name, sealed, who(r).Project, who(r).Environment)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'git.app.start',$3)", who(r).ID, who(r).KeyID, id)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		gitAppProblem(w, err)
		return
	}
	c, err := s.readGitConnection(r.Context(), id)
	if err != nil {
		failure(w, err)
		return
	}
	s.writeGitAppSetup(w, r, c, v)
}
func (s *Server) resumeGitHubSetup(w http.ResponseWriter, r *http.Request) {
	if !gitBrowserAdmin(w, r) {
		return
	}
	c, err := s.readGitConnection(r.Context(), r.PathValue("id"))
	if err != nil {
		authFailure(w, err)
		return
	}
	v, err := s.decodeGitCredentials(c)
	if err != nil {
		failure(w, err)
		return
	}
	if !v.Manifest || c.InstallationID != 0 {
		problem(w, 409, "github_setup_complete", "This connection is not awaiting GitHub App setup")
		return
	}
	s.writeGitAppSetup(w, r, c, v)
}
func (s *Server) writeGitAppSetup(w http.ResponseWriter, r *http.Request, c gitConnection, v gitConnectionCredentials) {
	origin, err := s.gitAppOrigin()
	if err != nil || origin != v.SetupOrigin {
		problem(w, 409, "github_public_url_changed", "Restore the HTTPS dashboard URL used to start this connection, or delete the unfinished connection and start again")
		return
	}
	phase := "manifest"
	if c.AppID > 0 {
		phase = "install"
	}
	state, err := s.Store.NewChallenge(r.Context(), "git-app-"+phase+":"+gitSession(r), gitAppPending{c.ID, c.Revision, origin}, 15*time.Minute)
	if err != nil {
		authFailure(w, err)
		return
	}
	result := map[string]any{"connection_id": c.ID, "phase": phase, "expires_at": time.Now().Add(15 * time.Minute)}
	if phase == "manifest" {
		path := "/settings/apps/new"
		if v.OwnerLogin != "" {
			path = "/organizations/" + v.OwnerLogin + "/settings/apps/new"
		}
		permissions := map[string]string{"contents": "read", "metadata": "read"}
		events := []string{"push"}
		if v.Builds {
			permissions["contents"] = "write"
			permissions["actions"] = "write"
			permissions["workflows"] = "write"
			events = append(events, "workflow_run")
		}
		manifest := map[string]any{"name": "Hakopod " + c.ID[:8], "url": origin, "hook_attributes": map[string]any{"url": s.gitWebhookURL(origin, c.ID), "active": true}, "redirect_url": origin + "/settings/git/github/callback", "setup_url": origin + "/settings/git/github/installed", "public": false, "request_oauth_on_install": false, "default_permissions": permissions, "default_events": events}
		result["action_url"] = "https://github.com" + path + "?state=" + url.QueryEscape(state)
		result["manifest"] = string(store.JSON(manifest))
	} else {
		if !gitHubSlugPattern.MatchString(v.AppSlug) {
			failure(w, errors.New("stored GitHub App slug is invalid"))
			return
		}
		result["action_url"] = "https://github.com/apps/" + v.AppSlug + "/installations/new?state=" + url.QueryEscape(state)
	}
	write(w, 200, result)
}

// Serialize each connection's one-time provider exchange without holding a DB
// transaction during network I/O. Competing callbacks fail instead of racing.
func (s *Server) lockGitApp(ctx context.Context, id string) (func(), error) {
	conn, err := s.Store.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var locked bool
	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,724891027))", id).Scan(&locked)
	if err != nil || !locked {
		conn.Release()
		if err == nil {
			err = store.ErrBusy
		}
		return nil, err
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, "SELECT pg_advisory_unlock(hashtextextended($1,724891027))", id); err != nil {
			_ = conn.Conn().Close(cleanup)
		}
		conn.Release()
	}, nil
}
func (s *Server) readGitAppPending(w http.ResponseWriter, r *http.Request, state, phase string) (gitConnection, gitConnectionCredentials, func(), bool) {
	empty := func() {}
	challenge, err := s.Store.ConsumeChallenge(r.Context(), state, "git-app-"+phase+":"+gitSession(r))
	if err != nil {
		authFailure(w, err)
		return gitConnection{}, gitConnectionCredentials{}, empty, false
	}
	var pending gitAppPending
	if json.Unmarshal(challenge.Data, &pending) != nil {
		authFailure(w, store.ErrUnauthorized)
		return gitConnection{}, gitConnectionCredentials{}, empty, false
	}
	unlock, err := s.lockGitApp(r.Context(), pending.ID)
	if err != nil {
		authFailure(w, err)
		return gitConnection{}, gitConnectionCredentials{}, empty, false
	}
	c, err := s.readGitConnection(r.Context(), pending.ID)
	var v gitConnectionCredentials
	if err == nil {
		v, err = s.decodeGitCredentials(c)
	}
	origin, originErr := s.gitAppOrigin()
	if err == nil && (originErr != nil || pending.Origin != origin || v.SetupOrigin != origin || !v.Manifest || c.Revision != pending.Revision || c.InstallationID != 0 || (phase == "manifest" && c.AppID != 0) || (phase == "install" && c.AppID <= 0)) {
		err = store.ErrConflict
	}
	if err != nil {
		unlock()
		authFailure(w, err)
		return c, v, empty, false
	}
	return c, v, unlock, true
}
func (s *Server) completeGitHubManifest(w http.ResponseWriter, r *http.Request) {
	if !gitBrowserAdmin(w, r) {
		return
	}
	var in struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if !decodeGitInput(w, r, &in) {
		return
	}
	if !gitHubCodePattern.MatchString(in.Code) || len(in.State) > 512 {
		problem(w, 400, "github_callback_invalid", "Restart GitHub App setup from the saved connection")
		return
	}
	c, v, unlock, ok := s.readGitAppPending(w, r, in.State, "manifest")
	if !ok {
		return
	}
	defer unlock()
	base := "https://api.github.com"
	if s.githubAPIURL != "" {
		base = s.githubAPIURL
	}
	req, err := http.NewRequestWithContext(r.Context(), "POST", base+"/app-manifests/"+in.Code+"/conversions", nil)
	if err != nil {
		failure(w, err)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := s.githubHTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	res, err := client.Do(req)
	if err != nil {
		problem(w, 502, "github_manifest_failed", "GitHub App registration could not be exchanged. Reopen the saved connection to restart setup")
		return
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (128<<10)+1))
	var app struct {
		ID            int64  `json:"id"`
		Slug          string `json:"slug"`
		PEM           string `json:"pem"`
		WebhookSecret string `json:"webhook_secret"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err != nil || len(raw) > 128<<10 || res.StatusCode != 201 || json.Unmarshal(raw, &app) != nil || app.ID <= 0 || !gitHubSlugPattern.MatchString(app.Slug) || len(app.PEM) > 16384 || len(app.WebhookSecret) < 32 || len(app.WebhookSecret) > 256 || !gitHubAccountPattern.MatchString(app.Owner.Login) || (v.OwnerLogin != "" && !strings.EqualFold(v.OwnerLogin, app.Owner.Login)) {
		problem(w, 502, "github_manifest_failed", "GitHub returned an invalid App registration. Reopen the saved connection to restart setup")
		return
	}
	v.AppID = strconv.FormatInt(app.ID, 10)
	v.AppSlug = app.Slug
	v.PrivateKey = app.PEM
	v.WebhookSecret = app.WebhookSecret
	v.OwnerLogin = app.Owner.Login
	if _, err = gitHubAppJWT(v); err != nil {
		problem(w, 502, "github_manifest_failed", "GitHub returned an invalid App key")
		return
	}
	// Persist the generated secret before asking the user to install; cancelling
	// or losing the next browser redirect never discards a successfully saved key.
	sealed, err := s.encodeGitCredentials(c.ID, v)
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
	tag, err := tx.Exec(r.Context(), "UPDATE git_connections SET github_app_id=$2,credentials=$3,revision=revision+1,updated_at=now() WHERE id=$1 AND revision=$4 AND NOT enabled", c.ID, app.ID, sealed, c.Revision)
	if err == nil && tag.RowsAffected() != 1 {
		err = store.ErrConflict
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'git.app.register',$3)", who(r).ID, who(r).KeyID, c.ID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		gitAppProblem(w, err)
		return
	}
	c.AppID = app.ID
	c.Revision++
	s.writeGitAppSetup(w, r, c, v)
}
func (s *Server) completeGitHubInstall(w http.ResponseWriter, r *http.Request) {
	if !gitBrowserAdmin(w, r) {
		return
	}
	var in struct {
		State          string `json:"state"`
		InstallationID int64  `json:"installation_id"`
	}
	if !decodeGitInput(w, r, &in) {
		return
	}
	if in.InstallationID < 1 || len(in.State) > 512 {
		problem(w, 400, "github_installation_invalid", "Complete installation from the saved GitHub connection")
		return
	}
	c, v, unlock, ok := s.readGitAppPending(w, r, in.State, "install")
	if !ok {
		return
	}
	defer unlock()
	c.InstallationID = in.InstallationID
	if err := s.verifyGitHubInstallation(r.Context(), &c, v); err != nil {
		problem(w, 400, "github_installation_invalid", err.Error())
		return
	}
	var installation struct {
		Permissions map[string]string `json:"permissions"`
	}
	if err := s.githubAppAPI(r.Context(), v, "GET", "/app/installations/"+strconv.FormatInt(in.InstallationID, 10), nil, &installation); err != nil {
		problem(w, 502, "github_installation_invalid", err.Error())
		return
	}
	var hook struct {
		URL         string `json:"url"`
		InsecureSSL string `json:"insecure_ssl"`
		ContentType string `json:"content_type"`
	}
	if err := s.githubAppAPI(r.Context(), v, "GET", "/app/hook/config", nil, &hook); err != nil {
		problem(w, 502, "github_webhook_unavailable", err.Error())
		return
	}
	if hook.URL != s.gitWebhookURL(v.SetupOrigin, c.ID) || hook.InsecureSSL != "0" || hook.ContentType != "json" {
		problem(w, 400, "github_webhook_invalid", "Restore the App webhook URL and secure JSON delivery configured by Hakopod before completing installation")
		return
	}
	p := installation.Permissions
	read := func(value string) bool { return value == "read" || value == "write" }
	if !strings.EqualFold(c.Account, v.OwnerLogin) || !read(p["contents"]) || !read(p["metadata"]) || (v.Builds && (p["contents"] != "write" || p["actions"] != "write" || p["workflows"] != "write")) {
		problem(w, 400, "github_permissions_required", "Install the App on its owning account and approve the repository permissions requested during setup")
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), "UPDATE git_connections SET installation_id=$2,account=$3,subject_id=$4,enabled=true,revision=revision+1,updated_at=now() WHERE id=$1 AND revision=$5 AND NOT enabled", c.ID, c.InstallationID, c.Account, c.SubjectID, c.Revision)
	if err == nil && tag.RowsAffected() != 1 {
		err = store.ErrConflict
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'git.app.install',$3)", who(r).ID, who(r).KeyID, c.ID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		gitAppProblem(w, err)
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
