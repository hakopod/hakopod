package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

type loginProvider struct {
	Provider         string `json:"provider"`
	Enabled          bool   `json:"enabled"`
	ClientID         string `json:"client_id"`
	ClientSecret     string `json:"client_secret,omitempty"`
	SecretConfigured bool   `json:"secret_configured"`
	IssuerURL        string `json:"issuer_url"`
	Revision         int64  `json:"revision"`
	CallbackURL      string `json:"callback_url"`
	Source           string `json:"source"`
	EncryptionReady  bool   `json:"encryption_ready"`
}

func loginFeature(provider string) string {
	if provider == "oidc" {
		return "enterprise_sso"
	}
	return "oauth_login"
}
func validLoginProvider(provider string) bool {
	return provider == "github" || provider == "google" || provider == "gitlab" || provider == "oidc"
}
func (s *Server) registerLoginProviderRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/installation/login-providers/{provider}", s.getLoginProvider)
	m.HandleFunc("PUT /api/v1/installation/login-providers/{provider}", s.putLoginProvider)
}
func (s *Server) loginSettingsAllowed(w http.ResponseWriter, r *http.Request) bool {
	if who(r).CredentialType != "browser" || !who(r).IsAdmin() || s.Auth.DeploymentMode == cluster.DeploymentManagedCloud {
		authFailure(w, store.ErrForbidden)
		return false
	}
	if !validLoginProvider(r.PathValue("provider")) {
		authFailure(w, store.ErrInput)
		return false
	}
	return true
}

type loginProviderQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Server) readLoginProvider(ctx context.Context, provider string) (loginProvider, error) {
	return s.readLoginProviderFrom(ctx, provider, s.Store.Pool)
}
func (s *Server) readLoginProviderFrom(ctx context.Context, provider string, q loginProviderQuerier) (loginProvider, error) {
	p := loginProvider{Provider: provider, Source: "operator", EncryptionReady: len(s.authEncryptionKey()) == 32, CallbackURL: strings.TrimSuffix(s.Auth.PublicURL, "/") + "/api/v1/auth/oauth/" + provider + "/callback"}
	switch provider {
	case "github":
		p.ClientID, p.ClientSecret = s.Auth.GitHubClientID, s.Auth.GitHubClientSecret
	case "google":
		p.ClientID, p.ClientSecret = s.Auth.GoogleClientID, s.Auth.GoogleClientSecret
	case "gitlab":
		p.ClientID, p.ClientSecret = s.Auth.GitLabClientID, s.Auth.GitLabClientSecret
	case "oidc":
		p.ClientID, p.ClientSecret, p.IssuerURL = s.Auth.OIDCClientID, s.Auth.OIDCClientSecret, s.Auth.OIDCIssuerURL
	}
	p.Enabled = p.ClientID != "" && p.ClientSecret != ""
	if s.Auth.DeploymentMode != cluster.DeploymentManagedCloud {
		var encrypted []byte
		err := q.QueryRow(ctx, "SELECT revision,configuration FROM installation_login_providers WHERE provider=$1", provider).Scan(&p.Revision, &encrypted)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return p, err
		}
		if err == nil {
			plain, e := s.decryptAuth(encrypted)
			if e != nil {
				return p, e
			}
			var saved loginProvider
			if e = json.Unmarshal(plain, &saved); e != nil {
				return p, e
			}
			if saved.Provider != provider {
				return p, store.ErrUnauthorized
			}
			p.Enabled, p.ClientID, p.ClientSecret, p.IssuerURL = saved.Enabled, saved.ClientID, saved.ClientSecret, saved.IssuerURL
			p.Source = "settings"
		}
	}
	p.SecretConfigured = p.ClientSecret != ""
	return p, nil
}
func (s *Server) getLoginProvider(w http.ResponseWriter, r *http.Request) {
	if !s.loginSettingsAllowed(w, r) {
		return
	}
	p, err := s.readLoginProvider(r.Context(), r.PathValue("provider"))
	if err != nil {
		authFailure(w, err)
		return
	}
	p.ClientSecret = ""
	write(w, 200, p)
}
func validateLoginProvider(p loginProvider) error {
	if len(p.ClientID) > 512 || len(p.ClientSecret) > 8192 || strings.ContainsAny(p.ClientID+p.ClientSecret, "\r\n") {
		return store.ErrInput
	}
	if p.Enabled && (p.ClientID == "" || p.ClientSecret == "") {
		return fmt.Errorf("%w: client ID and secret are required", store.ErrInput)
	}
	if p.Provider == "oidc" && (p.Enabled || p.IssuerURL != "") {
		u, e := url.Parse(p.IssuerURL)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(p.IssuerURL) > 2048 {
			return fmt.Errorf("%w: issuer must be an HTTPS URL without credentials, query or fragment", store.ErrInput)
		}
	} else if p.IssuerURL != "" {
		return fmt.Errorf("%w: issuer URL only applies to OpenID Connect", store.ErrInput)
	}
	return nil
}
func (s *Server) putLoginProvider(w http.ResponseWriter, r *http.Request) {
	if !s.loginSettingsAllowed(w, r) {
		return
	}
	var in struct {
		Enabled          bool   `json:"enabled"`
		ClientID         string `json:"client_id"`
		ClientSecret     string `json:"client_secret"`
		ClearSecret      bool   `json:"clear_secret"`
		IssuerURL        string `json:"issuer_url"`
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil || (in.ClearSecret && in.ClientSecret != "") {
		authFailure(w, store.ErrInput)
		return
	}
	provider := r.PathValue("provider")
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		authFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	// Serialize first insert and updates with license changes checked under a share lock.
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(793044232)"); err != nil {
		authFailure(w, err)
		return
	}
	if in.Enabled {
		if _, err = tx.Exec(r.Context(), "SELECT singleton FROM installation_license WHERE singleton FOR SHARE"); err != nil {
			authFailure(w, err)
			return
		}
		if err = s.Store.RequireFeaturesTx(r.Context(), tx, loginFeature(provider)); err != nil {
			authFailure(w, err)
			return
		}
	}
	p, err := s.readLoginProviderFrom(r.Context(), provider, tx)
	if err != nil {
		authFailure(w, err)
		return
	}
	if p.Revision != *in.ExpectedRevision {
		authFailure(w, store.ErrConflict)
		return
	}
	p.Enabled, p.ClientID, p.IssuerURL = in.Enabled, strings.TrimSpace(in.ClientID), strings.TrimSpace(in.IssuerURL)
	if in.ClearSecret {
		p.ClientSecret = ""
	} else if in.ClientSecret != "" {
		p.ClientSecret = in.ClientSecret
	}
	if err = validateLoginProvider(p); err != nil {
		authFailure(w, err)
		return
	}
	encrypted, err := s.encryptAuth(store.JSON(p))
	if err != nil {
		authFailure(w, err)
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO installation_login_providers(provider,revision,configuration) VALUES($1,1,$2) ON CONFLICT(provider) DO UPDATE SET revision=installation_login_providers.revision+1,configuration=EXCLUDED.configuration,updated_at=now()`, provider, encrypted)
	if err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'login-provider.configure',$3)", who(r).ID, who(r).KeyID, provider)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		authFailure(w, err)
		return
	}
	s.getLoginProvider(w, r)
}
func (s *Server) loginAllowed(ctx context.Context, provider string) error {
	if !validLoginProvider(provider) {
		return store.ErrInput
	}
	if s.Auth.DeploymentMode == cluster.DeploymentManagedCloud {
		return nil
	}
	return s.Store.RequireFeatures(ctx, loginFeature(provider))
}
func providerFingerprint(p loginProvider) string {
	sum := sha256.Sum256([]byte(p.Provider + "\x00" + p.ClientID + "\x00" + p.ClientSecret + "\x00" + p.IssuerURL))
	return hex.EncodeToString(sum[:])
}
func (s *Server) configuredOAuth(ctx context.Context, provider string) (*oauth2.Config, *oidc.Provider, loginProvider, error) {
	var p loginProvider
	if err := s.loginAllowed(ctx, provider); err != nil {
		return nil, nil, p, err
	}
	p, err := s.readLoginProvider(ctx, provider)
	if err != nil {
		return nil, nil, p, err
	}
	if !p.Enabled || p.ClientID == "" || p.ClientSecret == "" {
		return nil, nil, p, fmt.Errorf("%w: sign-in provider is disabled", store.ErrInput)
	}
	if provider == "oidc" {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		client := &http.Client{Transport: boundedOIDCTransport{}, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		discovery, err := oidc.NewProvider(oidc.ClientContext(ctx, client), p.IssuerURL)
		if err != nil {
			return nil, nil, p, fmt.Errorf("%w: could not discover OpenID Connect provider", store.ErrInput)
		}
		endpoint := discovery.Endpoint()
		var metadata struct {
			JWKS string `json:"jwks_uri"`
		}
		if discovery.Claims(&metadata) != nil {
			return nil, nil, p, store.ErrInput
		}
		for _, raw := range []string{endpoint.AuthURL, endpoint.TokenURL, metadata.JWKS} {
			u, e := url.Parse(raw)
			if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
				return nil, nil, p, store.ErrInput
			}
		}
		return &oauth2.Config{ClientID: p.ClientID, ClientSecret: p.ClientSecret, RedirectURL: p.CallbackURL, Endpoint: endpoint, Scopes: []string{oidc.ScopeOpenID, "email", "profile"}}, discovery, p, nil
	}
	config, err := s.oauthConfig(provider)
	// oauthConfig supplies fixed endpoints; settings may be the sole source of credentials.
	if config == nil {
		return nil, nil, p, err
	}
	config.ClientID, config.ClientSecret = p.ClientID, p.ClientSecret
	return config, nil, p, nil
}

func verifyOIDC(ctx context.Context, discovery *oidc.Provider, p loginProvider, token *oauth2.Token, nonce string) (oauthIdentity, error) {
	raw, ok := token.Extra("id_token").(string)
	if !ok || len(raw) > 64<<10 {
		return oauthIdentity{}, store.ErrUnauthorized
	}
	id, err := discovery.Verifier(&oidc.Config{ClientID: p.ClientID}).Verify(ctx, raw)
	if err != nil || nonce == "" || id.Nonce != nonce || id.Subject == "" || len(id.Subject) > 512 {
		return oauthIdentity{}, store.ErrUnauthorized
	}
	var claims struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
		Name     string `json:"name"`
	}
	if err = id.Claims(&claims); err != nil || !claims.Verified {
		return oauthIdentity{}, store.ErrUnauthorized
	}
	email, err := store.NormalizeEmail(claims.Email)
	if err != nil {
		return oauthIdentity{}, store.ErrUnauthorized
	}
	return oauthIdentity{id.Subject, email, claims.Name}, nil
}

// Bound discovery and JWKS responses even when an issuer returns an oversized body.
type boundedOIDCTransport struct{}
type oidcResponseBody struct {
	io.Reader
	io.Closer
}

func (boundedOIDCTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := http.DefaultTransport.RoundTrip(r)
	if err == nil {
		response.Body = oidcResponseBody{io.LimitReader(response.Body, 1<<20), response.Body}
	}
	return response, err
}
