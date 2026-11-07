package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// OAuthClient is a self-registered public MCP client. It never holds a secret.
type OAuthClient struct {
	ID           string    `json:"client_id"`
	Name         string    `json:"client_name"`
	RedirectURIs []string  `json:"redirect_uris"`
	CreatedAt    time.Time `json:"-"`
}

// OAuthExchange is what the token request presents for an authorization code.
type OAuthExchange struct{ ClientID, RedirectURI, Verifier string }

var pkceChallenge = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func (x OAuthExchange) matches(client, redirectURI, challenge string) bool {
	sum := sha256.Sum256([]byte(x.Verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	// RFC 7636: the verifier is 43-128 unreserved characters.
	return len(x.Verifier) >= 43 && len(x.Verifier) <= 128 &&
		subtle.ConstantTimeCompare([]byte(x.ClientID), []byte(client)) == 1 &&
		subtle.ConstantTimeCompare([]byte(x.RedirectURI), []byte(redirectURI)) == 1 &&
		subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

// ValidRedirectURI accepts https, or plain http only on a loopback host for
// desktop and command-line clients. Fragments and credentials are refused.
func ValidRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2000 || strings.Contains(raw, "#") || u.User != nil || u.Opaque != "" || u.Hostname() == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		return host == "localhost" || (ip != nil && ip.IsLoopback())
	}
	return false
}

func (s *Store) RegisterOAuthClient(ctx context.Context, name string, redirectURIs []string) (OAuthClient, error) {
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 100 || len(redirectURIs) == 0 || len(redirectURIs) > 10 {
		return OAuthClient{}, ErrInput
	}
	for _, uri := range redirectURIs {
		if !ValidRedirectURI(uri) {
			return OAuthClient{}, ErrInput
		}
	}
	if name == "" {
		name = "MCP client"
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return OAuthClient{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044219)"); err != nil {
		return OAuthClient{}, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM oauth_clients WHERE last_used_at<now()-interval '30 days'"); err != nil {
		return OAuthClient{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM oauth_clients").Scan(&count); err != nil {
		return OAuthClient{}, err
	}
	if count >= 1000 {
		return OAuthClient{}, ErrBusy
	}
	client := OAuthClient{ID: NewID(), Name: name, RedirectURIs: redirectURIs}
	if err = tx.QueryRow(ctx, "INSERT INTO oauth_clients(id,client_name,redirect_uris) VALUES($1,$2,$3) RETURNING created_at", client.ID, client.Name, client.RedirectURIs).Scan(&client.CreatedAt); err != nil {
		return OAuthClient{}, err
	}
	return client, tx.Commit(ctx)
}

// StartOAuthDevice opens a device code for a browser user's OAuth authorization.
// The user approves it through the device consent; the client redeems it with PKCE.
func (s *Store) StartOAuthDevice(ctx context.Context, p Principal, clientID, redirectURI, challenge, project, environment string, permissions []string) (DeviceAuthorization, OAuthClient, error) {
	if p.CredentialType != "browser" {
		return DeviceAuthorization{}, OAuthClient{}, ErrForbidden
	}
	if project == "" || !pkceChallenge.MatchString(challenge) {
		return DeviceAuthorization{}, OAuthClient{}, ErrInput
	}
	var client OAuthClient
	err := s.Pool.QueryRow(ctx, "UPDATE oauth_clients SET last_used_at=now() WHERE id=$1 RETURNING id,client_name,redirect_uris,created_at", clientID).Scan(&client.ID, &client.Name, &client.RedirectURIs, &client.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeviceAuthorization{}, OAuthClient{}, ErrUnknownOAuthClient
	}
	if err != nil {
		return DeviceAuthorization{}, OAuthClient{}, err
	}
	if !slices.Contains(client.RedirectURIs, redirectURI) {
		return DeviceAuthorization{}, OAuthClient{}, ErrUnknownOAuthClient
	}
	v, err := s.startDevice(ctx, project, environment, permissions, oauthGrant{identity: p.ID, client: client.ID, redirectURI: redirectURI, challenge: challenge})
	return v, client, err
}

var ErrUnknownOAuthClient = errors.New("unknown client or unregistered redirect_uri")

// ExchangeOAuthCode redeems an approved authorization code once.
func (s *Store) ExchangeOAuthCode(ctx context.Context, code string, exchange OAuthExchange) (Session, error) {
	return s.pollDevice(ctx, code, &exchange)
}
