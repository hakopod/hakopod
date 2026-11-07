package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

// MCP clients sign in with OAuth 2.1 (authorization code + PKCE) on top of the
// device consent: authorization opens a device code, the browser user approves
// it, and the token endpoint redeems that code once with the PKCE verifier.
// Only project MCP with read, log and optional deploy access is offered; other
// connections keep using API keys.
func (s *Server) registerOAuthRoutes(public, protected *http.ServeMux) {
	if s.CloudControlPlane {
		return
	}
	public.HandleFunc("GET /.well-known/oauth-protected-resource", s.oauthResourceMetadata)
	public.HandleFunc("GET /.well-known/oauth-protected-resource/api/v1/mcp", s.oauthResourceMetadata)
	public.HandleFunc("GET /.well-known/oauth-authorization-server", s.oauthServerMetadata)
	public.HandleFunc("POST /api/v1/oauth/register", s.oauthPublic(s.oauthRegister))
	public.HandleFunc("POST /api/v1/oauth/token", s.oauthPublic(s.oauthToken))
	protected.HandleFunc("POST /api/v1/oauth/authorize", s.oauthAuthorize)
}

func (s *Server) oauthIssuer() string { return strings.TrimSuffix(s.Auth.PublicURL, "/") }

// The query names the project connection, so it is carried into the metadata.
func (s *Server) mcpChallenge(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v1/mcp" || s.Auth.PublicURL == "" || s.CloudControlPlane {
		return
	}
	location := s.oauthIssuer() + "/.well-known/oauth-protected-resource/api/v1/mcp"
	if query := r.URL.Query().Encode(); query != "" {
		location += "?" + query
	}
	w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+location+`"`)
}

func (s *Server) oauthResourceMetadata(w http.ResponseWriter, r *http.Request) {
	if s.Auth.PublicURL == "" {
		http.NotFound(w, r)
		return
	}
	resource := s.oauthIssuer() + "/api/v1/mcp"
	if query := r.URL.Query().Encode(); query != "" {
		resource += "?" + query
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	write(w, 200, map[string]any{"resource": resource, "authorization_servers": []string{s.oauthIssuer()}, "bearer_methods_supported": []string{"header"}})
}

func (s *Server) oauthServerMetadata(w http.ResponseWriter, r *http.Request) {
	if s.Auth.PublicURL == "" {
		http.NotFound(w, r)
		return
	}
	issuer := s.oauthIssuer()
	w.Header().Set("Cache-Control", "public, max-age=300")
	write(w, 200, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/login/authorize",
		"token_endpoint":                        issuer + "/api/v1/oauth/token",
		"registration_endpoint":                 issuer + "/api/v1/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	})
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	write(w, status, map[string]string{"error": code, "error_description": description})
}

// Registration and token requests carry no cookie authority, so no Origin check.
func (s *Server) oauthPublic(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Auth.PublicURL == "" {
			http.NotFound(w, r)
			return
		}
		if !s.rate("auth:" + requestIP(r)) {
			w.Header().Set("Retry-After", "5")
			oauthError(w, 429, "slow_down", "too many authentication attempts")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		next(w, r)
	}
}

func (s *Server) oauthRegister(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
		GrantTypes   []string `json:"grant_types"`
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || json.NewDecoder(r.Body).Decode(&in) != nil {
		oauthError(w, 400, "invalid_client_metadata", "send the client metadata as a JSON object")
		return
	}
	if len(in.GrantTypes) > 0 && !slices.Contains(in.GrantTypes, "authorization_code") {
		oauthError(w, 400, "invalid_client_metadata", "only the authorization_code grant is supported")
		return
	}
	// Every client is public: a requested secret-based method is replaced by
	// "none" in the response, as RFC 7591 allows.
	client, err := s.Store.RegisterOAuthClient(r.Context(), in.ClientName, in.RedirectURIs)
	switch {
	case errors.Is(err, store.ErrInput):
		oauthError(w, 400, "invalid_redirect_uri", "redirect_uris must be 1-10 https URLs, or http URLs on a loopback host, without fragments; client_name is at most 100 characters")
	case errors.Is(err, store.ErrBusy):
		w.Header().Set("Retry-After", "60")
		oauthError(w, 429, "temporarily_unavailable", "too many registered clients")
	case err != nil:
		oauthError(w, 503, "temporarily_unavailable", "registration is temporarily unavailable")
	default:
		w.Header().Set("Cache-Control", "no-store")
		write(w, 201, map[string]any{"client_id": client.ID, "client_id_issued_at": client.CreatedAt.Unix(), "client_name": client.Name, "redirect_uris": client.RedirectURIs, "grant_types": []string{"authorization_code"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none"})
	}
}

func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	form := url.Values{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var in map[string]string
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			oauthError(w, 400, "invalid_request", "send the token request as a form or a JSON object of strings")
			return
		}
		for key, value := range in {
			form.Set(key, value)
		}
	} else if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request", "send the token request as application/x-www-form-urlencoded")
		return
	} else {
		form = r.PostForm
	}
	if form.Get("grant_type") != "authorization_code" {
		oauthError(w, 400, "unsupported_grant_type", "only authorization_code is supported")
		return
	}
	exchange := store.OAuthExchange{ClientID: form.Get("client_id"), RedirectURI: form.Get("redirect_uri"), Verifier: form.Get("code_verifier")}
	if form.Get("code") == "" || exchange.ClientID == "" || exchange.RedirectURI == "" || exchange.Verifier == "" {
		oauthError(w, 400, "invalid_request", "code, code_verifier, client_id and redirect_uri are required")
		return
	}
	session, err := s.Store.ExchangeOAuthCode(r.Context(), form.Get("code"), exchange)
	switch {
	case errors.Is(err, store.ErrUnauthorized):
		oauthError(w, 400, "invalid_grant", "the authorization code is invalid, expired, already used, or does not match this client, redirect_uri and code_verifier")
	case errors.Is(err, store.ErrDenied):
		oauthError(w, 400, "invalid_grant", "the user denied this authorization")
	case errors.Is(err, store.ErrForbidden):
		oauthError(w, 400, "invalid_grant", "the user can no longer grant this access")
	case err != nil:
		oauthError(w, 503, "temporarily_unavailable", "the token could not be issued; authorize again")
	default:
		w.Header().Set("Cache-Control", "no-store")
		write(w, 200, map[string]any{"access_token": session.Token, "token_type": "Bearer", "expires_in": int(time.Until(session.ExpiresAt).Seconds()), "scope": strings.Join(session.User.Permissions, " ")})
	}
}

// mcpResource reads the RFC 8707 resource: this installation's MCP endpoint for
// one project environment. It returns the permissions the connection needs.
func (s *Server) mcpResource(raw string) (project, environment string, permissions []string, err error) {
	u, parseErr := url.Parse(raw)
	if parseErr != nil || u.Fragment != "" || u.User != nil || u.Scheme+"://"+u.Host+u.Path != s.oauthIssuer()+"/api/v1/mcp" {
		return "", "", nil, errors.New("resource must be this installation's MCP endpoint, " + s.oauthIssuer() + "/api/v1/mcp")
	}
	permissions = []string{"deployments:read", "logs:read"}
	for name, values := range u.Query() {
		switch {
		case len(values) != 1:
			return "", "", nil, errors.New(name + " may appear only once in the connector URL")
		case name == "project":
			project = values[0]
		case name == "environment":
			environment = values[0]
		case name == "allow_deploy" && (values[0] == "true" || values[0] == "false"):
			if values[0] == "true" {
				permissions = append(permissions, "deployments:write")
			}
		case strings.HasPrefix(name, "allow_") || name == "installation" || name == "host_only" || name == "application":
			return "", "", nil, errors.New(name + " is not available with sign-in; create an API key for this connection")
		default:
			return "", "", nil, errors.New("the connector URL has an unknown option: " + name)
		}
	}
	if !validScope(project, environment) {
		return "", "", nil, errors.New("add ?project=…&environment=… to the connector URL")
	}
	return project, environment, permissions, nil
}

func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	if s.Auth.PublicURL == "" {
		http.NotFound(w, r)
		return
	}
	var in struct {
		ClientID            string `json:"client_id"`
		RedirectURI         string `json:"redirect_uri"`
		ResponseType        string `json:"response_type"`
		CodeChallenge       string `json:"code_challenge"`
		CodeChallengeMethod string `json:"code_challenge_method"`
		Resource            string `json:"resource"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ResponseType != "code" || in.CodeChallengeMethod != "S256" || in.CodeChallenge == "" {
		problem(w, 400, "invalid_request", "the client must request response_type=code with an S256 PKCE challenge")
		return
	}
	project, environment, permissions, err := s.mcpResource(in.Resource)
	if err != nil {
		problem(w, 400, "invalid_target", err.Error())
		return
	}
	v, client, err := s.Store.StartOAuthDevice(r.Context(), who(r), in.ClientID, in.RedirectURI, in.CodeChallenge, project, environment, permissions)
	switch {
	case errors.Is(err, store.ErrUnknownOAuthClient):
		problem(w, 400, "invalid_client", "This client is not registered here, or it asked to return to an address it did not register. Connect it again.")
	case errors.Is(err, store.ErrInput):
		problem(w, 400, "invalid_request", "The project environment does not exist, or the PKCE challenge is malformed.")
	case err != nil:
		authFailure(w, err)
	default:
		redirect, _ := url.Parse(in.RedirectURI)
		w.Header().Set("Cache-Control", "no-store")
		write(w, 200, map[string]any{"device_code": v.DeviceCode, "user_code": v.UserCode, "client_name": client.Name, "redirect_host": redirect.Host, "project": project, "environment": environment, "permissions": permissions, "issuer": s.oauthIssuer()})
	}
}
