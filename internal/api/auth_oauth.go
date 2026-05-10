package api

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *Server) oauthConfig(provider string) (*oauth2.Config, error) {
	c := &oauth2.Config{RedirectURL: strings.TrimSuffix(s.Auth.PublicURL, "/") + "/api/v1/auth/oauth/" + provider + "/callback"}
	switch provider {
	case "github":
		c.ClientID = s.Auth.GitHubClientID
		c.ClientSecret = s.Auth.GitHubClientSecret
		c.Endpoint = oauth2.Endpoint{AuthURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token", AuthStyle: oauth2.AuthStyleInParams}
		c.Scopes = []string{"read:user", "user:email"}
		if s.Auth.GitHubAuthURL != "" {
			c.Endpoint.AuthURL = s.Auth.GitHubAuthURL
		}
		if s.Auth.GitHubTokenURL != "" {
			c.Endpoint.TokenURL = s.Auth.GitHubTokenURL
		}
	case "google":
		c.ClientID = s.Auth.GoogleClientID
		c.ClientSecret = s.Auth.GoogleClientSecret
		c.Endpoint = oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", AuthStyle: oauth2.AuthStyleInParams}
		c.Scopes = []string{"openid", "email", "profile"}
		if s.Auth.GoogleAuthURL != "" {
			c.Endpoint.AuthURL = s.Auth.GoogleAuthURL
		}
		if s.Auth.GoogleTokenURL != "" {
			c.Endpoint.TokenURL = s.Auth.GoogleTokenURL
		}
	default:
		return nil, store.ErrInput
	}
	u, err := url.Parse(s.Auth.PublicURL)
	if err != nil || u.Host == "" || c.ClientID == "" || c.ClientSecret == "" {
		return nil, fmt.Errorf("%w: identity provider is not configured", store.ErrInput)
	}
	return c, nil
}
func (s *Server) authOAuthStart(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	config, err := s.oauthConfig(provider)
	if err != nil {
		problem(w, 409, "provider_not_configured", "this sign-in provider has not been configured by the operator")
		return
	}
	verifier := oauth2.GenerateVerifier()
	binding := store.NewID() + store.NewID()
	state, err := s.Store.NewChallenge(r.Context(), "oauth", map[string]string{"provider": provider, "verifier": verifier, "binding": binding}, 5*time.Minute)
	if err != nil {
		authFailure(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "hakopod_oauth", Value: binding, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.Auth.PublicURL, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: 300})
	http.Redirect(w, r, config.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

type oauthIdentity struct{ Subject, Email, Name string }

func decodeProvider(ctx context.Context, client *http.Client, endpoint, token string, v any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return store.ErrUnauthorized
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return store.ErrUnauthorized
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(v)
}
func (s *Server) providerIdentity(ctx context.Context, client *http.Client, provider, token string) (oauthIdentity, error) {
	if provider == "github" {
		base := "https://api.github.com"
		if s.Auth.GitHubAPIURL != "" {
			base = strings.TrimSuffix(s.Auth.GitHubAPIURL, "/")
		}
		var u struct {
			ID    int64  `json:"id"`
			Name  string `json:"name"`
			Login string `json:"login"`
		}
		if err := decodeProvider(ctx, client, base+"/user", token, &u); err != nil || u.ID <= 0 {
			return oauthIdentity{}, store.ErrUnauthorized
		}
		var emails []struct {
			Email    string `json:"email"`
			Verified bool   `json:"verified"`
			Primary  bool   `json:"primary"`
		}
		if err := decodeProvider(ctx, client, base+"/user/emails", token, &emails); err != nil {
			return oauthIdentity{}, store.ErrUnauthorized
		}
		for _, email := range emails {
			if email.Verified && email.Primary {
				normalized, err := store.NormalizeEmail(email.Email)
				if err != nil {
					return oauthIdentity{}, store.ErrUnauthorized
				}
				name := u.Name
				if name == "" {
					name = u.Login
				}
				return oauthIdentity{strconv.FormatInt(u.ID, 10), normalized, name}, nil
			}
		}
		return oauthIdentity{}, store.ErrUnauthorized
	}
	endpoint := "https://openidconnect.googleapis.com/v1/userinfo"
	if s.Auth.GoogleUserInfoURL != "" {
		endpoint = s.Auth.GoogleUserInfoURL
	}
	var u struct {
		Sub      string `json:"sub"`
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
		Name     string `json:"name"`
	}
	if err := decodeProvider(ctx, client, endpoint, token, &u); err != nil || !u.Verified || u.Sub == "" {
		return oauthIdentity{}, store.ErrUnauthorized
	}
	normalized, err := store.NormalizeEmail(u.Email)
	if err != nil {
		return oauthIdentity{}, store.ErrUnauthorized
	}
	return oauthIdentity{u.Sub, normalized, u.Name}, nil
}
func (s *Server) authOAuthCallback(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	config, err := s.oauthConfig(provider)
	if err != nil {
		authFailure(w, err)
		return
	}
	cookie, err := r.Cookie("hakopod_oauth")
	if err != nil {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	challenge, err := s.Store.ConsumeChallenge(r.Context(), r.URL.Query().Get("state"), "oauth")
	if err != nil {
		authFailure(w, err)
		return
	}
	var pending map[string]string
	if json.Unmarshal(challenge.Data, &pending) != nil || pending["provider"] != provider || subtle.ConstantTimeCompare([]byte(pending["binding"]), []byte(cookie.Value)) != 1 {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "hakopod_oauth", Value: "", Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.Auth.PublicURL, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: -1})
	if r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), oauth2.HTTPClient, client), 15*time.Second)
	defer cancel()
	token, err := config.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(pending["verifier"]))
	if err != nil {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	verified, err := s.providerIdentity(ctx, client, provider, token.AccessToken)
	if err != nil {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		authFailure(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, "SELECT i.id FROM oauth_identities o JOIN identities i ON i.id=o.identity_id WHERE o.provider=$1 AND o.subject=$2 AND NOT i.disabled", provider, verified.Subject).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = tx.QueryRow(ctx, "SELECT id FROM identities WHERE email=$1 AND NOT disabled FOR UPDATE", verified.Email).Scan(&id); err != nil {
			problem(w, 403, "invitation_required", "accept an invitation before signing in with this provider")
			return
		}
		if _, err = tx.Exec(ctx, "INSERT INTO oauth_identities(provider,subject,identity_id) VALUES($1,$2,$3)", provider, verified.Subject, id); err != nil {
			authFailure(w, store.ErrConflict)
			return
		}
	} else if err != nil {
		authFailure(w, err)
		return
	}
	if _, err = tx.Exec(ctx, "UPDATE identities SET email_verified=true WHERE id=$1 AND email=$2", id, verified.Email); err != nil {
		authFailure(w, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		authFailure(w, err)
		return
	}
	var mfa bool
	if err = s.Store.Pool.QueryRow(ctx, "SELECT totp_secret IS NOT NULL FROM identities WHERE id=$1", id).Scan(&mfa); err != nil {
		authFailure(w, err)
		return
	}
	if mfa {
		challenge, err := s.Store.NewChallenge(ctx, "mfa-login", map[string]string{"identity_id": id}, 5*time.Minute)
		if err != nil {
			authFailure(w, err)
			return
		}
		write(w, 200, map[string]any{"mfa_required": true, "challenge": challenge})
		return
	}
	session, err := s.Store.NewSession(ctx, id, "browser", "", "", nil)
	if err != nil {
		authFailure(w, err)
		return
	}
	s.sessionResponse(w, r, session)
}
func (s *Server) authMFAComplete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	c, err := s.Store.ConsumeChallenge(r.Context(), in.Challenge, "mfa-login")
	if err != nil {
		authFailure(w, err)
		return
	}
	var pending map[string]string
	if json.Unmarshal(c.Data, &pending) != nil {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	var encrypted []byte
	var step int64
	if err = s.Store.Pool.QueryRow(r.Context(), "SELECT totp_secret,totp_step FROM identities WHERE id=$1 AND NOT disabled AND (locked_until IS NULL OR locked_until<now())", pending["identity_id"]).Scan(&encrypted, &step); err != nil {
		authFailure(w, store.ErrUnauthorized)
		return
	}
	if err = s.verifyMFA(r.Context(), pending["identity_id"], encrypted, step, in.Code); err != nil {
		authFailure(w, err)
		return
	}
	session, err := s.Store.NewSession(r.Context(), pending["identity_id"], "browser", "", "", nil)
	if err != nil {
		authFailure(w, err)
		return
	}
	s.sessionResponse(w, r, session)
}
func (s *Server) sendAuthMail(ctx context.Context, recipient, subject, body string) error {
	if !s.Auth.SMTPAllowDelivery || s.Auth.SMTPAddress == "" {
		return store.ErrForbidden
	}
	recipient, err := store.NormalizeEmail(recipient)
	if err != nil {
		return err
	}
	from, err := store.NormalizeEmail(s.Auth.SMTPFrom)
	if err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(s.Auth.SMTPAddress)
	if err != nil {
		return err
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", s.Auth.SMTPAddress)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err = client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	} else {
		ip := net.ParseIP(host)
		if !s.Auth.SMTPAllowInsecure || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return errors.New("SMTP requires STARTTLS")
		}
	}
	if s.Auth.SMTPUsername != "" {
		if err = client.Auth(smtp.PlainAuth("", s.Auth.SMTPUsername, s.Auth.SMTPPassword, host)); err != nil {
			return err
		}
	}
	if err = client.Mail(from); err != nil {
		return err
	}
	if err = client.Rcpt(recipient); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", from, recipient, subject, strings.ReplaceAll(body, "\n", "\r\n"))
	if err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}
