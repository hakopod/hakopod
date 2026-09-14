package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Named connections are installation resources. Saving a source/build binding
// grants access to that exact connection and repository, never to a provider's
// first available credential. Legacy IDs always refer to their original Secret.
type gitConnection struct {
	ManagedApp           bool            `json:"managed_app,omitempty"`
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	Provider             string          `json:"provider"`
	AuthKind             string          `json:"auth_kind"`
	Revision             int64           `json:"revision"`
	Enabled              bool            `json:"enabled"`
	Account              string          `json:"account"`
	SubjectID            int64           `json:"subject_id"`
	OAuthScopes          string          `json:"oauth_scopes,omitempty"`
	OAuthClientID        string          `json:"oauth_client_id,omitempty"`
	CredentialGeneration int64           `json:"-"`
	RefreshState         string          `json:"-"`
	AppID                int64           `json:"github_app_id"`
	InstallationID       int64           `json:"installation_id"`
	Legacy               bool            `json:"legacy"`
	Configured           bool            `json:"configured"`
	TokenConfigured      bool            `json:"token_configured"`
	PrivateRepositories  bool            `json:"private_repositories"`
	WebhookPath          string          `json:"webhook_path"`
	Status               string          `json:"status"`
	Capabilities         map[string]bool `json:"capabilities"`
	UpdatedAt            time.Time       `json:"updated_at"`
	encrypted            []byte
	legacyRef            string
}

type gitConnectionCredentials struct {
	Manifest      bool      `json:"manifest,omitempty"`
	AppSlug       string    `json:"app_slug,omitempty"`
	SetupOrigin   string    `json:"setup_origin,omitempty"`
	OwnerLogin    string    `json:"owner_login,omitempty"`
	Builds        bool      `json:"builds,omitempty"`
	Token         string    `json:"token,omitempty"`
	WebhookSecret string    `json:"webhook_secret,omitempty"`
	AppID         string    `json:"github_app_id,omitempty"`
	PrivateKey    string    `json:"github_private_key,omitempty"`
	ClientID      string    `json:"oauth_client_id,omitempty"`
	ClientSecret  string    `json:"oauth_client_secret,omitempty"`
	RefreshToken  string    `json:"refresh_token,omitempty"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	Scopes        string    `json:"scopes,omitempty"`
}
type gitConnectionInput struct {
	Name             string `json:"name"`
	Provider         string `json:"provider"`
	AuthKind         string `json:"auth_kind"`
	ExpectedRevision *int64 `json:"expected_revision"`
	Enabled          *bool  `json:"enabled"`
	Token            string `json:"token"`
	WebhookSecret    string `json:"webhook_secret"`
	AppID            string `json:"github_app_id"`
	PrivateKey       string `json:"github_private_key"`
	InstallationID   int64  `json:"github_installation_id"`
	ClientID         string `json:"oauth_client_id"`
	ClientSecret     string `json:"oauth_client_secret"`
	Scopes           string `json:"oauth_scopes"`
}

const gitConnectionColumns = "id,name,provider,auth_kind,revision,enabled,account,subject_id,github_app_id,installation_id,credentials,legacy_secret_ref,updated_at,oauth_client_id,credential_generation,refresh_state"

func defaultGitConnection(provider string) string {
	if provider == "gitlab" {
		return "gitlab-default"
	}
	return "github-default"
}
func selectedGitConnection(provider string, ids ...string) string {
	if len(ids) > 0 && ids[0] != "" {
		return ids[0]
	}
	return defaultGitConnection(provider)
}
func scanGitConnection(row pgx.Row) (gitConnection, error) {
	var c gitConnection
	err := row.Scan(&c.ID, &c.Name, &c.Provider, &c.AuthKind, &c.Revision, &c.Enabled, &c.Account, &c.SubjectID, &c.AppID, &c.InstallationID, &c.encrypted, &c.legacyRef, &c.UpdatedAt, &c.OAuthClientID, &c.CredentialGeneration, &c.RefreshState)
	c.Legacy = c.legacyRef != ""
	c.WebhookPath = "/api/v1/webhooks/git/" + c.ID
	if c.AuthKind == "github_app" {
		c.WebhookPath = "/api/v1/webhooks/github-app/" + strconv.FormatInt(c.AppID, 10)
	}
	if c.Legacy {
		c.WebhookPath = "/api/v1/webhooks/" + c.Provider
	}
	return c, err
}
func (s *Server) readGitConnection(ctx context.Context, id string) (gitConnection, error) {
	return scanGitConnection(s.Store.Pool.QueryRow(ctx, "SELECT "+gitConnectionColumns+" FROM git_connections WHERE id=$1", id))
}
func (s *Server) decodeGitCredentials(c gitConnection) (gitConnectionCredentials, error) {
	var v gitConnectionCredentials
	raw, err := s.decryptAuth(c.encrypted)
	if err != nil {
		return v, err
	}
	// Include the immutable record ID in the sealed plaintext so ciphertext
	// cannot be moved between connection records.
	var sealed struct {
		ID          string                   `json:"id"`
		Credentials gitConnectionCredentials `json:"credentials"`
	}
	if json.Unmarshal(raw, &sealed) != nil || sealed.ID != c.ID {
		return v, store.ErrUnauthorized
	}
	return sealed.Credentials, nil
}
func (s *Server) encodeGitCredentials(id string, v gitConnectionCredentials) ([]byte, error) {
	return s.encryptAuth(store.JSON(map[string]any{"id": id, "credentials": v}))
}
func (s *Server) describeGitConnection(ctx context.Context, c gitConnection) (gitConnection, error) {
	var v gitConnectionCredentials
	if c.Legacy {
		d, e := s.sourceCredentials(ctx, c.Provider)
		if e != nil {
			return c, e
		}
		v.Token = string(d["token"])
		v.WebhookSecret = string(d["webhook-secret"])
	} else {
		var e error
		v, e = s.decodeGitCredentials(c)
		if e != nil {
			return c, e
		}
	}
	c.ManagedApp = v.Manifest
	if v.Manifest {
		c.WebhookPath = "/api/v1/webhooks/git/" + c.ID
	}
	c.OAuthScopes = v.Scopes
	c.TokenConfigured = v.Token != "" || (c.AuthKind == "github_app" && v.PrivateKey != "" && c.InstallationID > 0)
	c.PrivateRepositories = c.TokenConfigured
	c.Configured = c.Enabled && c.TokenConfigured
	if c.AuthKind == "gitlab_oauth" && (c.RefreshState != "ready" || v.RefreshToken == "" || c.SubjectID == 0) {
		c.Configured = false
	}
	c.Status = "ready"
	if !c.Enabled {
		c.Status = "disabled"
	} else if !c.TokenConfigured {
		c.Status = "not_configured"
	}
	if c.AuthKind == "gitlab_oauth" && !c.Configured && c.Enabled {
		c.Status = "reauthorize"
	}
	if v.Manifest && c.InstallationID == 0 {
		c.Status = "setup_required"
	}
	c.Capabilities = map[string]bool{"read_source": c.Enabled, "builds": c.Configured}
	if c.AuthKind == "gitlab_oauth" {
		c.Capabilities["read_source"] = c.Configured
		c.Capabilities["builds"] = c.Configured && v.Scopes == "api"
	}
	if v.Manifest {
		c.Capabilities["builds"] = c.Configured && v.Builds
		c.Capabilities["read_source"] = c.Configured
	}
	return c, nil
}
func (s *Server) registerGitConnectionRoutes(public, protected *http.ServeMux) {
	protected.HandleFunc("GET /api/v1/git/connections", s.gitConnectionHandler(s.listGitConnections))
	protected.HandleFunc("GET /api/v1/git/connections/{id}", s.gitConnectionHandler(s.getGitConnection))
	protected.HandleFunc("POST /api/v1/git/connections", s.gitConnectionHandler(s.saveGitConnection))
	protected.HandleFunc("PUT /api/v1/git/connections/{id}", s.gitConnectionHandler(s.saveGitConnection))
	protected.HandleFunc("DELETE /api/v1/git/connections/{id}", s.gitConnectionHandler(s.deleteGitConnection))
	protected.HandleFunc("POST /api/v1/git/connections/{id}/authorize", s.gitConnectionHandler(s.startGitOAuth))
	protected.HandleFunc("POST /api/v1/git/connections/{id}/oauth/complete", s.gitConnectionHandler(s.completeGitOAuth))
	protected.HandleFunc("POST /api/v1/git/oauth/complete", s.gitConnectionHandler(s.completeGitOAuth))
	protected.HandleFunc("GET /api/v1/git/setup", s.gitConnectionHandler(s.gitProviderSetup))
	protected.HandleFunc("POST /api/v1/git/github/start", s.gitConnectionHandler(s.startGitHubManifest))
	protected.HandleFunc("POST /api/v1/git/github/complete", s.gitConnectionHandler(s.completeGitHubManifest))
	protected.HandleFunc("POST /api/v1/git/github/install/complete", s.gitConnectionHandler(s.completeGitHubInstall))
	protected.HandleFunc("POST /api/v1/git/connections/{id}/github/setup", s.gitConnectionHandler(s.resumeGitHubSetup))
	public.HandleFunc("POST /api/v1/webhooks/git/{connection}", s.namedGitWebhook)
	public.HandleFunc("POST /api/v1/webhooks/github-app/{app}", s.gitHubAppWebhook)
}
func (s *Server) listGitConnections(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	rows, err := s.Store.Pool.Query(r.Context(), "SELECT "+gitConnectionColumns+" FROM git_connections ORDER BY lower(name),id LIMIT 100")
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	out := []gitConnection{}
	for rows.Next() {
		c, e := scanGitConnection(rows)
		if e != nil {
			failure(w, e)
			return
		}
		out = append(out, c)
	}
	if err = rows.Err(); err != nil {
		failure(w, err)
		return
	}
	rows.Close()
	for i, c := range out {
		out[i], err = s.describeGitConnection(r.Context(), c)
		if err != nil {
			problem(w, 503, "git_credentials_unavailable", "Git connection credential storage is unavailable")
			return
		}
	}
	write(w, 200, map[string]any{"items": out})
}
func (s *Server) getGitConnection(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	c, e := s.readGitConnection(r.Context(), r.PathValue("id"))
	if e == nil {
		c, e = s.describeGitConnection(r.Context(), c)
	}
	if e != nil {
		authFailure(w, e)
		return
	}
	write(w, 200, c)
}
func (s *Server) saveGitConnection(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var in gitConnectionInput
	if !decodeGitInput(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 || strings.ContainsAny(in.Name, "\r\n\x00") || (in.Provider != "github" && in.Provider != "gitlab") || (in.AuthKind != "token" && in.AuthKind != "github_app" && in.AuthKind != "gitlab_oauth") || (in.AuthKind == "github_app" && in.Provider != "github") || (in.AuthKind == "gitlab_oauth" && in.Provider != "gitlab") || len(in.ClientID) > 1024 || len(in.ClientSecret) > 4096 || strings.ContainsAny(in.ClientID+in.ClientSecret, "\r\n\x00") || (in.Scopes != "" && in.Scopes != "api" && in.Scopes != "read_api") || len(in.Token) > 16384 || len(in.WebhookSecret) > 256 || len(in.PrivateKey) > 16384 || len(in.AppID) > 100 || strings.ContainsAny(in.Token+in.WebhookSecret+in.AppID, "\r\n\x00") {
		problem(w, 400, "invalid_git_connection", "Choose a name, provider and supported authentication method with bounded credentials")
		return
	}
	if in.AuthKind != "token" && in.Token != "" {
		problem(w, 400, "unexpected_git_token", "Use the selected App authentication flow instead of supplying a token")
		return
	}
	c := gitConnection{ID: store.NewID(), Name: in.Name, Provider: in.Provider, AuthKind: in.AuthKind, Revision: 1, Enabled: true}
	var v gitConnectionCredentials
	updating := r.PathValue("id") != ""
	if updating {
		var err error
		c, err = s.readGitConnection(r.Context(), r.PathValue("id"))
		if err != nil {
			authFailure(w, err)
			return
		}
		if c.Legacy {
			problem(w, 409, "legacy_connection", "Update legacy credentials through the original provider settings or create a named connection")
			return
		}
		if in.ExpectedRevision == nil || *in.ExpectedRevision != c.Revision {
			problem(w, 409, "git_connection_conflict", "Git connection changed; refresh before saving")
			return
		}
		if c.Provider != in.Provider || c.AuthKind != in.AuthKind {
			problem(w, 400, "immutable_git_connection", "Provider and authentication method cannot change; create another connection")
			return
		}
		v, err = s.decodeGitCredentials(c)
		if err != nil {
			failure(w, err)
			return
		}
		if v.Manifest && (c.InstallationID == 0 || in.PrivateKey != "" || in.WebhookSecret != "" || (in.AppID != "" && in.AppID != v.AppID)) {
			problem(w, 409, "managed_app_setup", "Complete App setup; manage this App's credentials through GitHub")
			return
		}
		c.Name = in.Name
		c.Revision++
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	if in.Token != "" {
		v.Token = in.Token
	}
	if in.WebhookSecret != "" {
		v.WebhookSecret = in.WebhookSecret
	}
	if in.AppID != "" {
		v.AppID = in.AppID
	}
	if in.PrivateKey != "" {
		v.PrivateKey = in.PrivateKey
	}
	if in.InstallationID > 0 {
		if updating && c.InstallationID != in.InstallationID {
			problem(w, 400, "immutable_installation", "Create another connection for a different GitHub installation")
			return
		}
		c.InstallationID = in.InstallationID
	}
	if c.AuthKind == "gitlab_oauth" {
		if updating && in.ClientID != "" && in.ClientID != v.ClientID {
			problem(w, 400, "immutable_oauth_app", "Create another connection for a different OAuth App")
			return
		}
		if in.ClientID != "" {
			v.ClientID = in.ClientID
		}
		if in.ClientSecret != "" {
			v.ClientSecret = in.ClientSecret
		}
		if v.Scopes == "" {
			v.Scopes = "api"
		}
		if in.Scopes != "" && in.Scopes != v.Scopes {
			if updating && v.Token != "" {
				problem(w, 409, "oauth_scope_changed", "Create another connection to change authorized OAuth scopes")
				return
			}
			v.Scopes = in.Scopes
		}
		if v.ClientID == "" || v.ClientSecret == "" {
			problem(w, 400, "oauth_app_required", "Provide the GitLab source OAuth App ID and client secret")
			return
		}
		c.OAuthClientID = v.ClientID
	}
	once := ""
	if v.WebhookSecret == "" {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			failure(w, err)
			return
		}
		once = hex.EncodeToString(raw)
		v.WebhookSecret = once
	}
	if len(v.WebhookSecret) < 32 || (c.AuthKind == "token" && v.Token == "") {
		problem(w, 400, "git_credentials_required", "Provide a provider token and a webhook secret of at least 32 characters, or configure a GitHub App")
		return
	}
	if c.AuthKind == "github_app" {
		if err := s.verifyGitHubInstallation(r.Context(), &c, v); err != nil {
			problem(w, 400, "github_installation_unavailable", err.Error())
			return
		}
	}
	sealed, err := s.encodeGitCredentials(c.ID, v)
	if err != nil {
		problem(w, 503, "credential_key_unavailable", "Configure the installation authentication encryption key before saving Git credentials")
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(724891023)"); err != nil {
		failure(w, err)
		return
	}
	if c.AuthKind == "github_app" {
		rows, e := tx.Query(r.Context(), "SELECT "+gitConnectionColumns+" FROM git_connections WHERE github_app_id=$1 AND id<>$2 ORDER BY id LIMIT 1", c.AppID, c.ID)
		if e != nil {
			failure(w, e)
			return
		}
		var existing *gitConnection
		if rows.Next() {
			other, e := scanGitConnection(rows)
			if e != nil {
				rows.Close()
				failure(w, e)
				return
			}
			existing = &other
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			failure(w, e)
			return
		}
		if existing != nil {
			other, e := s.decodeGitCredentials(*existing)
			if e != nil {
				failure(w, e)
				return
			}
			if in.WebhookSecret != "" && in.WebhookSecret != other.WebhookSecret {
				problem(w, 409, "app_webhook_secret", "Installations of the same GitHub App must use the same webhook secret")
				return
			}
			v.WebhookSecret = other.WebhookSecret
			once = ""
			sealed, e = s.encodeGitCredentials(c.ID, v)
			if e != nil {
				failure(w, e)
				return
			}
		}
	}
	if updating {
		var tag pgconn.CommandTag
		tag, err = tx.Exec(r.Context(), "UPDATE git_connections SET name=$2,revision=$3,enabled=$4,account=$5,subject_id=$6,installation_id=$7,credentials=$8,github_app_id=$10,oauth_client_id=$11,credential_generation=credential_generation+1,updated_at=now() WHERE id=$1 AND revision=$9 AND credential_generation=$12", c.ID, c.Name, c.Revision, c.Enabled, c.Account, c.SubjectID, c.InstallationID, sealed, c.Revision-1, c.AppID, c.OAuthClientID, c.CredentialGeneration)
		if err == nil && tag.RowsAffected() != 1 {
			err = store.ErrConflict
		}
	} else {
		var n int
		err = tx.QueryRow(r.Context(), "SELECT count(*) FROM git_connections").Scan(&n)
		if err == nil && n >= 100 {
			err = store.ErrBusy
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), "INSERT INTO git_connections(id,name,provider,auth_kind,enabled,account,subject_id,installation_id,credentials,github_app_id,oauth_client_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)", c.ID, c.Name, c.Provider, c.AuthKind, c.Enabled, c.Account, c.SubjectID, c.InstallationID, sealed, c.AppID, c.OAuthClientID)
		}
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'git.connection.configure',$3)", who(r).ID, who(r).KeyID, c.ID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			problem(w, 409, "git_connection_name", "A Git connection already uses this name")
		} else {
			authFailure(w, err)
		}
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
	status := 201
	if updating {
		status = 200
	}
	write(w, status, struct {
		gitConnection
		WebhookSecret string `json:"webhook_secret,omitempty"`
	}{c, once})
}
func (s *Server) deleteGitConnection(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	revision, err := strconv.ParseInt(r.URL.Query().Get("expected_revision"), 10, 64)
	if err != nil || revision < 1 {
		problem(w, 400, "revision_required", "Provide expected_revision before removing a connection")
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	c, err := scanGitConnection(tx.QueryRow(r.Context(), "SELECT "+gitConnectionColumns+" FROM git_connections WHERE id=$1 FOR UPDATE", r.PathValue("id")))
	if err != nil {
		authFailure(w, err)
		return
	}
	if c.Legacy {
		problem(w, 409, "legacy_connection", "Default connections are retained for compatibility")
		return
	}
	if c.Revision != revision {
		authFailure(w, store.ErrConflict)
		return
	}
	_, err = tx.Exec(r.Context(), "DELETE FROM git_connections WHERE id=$1", c.ID)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" {
		problem(w, 409, "git_connection_in_use", "This connection is referenced by a source, build or retained job; remove its bindings before deleting it")
		return
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'git.connection.delete',$3)", who(r).ID, who(r).KeyID, c.ID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]string{"status": "deleted"})
}

func (s *Server) connectionCredentials(ctx context.Context, provider, id, repository string, permissions map[string]string) (map[string][]byte, error) {
	id = selectedGitConnection(provider, id)
	if id == defaultGitConnection(provider) {
		return s.sourceCredentials(ctx, provider)
	}
	c, err := s.readGitConnection(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Provider != provider || !c.Enabled {
		return nil, fmt.Errorf("%w: Git connection is disabled or belongs to another provider", store.ErrForbidden)
	}
	v, err := s.decodeGitCredentials(c)
	if err != nil {
		return nil, err
	}
	token := v.Token
	if c.AuthKind == "gitlab_oauth" {
		token, err = s.gitOAuthToken(ctx, c.ID)
		if err != nil {
			return nil, err
		}
	}
	if c.AuthKind == "github_app" && repository != "" {
		token, err = s.githubInstallationToken(ctx, c, v, repository, permissions)
		if err != nil {
			return nil, err
		}
	}
	return map[string][]byte{"token": []byte(token), "webhook-secret": []byte(v.WebhookSecret), "auth-kind": []byte(c.AuthKind), "scopes": []byte(v.Scopes)}, nil
}
func (s *Server) validateGitConnection(ctx context.Context, provider, id string) error {
	c, e := s.readGitConnection(ctx, selectedGitConnection(provider, id))
	if e != nil {
		return e
	}
	if c.Provider != provider || !c.Enabled {
		return fmt.Errorf("%w: select an enabled connection for this provider", store.ErrForbidden)
	}
	return nil
}
func (s *Server) namedGitWebhook(w http.ResponseWriter, r *http.Request) {
	c, e := s.readGitConnection(r.Context(), r.PathValue("connection"))
	if e != nil || !c.Enabled {
		problem(w, 404, "git_connection_unavailable", "Git connection is unavailable")
		return
	}
	if c.Provider == "gitlab" {
		s.gitlabWebhook(w, r)
	} else {
		s.githubWebhook(w, r)
	}
}

func gitHubAppJWT(v gitConnectionCredentials) (string, error) {
	block, _ := pem.Decode([]byte(v.PrivateKey))
	if block == nil {
		return "", errors.New("Provide a PEM RSA private key for the GitHub App")
	}
	var key *rsa.PrivateKey
	if parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = parsed
	} else if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		key, _ = parsed.(*rsa.PrivateKey)
	}
	if key == nil || key.N.BitLen() < 2048 || v.AppID == "" {
		return "", errors.New("Provide the GitHub App ID and an RSA private key of at least 2048 bits")
	}
	now := time.Now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := base64.RawURLEncoding.EncodeToString(store.JSON(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": v.AppID}))
	input := header + "." + claims
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
func (s *Server) githubAppAPI(ctx context.Context, v gitConnectionCredentials, method, endpoint string, body any, out any) error {
	jwt, err := gitHubAppJWT(v)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(store.JSON(body)))
	}
	base := "https://api.github.com"
	if s.githubAPIURL != "" {
		base = s.githubAPIURL
	}
	req, err := http.NewRequestWithContext(ctx, method, base+endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := s.githubHTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("GitHub App verification is unavailable; check outbound HTTPS")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 && res.StatusCode != 201 {
		return fmt.Errorf("GitHub App returned HTTP %d; verify the app key, installation and granted permissions", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (512<<10)+1))
	if err != nil || len(raw) > 512<<10 {
		return errors.New("GitHub App response exceeds supported bounds")
	}
	if json.Unmarshal(raw, out) != nil {
		return errors.New("GitHub App returned an invalid response")
	}
	return nil
}
func (s *Server) verifyGitHubInstallation(ctx context.Context, c *gitConnection, v gitConnectionCredentials) error {
	if c.InstallationID <= 0 {
		return errors.New("Provide the installation ID from your GitHub App installation")
	}
	var app struct {
		ID int64 `json:"id"`
	}
	if err := s.githubAppAPI(ctx, v, "GET", "/app", nil, &app); err != nil {
		return err
	}
	if app.ID <= 0 {
		return errors.New("GitHub returned an invalid App identity")
	}
	var installation struct {
		ID          int64      `json:"id"`
		AppID       int64      `json:"app_id"`
		SuspendedAt *time.Time `json:"suspended_at"`
		Account     struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := s.githubAppAPI(ctx, v, "GET", "/app/installations/"+strconv.FormatInt(c.InstallationID, 10), nil, &installation); err != nil {
		return err
	}
	if installation.ID != c.InstallationID || installation.AppID != app.ID || installation.Account.ID <= 0 || installation.Account.Login == "" || len(installation.Account.Login) > 100 || strings.ContainsAny(installation.Account.Login, "/\\\r\n\x00") || installation.SuspendedAt != nil {
		return errors.New("The installation must be active and belong to the configured GitHub App")
	}
	if c.AppID != 0 && c.AppID != app.ID {
		return errors.New("The GitHub App identity cannot change; create a new connection")
	}
	c.AppID = app.ID
	if c.SubjectID != 0 && c.SubjectID != installation.Account.ID {
		return errors.New("The installation account changed; create a new connection")
	}
	c.SubjectID = installation.Account.ID
	c.Account = installation.Account.Login
	return nil
}
func (s *Server) githubInstallationToken(ctx context.Context, c gitConnection, v gitConnectionCredentials, repository string, permissions map[string]string) (string, error) {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || !strings.EqualFold(parts[0], c.Account) {
		return "", fmt.Errorf("%w: repository does not belong to this GitHub installation", store.ErrForbidden)
	}
	var token struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	err := s.githubAppAPI(ctx, v, "POST", "/app/installations/"+strconv.FormatInt(c.InstallationID, 10)+"/access_tokens", map[string]any{"repositories": []string{parts[1]}, "permissions": permissions}, &token)
	if err != nil {
		return "", err
	}
	if token.Token == "" || len(token.Token) > 16384 || !token.ExpiresAt.After(time.Now().Add(10*time.Second)) {
		return "", errors.New("GitHub returned an invalid or expired installation token")
	}
	return token.Token, nil
}

func githubEndpointRepository(endpoint string) string {
	parts := strings.Split(strings.TrimPrefix(endpoint, "/"), "/")
	if len(parts) >= 3 && parts[0] == "repos" {
		return parts[1] + "/" + parts[2]
	}
	return ""
}

// A valid signature is only authority for the installation selected by this
// endpoint. It cannot submit work for another installation of the same App.
func (s *Server) verifyWebhookConnection(w http.ResponseWriter, r *http.Request, id, provider string, body []byte) bool {
	if id == defaultGitConnection(provider) {
		return true
	}
	c, err := s.readGitConnection(r.Context(), id)
	if err != nil || !c.Enabled || c.Provider != provider {
		problem(w, 403, "git_connection_unavailable", "Git connection is unavailable")
		return false
	}
	if c.AuthKind == "github_app" {
		var p struct {
			Installation struct {
				ID int64 `json:"id"`
			} `json:"installation"`
		}
		if json.Unmarshal(body, &p) != nil || p.Installation.ID != c.InstallationID {
			problem(w, 403, "installation_mismatch", "Webhook installation does not match this connection")
			return false
		}
	}
	return true
}

// The App has one webhook URL shared by all its installations. The untrusted
// installation ID selects a candidate key; githubWebhook still verifies HMAC
// and the exact installation before it can enqueue any work.
func (s *Server) gitHubAppWebhook(w http.ResponseWriter, r *http.Request) {
	app, err := strconv.ParseInt(r.PathValue("app"), 10, 64)
	if err != nil || app < 1 {
		problem(w, 404, "app_unavailable", "GitHub App is unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		problem(w, 413, "body_limit", "Webhook body exceeds 512 KiB")
		return
	}
	var payload struct {
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Installation.ID < 1 {
		problem(w, 400, "installation_required", "GitHub installation is required")
		return
	}
	var id string
	err = s.Store.Pool.QueryRow(r.Context(), "SELECT id FROM git_connections WHERE github_app_id=$1 AND installation_id=$2 AND enabled", app, payload.Installation.ID).Scan(&id)
	if err != nil {
		problem(w, 404, "installation_unavailable", "GitHub installation is unavailable")
		return
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	r.SetPathValue("connection", id)
	s.githubWebhook(w, r)
}

func (s *Server) gitConnectionHandler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		next(w, r.WithContext(ctx))
	}
}

// Do not expose decoder diagnostics: malformed JSON may contain credential
// fragments in an unexpected field name or token value.
func decodeGitInput(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		problem(w, 400, "invalid_git_input", "Provide a valid Git connection request within the supported size limit")
		return false
	}
	return true
}

func (s *Server) gitWebhookCredentials(ctx context.Context, provider, id string) (map[string][]byte, error) {
	id = selectedGitConnection(provider, id)
	if id == defaultGitConnection(provider) {
		return s.sourceCredentials(ctx, provider)
	}
	c, err := s.readGitConnection(ctx, id)
	if err != nil {
		return nil, err
	}
	if !c.Enabled || c.Provider != provider {
		return nil, store.ErrForbidden
	}
	v, err := s.decodeGitCredentials(c)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{"webhook-secret": []byte(v.WebhookSecret)}, nil
}
